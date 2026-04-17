package workflow

import (
	"context"
	"fmt"
	"sync"

	ghapi "github.com/maszynka/gh-crfix/internal/github"
	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/progress"
	"github.com/maszynka/gh-crfix/internal/worktree"
)

// SetupMaxConcurrency caps the number of PRs that can run their setup phase
// in parallel, mirroring the bash SETUP_MAX_CONCURRENCY=8 constant.
const SetupMaxConcurrency = 8

// splitOwnerRepo splits "owner/name" into (owner, name). Unexpected shapes
// return the input as owner with an empty repo — callers that use this for
// MatchesRepo treat that as a non-match safely.
func splitOwnerRepo(repo string) (string, string) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			return repo[:i], repo[i+1:]
		}
	}
	return repo, ""
}

// PreparedPR is one PR after the setup phase. Status is one of
// "ready" | "skipped" | "failed".
type PreparedPR struct {
	PRNum      int
	Title      string
	HeadBranch string
	BaseBranch string
	HeadSHA    string
	Worktree   string
	Threads    int
	HasCaseCol bool
	Status     string
	Reason     string
}

// --- Small interfaces used by setupOnePR so tests can fake I/O --------------

// The setup interfaces keep their historical (no-ctx) shapes so existing
// tests that implement them with fakes (see setup_test.go) don't have to
// change. The real adapters stamp context.Background() onto every call —
// the cancellation semantics in the setup phase are already handled by the
// outer goroutine pool in ProcessBatch, and the blocking work is dominated
// by local git operations that return quickly on a real disk.
//
// If deeper ctx plumbing is needed here in the future, add ctx-bearing
// overloads and gate the old ones on a transitional adapter.
type prFetcher interface {
	FetchPR(repo string, prNum int) (ghapi.PRInfo, error)
}

type worktreeSetup interface {
	Setup(repoRoot, branch string, prNum int) (string, error)
	DirtyStatus(path string) (string, error)
	DetectCaseCollisions(path string) ([][]string, error)
	RepoRoot(path string) (string, error)
}

type threadFetcher interface {
	FetchThreads(repo string, prNum, maxThreads int) ([]ghapi.Thread, error)
}

// Default production adapters ------------------------------------------------

type realPRFetcher struct{ ctx context.Context }

func (r realPRFetcher) FetchPR(repo string, prNum int) (ghapi.PRInfo, error) {
	return ghapi.FetchPR(r.pickCtx(), repo, prNum)
}

func (r realPRFetcher) pickCtx() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

type realWorktreeSetup struct{ ctx context.Context }

func (r realWorktreeSetup) Setup(repoRoot, branch string, prNum int) (string, error) {
	return worktree.Setup(r.pickCtx(), repoRoot, branch, prNum)
}
func (r realWorktreeSetup) DirtyStatus(path string) (string, error) {
	return worktree.DirtyStatus(path)
}
func (r realWorktreeSetup) DetectCaseCollisions(path string) ([][]string, error) {
	return worktree.DetectCaseCollisions(path)
}
func (r realWorktreeSetup) RepoRoot(path string) (string, error) {
	return worktree.RepoRoot(r.pickCtx(), path)
}
func (r realWorktreeSetup) pickCtx() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

type realThreadFetcher struct{ ctx context.Context }

func (r realThreadFetcher) FetchThreads(repo string, prNum, maxThreads int) ([]ghapi.Thread, error) {
	if r.ctx == nil {
		return ghapi.FetchThreads(context.Background(), repo, prNum, maxThreads)
	}
	return ghapi.FetchThreads(r.ctx, repo, prNum, maxThreads)
}

// SetupPhase runs per-PR setup in parallel up to `concurrency` (capped by
// SetupMaxConcurrency). The returned slice preserves input order from
// opts.PRNums. run and tracker may be nil (tests use that to keep things
// pure).
func SetupPhase(ctx context.Context, opts BatchOptions, run *logs.Run, tracker *progress.Tracker, concurrency int) []PreparedPR {
	return setupPhaseWith(opts, realPRFetcher{ctx: ctx}, realWorktreeSetup{ctx: ctx}, realThreadFetcher{ctx: ctx}, run, tracker, concurrency)
}

// setupPhaseWith is the testable core of SetupPhase.
func setupPhaseWith(
	opts BatchOptions,
	prf prFetcher,
	wt worktreeSetup,
	tf threadFetcher,
	run *logs.Run,
	tracker *progress.Tracker,
	concurrency int,
) []PreparedPR {
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > SetupMaxConcurrency {
		concurrency = SetupMaxConcurrency
	}
	if concurrency > len(opts.PRNums) && len(opts.PRNums) > 0 {
		concurrency = len(opts.PRNums)
	}

	out := make([]PreparedPR, len(opts.PRNums))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, prNum := range opts.PRNums {
		wg.Add(1)
		sem <- struct{}{}
		go func(i, prNum int) {
			defer wg.Done()
			defer func() { <-sem }()

			o := opts.Base
			o.PRNum = prNum
			out[i] = setupOnePR(o, prf, wt, tf, run, tracker)
		}(i, prNum)
	}
	wg.Wait()
	return out
}

// setupOnePR runs the pure setup logic for a single PR. It mirrors the bash
// `setup_pr` function: fetch PR metadata, bail on non-OPEN / not-found,
// create/reset the worktree, detect case collisions (mark dirty-but-fixable
// as ready + HasCaseCol), fetch threads, and handle the SetupOnly short-circuit.
//
// run and tracker may be nil (tests pass nil). All logging/progress updates
// are no-ops in that case.
func setupOnePR(
	opts Options,
	prf prFetcher,
	wt worktreeSetup,
	tf threadFetcher,
	run *logs.Run,
	tracker *progress.Tracker,
) PreparedPR {
	pr := PreparedPR{PRNum: opts.PRNum}

	logMaster := func(format string, a ...interface{}) {
		if run != nil {
			run.Mlog("[setup-pr] PR #%d: "+format, append([]interface{}{opts.PRNum}, a...)...)
		}
	}
	logPR := func(format string, a ...interface{}) {
		if run != nil {
			run.MlogTo(run.PRLog(opts.PRNum), "[setup-pr] "+format, a...)
		}
	}
	setStep := func(status progress.Status, note string) {
		if tracker != nil {
			_ = tracker.Set(opts.PRNum, progress.StepSetup, status, note)
		}
	}
	markStarted := func() {
		if run != nil {
			_ = run.MarkStarted(opts.PRNum)
		}
	}
	markStatus := func(ok bool) {
		if run != nil {
			_ = run.MarkStatus(opts.PRNum, ok)
		}
	}

	// progress writes one-liner setup-phase updates to opts.ProgressOut.
	// These are safe to call even when ProgressOut is nil. The dashboard
	// caller wires stderr here (stderr is not redirected even when the
	// dashboard takes over stdout), so the user still sees some feedback
	// during the otherwise-silent setup phase.
	prog := func(format string, a ...interface{}) {
		if opts.ProgressOut == nil {
			return
		}
		fmt.Fprintf(opts.ProgressOut, "[setup] PR #%d: "+format+"\n",
			append([]interface{}{opts.PRNum}, a...)...)
	}

	markStarted()
	setStep(progress.Running, "")
	logMaster("starting setup")
	logPR("starting setup")
	prog("fetching metadata...")

	// 1. Fetch PR metadata.
	info, err := prf.FetchPR(opts.Repo, opts.PRNum)
	if err != nil {
		pr.Status = "skipped"
		pr.Reason = "not found"
		logMaster("gh pr view failed: %v -- marking as %s (%s)", err, pr.Status, pr.Reason)
		logPR("gh pr view failed: %v", err)
		prog("skipped (%s)", pr.Reason)
		setStep(progress.Skipped, pr.Reason)
		markStatus(false)
		return pr
	}
	pr.Title = info.Title
	pr.HeadBranch = info.HeadRefName
	pr.BaseBranch = info.BaseRefName
	pr.HeadSHA = info.HeadSHA

	if info.State != "OPEN" {
		pr.Status = "skipped"
		pr.Reason = fmt.Sprintf("PR is %s", info.State)
		logMaster("state=%s -- skipping", info.State)
		logPR("state=%s -- skipping", info.State)
		prog("skipped (%s)", pr.Reason)
		setStep(progress.Skipped, pr.Reason)
		// Non-OPEN PRs are a clean no-op, not a failure.
		markStatus(true)
		return pr
	}

	logMaster("branch=%q base=%q title=%q", info.HeadRefName, info.BaseRefName, info.Title)
	logPR("branch=%q base=%q title=%q", info.HeadRefName, info.BaseRefName, info.Title)

	// 2. Resolve repo root. A generic "worktree setup failed" here leaves
	// users guessing — surface the two common root causes explicitly so they
	// know how to recover.
	repoRoot := opts.RepoRoot
	if repoRoot == "" {
		rr, rerr := wt.RepoRoot(".")
		if rerr != nil {
			pr.Status = "failed"
			pr.Reason = "not in a git repository — cd into your clone or set GH_CRFIX_DIR=/path/to/clone"
			logMaster("could not resolve repo root: %v", rerr)
			logPR("could not resolve repo root: %v", rerr)
			prog("failed (%s)", pr.Reason)
			setStep(progress.Failed, pr.Reason)
			markStatus(false)
			return pr
		}
		repoRoot = rr
	}

	// Validate the resolved repo root actually points at the target PR's
	// repo. A mismatched origin is the other common "worktree setup failed"
	// root cause — users run gh crfix from a different clone than the PR.
	owner, repoName := splitOwnerRepo(opts.Repo)
	if owner != "" && repoName != "" {
		if ok, mismatchMsg, werr := worktree.MatchesRepo(repoRoot, owner, repoName); werr == nil && !ok {
			pr.Status = "failed"
			pr.Reason = mismatchMsg
			logMaster("origin mismatch: %s", mismatchMsg)
			logPR("origin mismatch: %s", mismatchMsg)
			setStep(progress.Failed, pr.Reason)
			markStatus(false)
			return pr
		}
	}

	// 3. Set up worktree.
	wtPath, err := wt.Setup(repoRoot, info.HeadRefName, opts.PRNum)
	if err != nil {
		pr.Status = "failed"
		pr.Reason = "worktree setup failed"
		logMaster("worktree setup error: %v", err)
		logPR("worktree setup error: %v", err)
		prog("failed (%s)", pr.Reason)
		setStep(progress.Failed, pr.Reason)
		markStatus(false)
		return pr
	}
	pr.Worktree = wtPath

	// 4. Detect case collisions while the worktree may still be dirty.
	if dirty, _ := wt.DirtyStatus(wtPath); dirty != "" {
		groups, _ := wt.DetectCaseCollisions(wtPath)
		if len(groups) > 0 {
			pr.HasCaseCol = true
			logMaster("detected %d case-collision group(s) -- deferring to process phase", len(groups))
			logPR("detected %d case-collision group(s)", len(groups))
		} else {
			pr.Status = "failed"
			pr.Reason = "worktree not clean"
			logMaster("worktree dirty with no recoverable case collisions -- failing")
			logPR("worktree dirty: %s", firstLine(dirty))
			prog("failed (%s)", pr.Reason)
			setStep(progress.Failed, pr.Reason)
			markStatus(false)
			return pr
		}
	}

	// 5. Fetch unresolved review threads.
	threads, err := tf.FetchThreads(opts.Repo, opts.PRNum, opts.MaxThreads)
	if err != nil {
		pr.Status = "failed"
		pr.Reason = "fetch threads failed"
		logMaster("fetch threads error: %v", err)
		logPR("fetch threads error: %v", err)
		prog("failed (%s)", pr.Reason)
		setStep(progress.Failed, pr.Reason)
		markStatus(false)
		return pr
	}
	pr.Threads = len(threads)

	if pr.Threads == 0 {
		pr.Status = "skipped"
		pr.Reason = "no unresolved threads"
		logMaster("no unresolved threads -- skipping")
		logPR("no unresolved threads")
		prog("skipped (%s)", pr.Reason)
		setStep(progress.Skipped, pr.Reason)
		markStatus(true)
		return pr
	}

	if opts.SetupOnly {
		pr.Status = "skipped"
		pr.Reason = "setup-only"
		logMaster("setup-only: worktree ready at %s", wtPath)
		logPR("setup-only: cd %s", wtPath)
		prog("skipped (setup-only)")
		setStep(progress.Done, "setup-only")
		markStatus(true)
		return pr
	}

	pr.Status = "ready"
	pr.Reason = "ready"
	logMaster("ready -- %d thread(s), worktree=%s", pr.Threads, wtPath)
	logPR("ready -- %d thread(s)", pr.Threads)
	prog("ready (%d thread(s))", pr.Threads)
	setStep(progress.Done, fmt.Sprintf("%d thread(s)", pr.Threads))
	return pr
}
