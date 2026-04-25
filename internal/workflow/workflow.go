// Package workflow orchestrates the full gh-crfix PR processing pipeline.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/conflict"
	"github.com/maszynka/gh-crfix/internal/gate"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/progress"
	"github.com/maszynka/gh-crfix/internal/triage"
	"github.com/maszynka/gh-crfix/internal/validate"
)

// ThreadResponse is written by the fix model (and by deterministic logic)
// to describe what was done for each thread.
type ThreadResponse struct {
	ThreadID           string `json:"thread_id"`
	Action             string `json:"action"` // fixed | skipped | already_fixed
	Comment            string `json:"comment"`
	ResolveWhenSkipped bool   `json:"resolve_when_skipped,omitempty"`
}

// Options configures a single PR processing run.
type Options struct {
	Repo            string
	PRNum           int
	RepoRoot        string // local git clone root; empty = auto-detect from cwd
	AIBackend       ai.Backend
	GateModel       string
	FixModel        string
	MaxThreads      int
	IncludeOutdated bool
	ValidateHook    string
	AutofixHook     string
	DryRun          bool
	ResolveSkipped  bool
	NoResolve       bool
	NoPostFix       bool
	NoAutofix       bool
	NoValidate      bool
	SetupOnly       bool
	WorktreeMode    string // "temp" | "reuse" | "stash" (empty = "temp")
	ReviewWaitSecs  int
	Weights         gate.ScoreWeights
	Verbose         bool
	LogDir          string // batch-level log dir; workflow writes per-PR logs here

	// Run is the batch-level log run. When nil, logging is a no-op and the
	// workflow prints to stdout exactly as it did before.
	Run *logs.Run
	// Tracker records per-step progress. When nil, all tracker updates are no-ops.
	Tracker *progress.Tracker
	// SetupMaxConc optionally overrides the setup-phase concurrency cap. A
	// zero value defaults to SetupMaxConcurrency in batch.go.
	SetupMaxConc int

	// Out is the writer used for the per-PR human-readable log lines (the
	// `[PR #N] ...` narrative). When nil, ProcessPR falls back to os.Stdout.
	// Setting this to a bytes.Buffer lets tests capture output without
	// swapping the global os.Stdout, and lets a dashboard redirect those
	// lines into the master log without the global `os.Stdout = logfile`
	// hack currently in cmd/gh-crfix/main.go.
	Out io.Writer
	// ProgressOut receives coarse setup-phase progress lines (one per
	// setupOnePR start / result). Writing to stderr lets users see progress
	// even when stdout is redirected to the master log for a TUI. When nil,
	// setupOnePR emits no progress lines.
	ProgressOut io.Writer
}

// Result summarises the outcome of a single ProcessPR call. Filled in even on
// early-exit paths so a batch driver can print one summary line per PR.
type Result struct {
	PRNum        int
	Title        string
	Branch       string
	Status       string // ok | skipped | failed
	Reason       string
	Worktree     string
	Threads      int
	Replied      int
	Resolved     int
	Skipped      int
	FixModelRan  bool
	GateScore    float64
	Err          error
}

// OptionsFromConfig builds Options from a Config and overrides.
func OptionsFromConfig(cfg config.Config, repo string, prNum int) Options {
	mode := cfg.WorktreeMode
	if mode == "" {
		mode = "temp"
	}
	return Options{
		Repo:            repo,
		PRNum:           prNum,
		AIBackend:       ai.ParseBackend(cfg.AIBackend),
		GateModel:       cfg.GateModel,
		FixModel:        cfg.FixModel,
		MaxThreads:      100,
		IncludeOutdated: true,
		WorktreeMode:    mode,
		// 90s matches the bash `GH_CRFIX_REVIEW_WAIT` default and the README.
		ReviewWaitSecs: 90,
		Weights: gate.ScoreWeights{
			NeedsLLM:    cfg.ScoreNeedsLLM,
			PRComment:   cfg.ScorePRComment,
			TestFailure: cfg.ScoreTestFailure,
		},
	}
}

// ProcessPR runs the full gh-crfix pipeline for a single PR. The ctx is
// threaded through every long-running operation (ai, github, worktree) so
// cancellation or a deadline propagates end-to-end.
func ProcessPR(ctx context.Context, opts Options) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	res := Result{PRNum: opts.PRNum, Status: "failed"}
	// Fast-fail on already-cancelled ctx to avoid spawning subprocesses.
	if err := ctx.Err(); err != nil {
		res.Err = err
		res.Reason = "cancelled: " + err.Error()
		return res
	}
	// Resolve the log writer once so the closure doesn't re-evaluate the
	// fallback on every line. Defaulting to os.Stdout preserves the legacy
	// behavior when callers don't set opts.Out.
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	log := func(format string, a ...interface{}) {
		args := append([]interface{}{opts.PRNum}, a...)
		fmt.Fprintf(out, "  [PR #%d] "+format+"\n", args...)
		if opts.Run != nil {
			// Mirror the bash `[process-pr]` prefix so the master log carries
			// the same shape. `Mlog` handles timestamp + newline.
			opts.Run.Mlog("[process-pr] PR #%d: "+format, args...)
		}
	}
	setStep := func(step progress.Step, status progress.Status, note string) {
		if opts.Tracker != nil {
			_ = opts.Tracker.Set(opts.PRNum, step, status, note)
		}
	}

	// ── 1. Fetch PR metadata ──────────────────────────────────────────────
	log("fetching PR metadata...")
	pr, err := fetchPRFn(ctx, opts.Repo, opts.PRNum)
	if err != nil {
		res.Err = fmt.Errorf("fetch PR: %w", err)
		res.Reason = res.Err.Error()
		return res
	}
	res.Title = pr.Title
	res.Branch = pr.HeadRefName
	if pr.State != "OPEN" {
		res.Status = "skipped"
		res.Reason = fmt.Sprintf("PR is %s", pr.State)
		log("%s — skipping", res.Reason)
		return res
	}
	log("branch=%s  title=%q", pr.HeadRefName, pr.Title)

	// ── 2. Set up worktree ────────────────────────────────────────────────
	repoRoot := opts.RepoRoot
	if repoRoot == "" {
		var rerr error
		repoRoot, rerr = repoRootFn(ctx, ".")
		if rerr != nil {
			res.Err = fmt.Errorf("not in a git repo; set GH_CRFIX_DIR or cd into your clone: %w", rerr)
			res.Reason = res.Err.Error()
			return res
		}
	}
	log("setting up worktree...")
	setStep(progress.StepSetup, progress.Running, "")
	wtPath, err := setupWorktreeFn(ctx, repoRoot, pr.HeadRefName, opts.PRNum)
	if err != nil {
		res.Err = fmt.Errorf("worktree setup: %w", err)
		res.Reason = res.Err.Error()
		setStep(progress.StepSetup, progress.Failed, err.Error())
		return res
	}
	res.Worktree = wtPath
	setStep(progress.StepSetup, progress.Done, "worktree ready")

	// ── 3. Handle case collisions if the worktree is dirty ────────────────
	if dirty, _ := dirtyStatusFn(wtPath); dirty != "" {
		setStep(progress.StepNormalizeCase, progress.Running, "")
		if err := handleCaseCollisions(ctx, opts, wtPath, pr.HeadRefName); err != nil {
			log("case-collision: %v", err)
		}
		// Re-check — if still dirty, we can't proceed safely.
		if dirty2, _ := dirtyStatusFn(wtPath); dirty2 != "" {
			res.Reason = "worktree dirty before processing"
			log("%s", res.Reason)
			setStep(progress.StepNormalizeCase, progress.Failed, "dirty after LLM")
			return res
		}
		setStep(progress.StepNormalizeCase, progress.Done, "")
	} else {
		setStep(progress.StepNormalizeCase, progress.Skipped, "clean worktree")
	}

	// ── 4. Merge base branch ──────────────────────────────────────────────
	baseBranch := pr.BaseRefName
	if baseBranch == "" {
		baseBranch = "main"
	}
	log("merging base branch %q...", baseBranch)
	setStep(progress.StepMergeBase, progress.Running, "")
	if mergeErr := mergeBaseFn(ctx, wtPath, baseBranch); mergeErr != nil {
		log("warning: merge base failed: %v", mergeErr)
		setStep(progress.StepMergeBase, progress.Failed, mergeErr.Error())
	} else {
		setStep(progress.StepMergeBase, progress.Done, "")
	}

	// ── 5. Fix committed conflict markers ─────────────────────────────────
	// Snapshot whether conflict markers exist before fixing — used below to
	// distinguish "conflicts resolved" from "nothing to do" when there are
	// zero review threads.
	preFixMarkers, _ := detectMarkersFn(wtPath)
	hadConflicts := len(preFixMarkers) > 0

	setStep(progress.StepResolveConflicts, progress.Running, "")
	if err := fixConflictMarkers(ctx, opts, wtPath, log); err != nil {
		res.Reason = fmt.Sprintf("committed conflict markers could not be auto-fixed: %v", err)
		log("%s", res.Reason)
		setStep(progress.StepResolveConflicts, progress.Failed, err.Error())
		return res
	}
	setStep(progress.StepResolveConflicts, progress.Done, "")

	// ── 6. Fetch review threads ───────────────────────────────────────────
	log("fetching review threads...")
	setStep(progress.StepFetchThreads, progress.Running, "")
	rawThreads, err := fetchThreadsFn(ctx, opts.Repo, opts.PRNum, opts.MaxThreads)
	if err != nil {
		res.Err = fmt.Errorf("fetch threads: %w", err)
		res.Reason = res.Err.Error()
		setStep(progress.StepFetchThreads, progress.Failed, err.Error())
		return res
	}
	res.Threads = len(rawThreads)
	log("%d unresolved threads", len(rawThreads))
	setStep(progress.StepFetchThreads, progress.Done, fmt.Sprintf("%d threads", len(rawThreads)))

	if len(rawThreads) == 0 {
		if hadConflicts {
			// In dry-run mode fixConflictMarkers exits early without committing
			// or pushing the resolution, so the branch on origin is unchanged.
			// Reporting "ok / resolved merge conflicts" here would be misleading
			// for users and any automation that treats `ok` as a real
			// remediation. Surface dry-run as skipped instead.
			if opts.DryRun {
				res.Status = "skipped"
				res.Reason = "dry-run: merge conflicts detected (would resolve)"
				log("dry-run: merge conflicts detected, skipping resolution")
				return res
			}
			// Merge conflicts were present and auto-resolved (fixConflictMarkers
			// committed and pushed the resolution). No threads needed.
			res.Status = "ok"
			res.Reason = "resolved merge conflicts"
			log("no review threads — done (merge conflicts were resolved)")
			return res
		}
		res.Status = "skipped"
		res.Reason = "no unresolved threads"
		return res
	}
	if opts.SetupOnly {
		res.Status = "skipped"
		res.Reason = "setup-only"
		log("setup-only: cd %s", wtPath)
		return res
	}

	// ── 7. Triage threads ─────────────────────────────────────────────────
	setStep(progress.StepFilterThreads, progress.Running, "")
	classifications := make([]triage.Classification, 0, len(rawThreads))
	for _, rt := range rawThreads {
		t := toTriageThread(rt)
		c := triage.ClassifyThread(wtPath, t, opts.IncludeOutdated)
		classifications = append(classifications, c)
	}

	var skipList, autoList, alreadyFixedList, needsLLMList []triage.Classification
	for _, c := range classifications {
		switch c.Decision {
		case "skip":
			skipList = append(skipList, c)
		case "auto":
			autoList = append(autoList, c)
		case "already_likely_fixed":
			alreadyFixedList = append(alreadyFixedList, c)
		default:
			needsLLMList = append(needsLLMList, c)
		}
	}
	log("triage: skip=%d auto=%d already_fixed=%d needs_llm=%d",
		len(skipList), len(autoList), len(alreadyFixedList), len(needsLLMList))
	setStep(progress.StepFilterThreads, progress.Done,
		fmt.Sprintf("skip=%d auto=%d already=%d llm=%d",
			len(skipList), len(autoList), len(alreadyFixedList), len(needsLLMList)))

	// ── 8. Build deterministic responses ──────────────────────────────────
	responses := deterministicResponses(skipList, alreadyFixedList)

	// ── 8.5 Regenerate lockfile review threads deterministically ──────────
	// Before spending gate + fix tokens on the threads, intercept any thread
	// whose file is a lockfile (`bun.lock`, `pnpm-lock.yaml`, …). Those are
	// almost always "regenerate the lock" asks — which is `<pm> install`, a
	// deterministic, zero-token operation. We handle them here, emit
	// `fixed` responses, and drop them from the pools the gate will see.
	//
	// Skipped silently in dry-run (running install mutates the worktree).
	if !opts.DryRun {
		lockfixResponses, handled := regenerateLockfileThreads(
			ctx, opts, wtPath, &needsLLMList, &autoList, log,
		)
		responses = append(responses, lockfixResponses...)
		if handled > 0 {
			log("pre-gate: %d lockfile thread(s) handled deterministically — gate+fix will not see them", handled)
		}
	}

	// ── 9. Run autofix hook ───────────────────────────────────────────────
	autofixRan := false
	if !opts.NoAutofix {
		hookPath := opts.AutofixHook
		if hookPath == "" {
			hookPath = detectAutofixHook(wtPath)
		}
		if hookPath != "" {
			log("running autofix hook...")
			setStep(progress.StepAutofix, progress.Running, hookPath)
			if herr := runHook(ctx, hookPath, wtPath); herr != nil {
				// Autofix is best-effort; surface the error but continue.
				log("autofix hook failed: %v", herr)
				setStep(progress.StepAutofix, progress.Failed, herr.Error())
			} else {
				setStep(progress.StepAutofix, progress.Done, hookPath)
			}
			autofixRan = true
		}
	}
	if !autofixRan {
		setStep(progress.StepAutofix, progress.Skipped, "no hook")
	}

	// ── 10. Validation ────────────────────────────────────────────────────
	var validResult validate.Result
	if opts.NoValidate {
		log("validation skipped (disabled by --no-validate)")
		setStep(progress.StepValidate, progress.Skipped, "disabled by --no-validate")
	} else {
		runner := validate.Detect(wtPath, opts.ValidateHook)
		if runner.Kind != validate.RunnerNone {
			log("running validation (%s)...", runner.Command)
			setStep(progress.StepValidate, progress.Running, runner.Command)
			// Prefix every streamed line so users don't confuse test runner
			// output with gh-crfix's own progress. stream falls through to
			// stderr when a dashboard is active (opts.ProgressOut routes the
			// same way).
			stream := opts.ProgressOut
			if stream == nil {
				stream = out
			}
			validResult = validate.Run(ctx, wtPath, runner, prefixWriter(stream, "    "))
			if validResult.TestsFailed {
				log("validation: FAILED — %s", firstLine(validResult.Summary))
				setStep(progress.StepValidate, progress.Failed, firstLine(validResult.Summary))
			} else {
				log("validation: passed")
				setStep(progress.StepValidate, progress.Done, "passed")
			}
		} else {
			setStep(progress.StepValidate, progress.Skipped, "no runner")
		}
	}

	// ── 11. Fetch failing CI checks ───────────────────────────────────────
	// CI surfacing is best-effort but the error must be visible — silent
	// `nil, nil` previously masked regressions where the gate stopped
	// receiving CI context entirely.
	var ciChecks []ghapi.CICheck
	if pr.HeadSHA != "" {
		log("fetching CI check results...")
		var ciErr error
		ciChecks, ciErr = fetchFailingChecksFn(ctx, opts.Repo, pr.HeadSHA)
		if ciErr != nil {
			log("warning: fetch CI checks failed: %v (continuing without CI context)", ciErr)
		}
		if len(ciChecks) > 0 {
			log("%d failing CI check(s)", len(ciChecks))
		}
	}

	// ── 12. Gate scoring ──────────────────────────────────────────────────
	// auto threads without an autofix hook are treated as needs_llm.
	activeNeedsLLM := needsLLMList
	if !hasAutofixHook(wtPath) && opts.AutofixHook == "" {
		activeNeedsLLM = append(activeNeedsLLM, autoList...)
	}

	triageSummary := gate.TriageSummary{}
	for _, c := range activeNeedsLLM {
		triageSummary.NeedsLLM = append(triageSummary.NeedsLLM,
			gate.TriageEntry{ThreadID: c.ThreadID, Reason: c.Reason})
	}
	validationResult := gate.ValidationResult{TestsFailed: validResult.TestsFailed}
	gateCtx := gate.BuildGateContext(triageSummary, validationResult, opts.Weights)
	res.GateScore = gateCtx.TotalScore

	log("gate score=%.3f (threshold=%.1f) should_run=%v",
		gateCtx.TotalScore, gateCtx.Threshold, gateCtx.ShouldRunGate)

	// ── 13. Gate model ────────────────────────────────────────────────────
	var gateOut ai.GateOutput
	var selected []string
	// gateFailed tracks whether an invocation error occurred so downstream
	// fallbacks know not to treat needs_llm threads as already_fixed.
	gateFailed := false
	if gateCtx.ShouldRunGate && !opts.DryRun {
		log("running gate model (%s)...", opts.GateModel)
		setStep(progress.StepGate, progress.Running, opts.GateModel)
		prompt := buildGatePrompt(rawThreads, activeNeedsLLM, validResult, ciChecks, gateCtx)
		gateOut, err = runGateFn(ctx, opts.AIBackend, opts.GateModel, prompt, gate.GateSchema())
		if err != nil {
			log("gate model error: %v", err)
			setStep(progress.StepGate, progress.Failed, err.Error())
			gateFailed = true
			// Emit an explicit "failed" skipped response for every needs_llm
			// thread so the "already_fixed" fallback below does not silently
			// resolve them. ResolveWhenSkipped stays false — a human should
			// re-trigger once the gate model is healthy again.
			reason := firstLine(err.Error())
			if reason == "" {
				reason = "gate model failed"
			}
			for _, c := range activeNeedsLLM {
				responses = append(responses, ThreadResponse{
					ThreadID: c.ThreadID,
					Action:   "skipped",
					Comment: fmt.Sprintf(
						"gh crfix: gate model failed (%s) — leaving this thread unresolved for a human follow-up.",
						reason),
				})
			}
		} else {
			log("gate: needs_advanced_model=%v threads_to_fix=%v", gateOut.NeedsAdvancedModel, gateOut.ThreadsToFix)
			selected = gateOut.ThreadsToFix
			setStep(progress.StepGate, progress.Done,
				fmt.Sprintf("advanced=%v selected=%d", gateOut.NeedsAdvancedModel, len(selected)))
		}
	} else if !gateCtx.ShouldRunGate {
		setStep(progress.StepGate, progress.Skipped, "below threshold")
		// Below threshold — emit "skipped by score" responses.
		for _, c := range activeNeedsLLM {
			responses = append(responses, ThreadResponse{
				ThreadID: c.ThreadID,
				Action:   "skipped",
				Comment: fmt.Sprintf(
					"Skipped automatically: score below threshold (%.3f < 1), so no AI review was triggered.",
					gateCtx.TotalScore),
			})
		}
	}

	// ── 14. Fix model ─────────────────────────────────────────────────────
	// Run the fix model when EITHER the gate flag is set OR the gate returned
	// a non-empty ThreadsToFix list. Some models respond with flag=false +
	// non-empty list (contradictory but common); honoring the list prevents
	// those threads from being silently dropped as "no code change needed".
	shouldFix := gateOut.NeedsAdvancedModel || len(gateOut.ThreadsToFix) > 0
	if shouldFix && !opts.DryRun && !gateFailed {
		// If gate didn't nominate a list, send all active needs_llm threads.
		if len(selected) == 0 && len(activeNeedsLLM) > 0 {
			for _, c := range activeNeedsLLM {
				selected = append(selected, c.ThreadID)
			}
		}
		log("running fix model (%s) on %d thread(s)...", opts.FixModel, len(selected))
		setStep(progress.StepFix, progress.Running, fmt.Sprintf("%d thread(s)", len(selected)))
		fixPrompt := buildFixPrompt(rawThreads, activeNeedsLLM, selected, validResult, ciChecks)
		if ferr := runFixFn(ctx, opts.AIBackend, opts.FixModel, fixPrompt, wtPath); ferr != nil {
			log("fix model error: %v", ferr)
			setStep(progress.StepFix, progress.Failed, ferr.Error())
		} else {
			res.FixModelRan = true
			setStep(progress.StepFix, progress.Done, opts.FixModel)
		}
		// Read thread-responses.json written by the fix model.
		if fixResponses, rerr := readThreadResponses(wtPath); rerr == nil {
			responses = append(responses, fixResponses...)
		}
	} else if gateCtx.ShouldRunGate && !shouldFix && !gateFailed && !opts.DryRun {
		setStep(progress.StepFix, progress.Skipped, "gate said not needed")
		// Gate ran and said "not needed" — generate already-fixed responses so
		// needs_llm threads still get resolved. Mirrors Bash gate-skipped path.
		// Guards:
		//  - !gateFailed: a crashed gate model must not silently resolve real
		//    review threads as "already_fixed".
		//  - !opts.DryRun: in dry-run mode the gate model was intentionally
		//    skipped, so gateOut is zero-value; emitting `already_fixed` here
		//    would claim issues were addressed without running any AI.
		for _, c := range activeNeedsLLM {
			responses = append(responses, ThreadResponse{
				ThreadID: c.ThreadID,
				Action:   "already_fixed",
				Comment:  "Reviewed by automation — no code change needed: " + c.Reason + ".",
			})
		}
	} else if opts.DryRun && gateCtx.ShouldRunGate {
		setStep(progress.StepFix, progress.Skipped, "dry-run")
	}

	// ── 15. Uncovered-response fallback ───────────────────────────────────
	uncovered := uncoveredResponses(autoList, needsLLMList, responses, selected)
	responses = append(responses, uncovered...)

	// ── 16. Reply and resolve ─────────────────────────────────────────────
	if !opts.DryRun && !opts.NoResolve {
		setStep(progress.StepReply, progress.Running, "")
		replied, resolved, skipped := replyAndResolve(ctx, responses, opts.ResolveSkipped, log)
		res.Replied, res.Resolved, res.Skipped = replied, resolved, skipped
		log("replied=%d resolved=%d skipped=%d", replied, resolved, skipped)
		setStep(progress.StepReply, progress.Done,
			fmt.Sprintf("replied=%d resolved=%d skipped=%d", replied, resolved, skipped))
	} else if opts.DryRun {
		log("dry-run: would process %d responses", len(responses))
		setStep(progress.StepReply, progress.Skipped, "dry-run")
	} else {
		setStep(progress.StepReply, progress.Skipped, "no-resolve flag")
	}

	// ── 17. Remove thread-responses.json artifact ─────────────────────────
	if !opts.DryRun {
		setStep(progress.StepCleanup, progress.Running, "")
		cleanupThreadResponsesArtifact(wtPath)
		setStep(progress.StepCleanup, progress.Done, "")
	} else {
		setStep(progress.StepCleanup, progress.Skipped, "dry-run")
	}

	// ── 18. Post-fix summary comment ──────────────────────────────────────
	fixedCount := 0
	for _, r := range responses {
		if r.Action == "fixed" || r.Action == "already_fixed" {
			fixedCount++
		}
	}
	if !opts.DryRun {
		summary := buildSummaryComment(skipList, autoList, alreadyFixedList, needsLLMList, fixedCount, len(rawThreads))
		if cerr := postCommentFn(ctx, opts.Repo, opts.PRNum, summary); cerr != nil {
			log("post comment: %v", cerr)
		}
	}

	// ── 19. Request Copilot re-review ─────────────────────────────────────
	if !opts.DryRun {
		setStep(progress.StepRereview, progress.Running, "")
		if cerr := requestCopilotReviewFn(ctx, opts.Repo, opts.PRNum); cerr != nil {
			log("copilot re-review: %v", cerr)
			setStep(progress.StepRereview, progress.Failed, cerr.Error())
		} else {
			setStep(progress.StepRereview, progress.Done, "")
		}
	} else {
		setStep(progress.StepRereview, progress.Skipped, "dry-run")
	}

	// ── 20. Post-fix review cycle ─────────────────────────────────────────
	if !opts.NoPostFix && !opts.DryRun {
		setStep(progress.StepPostfix, progress.Running, "")
		postFixReviewCycle(ctx, opts, wtPath, fixedCount, log)
		setStep(progress.StepPostfix, progress.Done, "")
	} else {
		setStep(progress.StepPostfix, progress.Skipped, "disabled")
	}

	res.Status = "ok"
	res.Reason = "processed"
	return res
}

// replyAndResolve posts reply comments and resolves threads according to
// responses. Returns (replied, resolved, skipped_unresolved).
func replyAndResolve(ctx context.Context, responses []ThreadResponse, resolveSkipped bool, log func(string, ...interface{})) (int, int, int) {
	replied, resolved, skippedUnresolved := 0, 0, 0
	for _, r := range responses {
		if r.Comment != "" && r.ThreadID != "" {
			if rerr := replyToThreadFn(ctx, r.ThreadID, r.Comment); rerr != nil {
				log("reply thread %s: %v", r.ThreadID, rerr)
			} else {
				replied++
			}
		}
		switch r.Action {
		case "fixed", "already_fixed":
			if rerr := resolveThreadFn(ctx, r.ThreadID); rerr != nil {
				log("resolve thread %s: %v", r.ThreadID, rerr)
			} else {
				resolved++
			}
		case "skipped":
			if resolveSkipped || r.ResolveWhenSkipped {
				if rerr := resolveThreadFn(ctx, r.ThreadID); rerr != nil {
					log("resolve thread %s: %v", r.ThreadID, rerr)
				} else {
					resolved++
				}
			} else {
				skippedUnresolved++
			}
		}
	}
	return replied, resolved, skippedUnresolved
}

// ── Case collision + conflict marker handlers ───────────────────────────────

func handleCaseCollisions(ctx context.Context, opts Options, wtPath, branch string) error {
	groups, err := detectCaseCollisionsFn(wtPath)
	if err != nil || len(groups) == 0 {
		return err
	}
	if opts.DryRun {
		return fmt.Errorf("dry-run: detected %d case-collision group(s)", len(groups))
	}
	// Build prompt and run model.
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are fixing case-colliding tracked paths on a Git branch.\n\n"+
		"Repo: %s\nPR: #%d\nBranch: %s\nWorktree: %s\n\n"+
		"Collision groups:\n", opts.Repo, opts.PRNum, branch, wtPath)
	for _, g := range groups {
		sb.WriteString("- ")
		sb.WriteString(strings.Join(g, " | "))
		sb.WriteString("\n")
	}
	sb.WriteString(`
Required approach:
1. Inspect conflicting paths: git ls-tree -r --name-only HEAD; git show HEAD:<path>
2. Decide canonical casing per group following repo conventions.
3. Merge content if both variants are useful.
4. On case-insensitive FS, normalize in TWO commits: first remove losing case, then restore canonical.
5. Use temporary filenames for casing-only renames if needed.
6. Do NOT create thread-responses.json.
7. End with a clean git status.
`)
	if err := runPlainFn(ctx, opts.AIBackend, opts.FixModel, sb.String(), wtPath); err != nil {
		return err
	}
	// Verify it's actually clean.
	if remaining, _ := detectCaseCollisionsFn(wtPath); len(remaining) > 0 {
		return fmt.Errorf("remaining case collisions after LLM: %d group(s)", len(remaining))
	}
	return nil
}

func fixConflictMarkers(ctx context.Context, opts Options, wtPath string, log func(string, ...interface{})) error {
	files, err := detectMarkersFn(wtPath)
	if err != nil || len(files) == 0 {
		return err
	}
	log("conflict-markers: %d file(s) with markers", len(files))
	if opts.DryRun {
		return nil
	}

	// Before burning tokens on the fix-model, resolve any conflicts whose
	// outcome is deterministic — lockfiles (→ theirs), changelogs/CI
	// config/artifacts (→ ours). On a pure lockfile conflict the LLM is
	// never called, saving both time and tokens. Mirrors bash
	// merge_base_branch's auto-resolve block.
	ar := autoResolveFn(ctx, wtPath)
	result, arErr := ar.Apply()
	if arErr != nil {
		log("auto-resolve: %v (continuing to LLM)", arErr)
	}
	for p, side := range result.Resolved {
		log("auto-resolve: %s (--%s)", p, string(side))
	}
	autoResolvedEverything := arErr == nil && len(result.Resolved) > 0 && len(result.Remaining) == 0
	if autoResolvedEverything {
		if cerr := ar.CommitAndPush(); cerr != nil {
			log("auto-resolve: commit/push failed: %v (falling through to LLM)", cerr)
			autoResolvedEverything = false
		} else {
			log("auto-resolve: resolved %d file(s), committed and pushed", len(result.Resolved))
			return nil
		}
	}

	// Auto-resolve handled some but not all (or failed). Narrow the LLM
	// prompt to the files it didn't handle so we don't re-process
	// already-handled lockfiles.
	llmTargets := files
	if len(result.Remaining) > 0 {
		llmTargets = result.Remaining
	}
	log("fix-model: %d file(s) need LLM resolution (%v)", len(llmTargets), llmTargets)
	if err := runPlainFn(ctx, opts.AIBackend, opts.FixModel, conflict.BuildFixPrompt(llmTargets), wtPath); err != nil {
		return err
	}
	remaining, _ := detectMarkersFn(wtPath)
	if len(remaining) > 0 {
		return fmt.Errorf("markers remain in %v after LLM run", remaining)
	}
	return nil
}

// ── Post-fix review cycle ───────────────────────────────────────────────────

func postFixReviewCycle(ctx context.Context, opts Options, wtPath string, fixedCount int, log func(string, ...interface{})) {
	// A negative value is a bug — fall back to the bash default. Explicit
	// zero ("no wait") is a valid override and is honored here.
	wait := opts.ReviewWaitSecs
	if wait < 0 {
		wait = 90
	}
	if wait > 0 {
		log("post-fix: waiting %ds...", wait)
		sleepFn(time.Duration(wait) * time.Second)
	}

	newThreads, err := fetchThreadsFn(ctx, opts.Repo, opts.PRNum, opts.MaxThreads)
	if err != nil {
		log("post-fix: fetch threads failed: %v", err)
		return
	}
	if len(newThreads) == 0 {
		log("post-fix: no new comments")
		body := fmt.Sprintf(
			"gh crfix: All %d comments addressed. No new issues after re-review.",
			fixedCount)
		_ = postCommentFn(ctx, opts.Repo, opts.PRNum, body)
		// Merge + re-fix conflict markers one more time; swallow all errors.
		if pr, err := fetchPRFn(ctx, opts.Repo, opts.PRNum); err == nil && pr.BaseRefName != "" {
			if merr := mergeBaseFn(ctx, wtPath, pr.BaseRefName); merr != nil {
				_ = postCommentFn(ctx, opts.Repo, opts.PRNum,
					"gh crfix: WARNING — could not merge base branch; conflicts need manual resolution.")
			}
		}
		_ = fixConflictMarkers(ctx, opts, wtPath, log)
		_ = requestCopilotReviewFn(ctx, opts.Repo, opts.PRNum)
		log("post-fix: done")
		return
	}
	log("post-fix: %d new comment(s)", len(newThreads))
	body := fmt.Sprintf(
		"gh crfix: Fixed %d comments, but %d new issue(s) raised. Run again to address.",
		fixedCount, len(newThreads))
	_ = postCommentFn(ctx, opts.Repo, opts.PRNum, body)
}

// ── Prompt builders ─────────────────────────────────────────────────────────

func toTriageThread(rt ghapi.Thread) triage.Thread {
	t := triage.Thread{
		ID:         rt.ID,
		IsResolved: rt.IsResolved,
		IsOutdated: rt.IsOutdated,
		Path:       rt.Path,
		Line:       rt.Line,
	}
	for _, c := range rt.Comments {
		t.Comments = append(t.Comments, triage.Comment{
			ID:           c.ID,
			Body:         c.Body,
			Path:         c.Path,
			Line:         c.Line,
			OriginalLine: c.OriginalLine,
			Author:       c.Author,
			CreatedAt:    c.CreatedAt,
		})
	}
	return t
}

func buildGatePrompt(
	threads []ghapi.Thread,
	classes []triage.Classification,
	vr validate.Result,
	ci []ghapi.CICheck,
	gctx gate.GateContext,
) string {
	var sb strings.Builder

	sb.WriteString("## Gate Decision Request\n\n")
	sb.WriteString("You are a senior code reviewer acting as a triage gate.\n")
	sb.WriteString("Decide whether the residual review threads require an advanced model to fix.\n\n")

	sb.WriteString("### Gate Score\n")
	fmt.Fprintf(&sb, "- total_score: %.3f (threshold: %.1f)\n", gctx.TotalScore, gctx.Threshold)
	fmt.Fprintf(&sb, "- needs_llm threads: %d\n", gctx.Components.NeedsLLM.Count)
	fmt.Fprintf(&sb, "- pr_comment threads: %d\n", gctx.Components.PRComment.Count)
	fmt.Fprintf(&sb, "- tests_failed: %v\n\n", gctx.Components.TestFailure.Failed)

	if vr.Ran && !vr.Success {
		sb.WriteString("### Validation Failure\n")
		sb.WriteString("```\n")
		sb.WriteString(vr.Summary)
		sb.WriteString("\n```\n\n")
	}

	if len(ci) > 0 {
		sb.WriteString("### Failing CI Checks\n")
		for _, c := range ci {
			fmt.Fprintf(&sb, "**%s**\n```\n%s\n```\n\n", c.Name, truncate(c.LogText, 500))
		}
	}

	sb.WriteString("### Residual Review Threads\n")
	byID := make(map[string]ghapi.Thread, len(threads))
	for _, t := range threads {
		byID[t.ID] = t
	}
	for _, c := range classes {
		t := byID[c.ThreadID]
		fmt.Fprintf(&sb, "\n**Thread %s** — `%s:%d` — reason: %s\n", c.ThreadID, c.Path, c.Line, c.Reason)
		for _, cm := range t.Comments {
			fmt.Fprintf(&sb, "> @%s: %s\n", cm.Author, cm.Body)
		}
	}

	sb.WriteString("\n### Output Instructions\n")
	sb.WriteString("Return a JSON object with:\n")
	sb.WriteString("- `needs_advanced_model` (boolean): set true if ANY thread needs a code change\n")
	sb.WriteString("- `reason` (string): brief explanation\n")
	sb.WriteString("- `threads_to_fix` (array of thread IDs): IDs that require code changes.\n")
	sb.WriteString("  If this array is non-empty you MUST set `needs_advanced_model` to true.\n")

	return sb.String()
}

func buildFixPrompt(
	threads []ghapi.Thread,
	classes []triage.Classification,
	threadIDs []string,
	vr validate.Result,
	ci []ghapi.CICheck,
) string {
	var sb strings.Builder

	sb.WriteString("## PR Review Fix Task\n\n")
	sb.WriteString("You are a senior software engineer. Fix the issues identified in this PR review.\n\n")

	sb.WriteString("### Instructions\n")
	sb.WriteString("1. Read AGENTS.md and CLAUDE.md in this repo if they exist.\n")
	sb.WriteString("2. Fix ALL of the threads listed below AND any CI/test failures.\n")
	sb.WriteString("3. Make minimal, correct changes. Do not refactor unrelated code.\n")
	sb.WriteString("4. After making all changes, create `thread-responses.json` in the repo root:\n")
	sb.WriteString("   ```json\n")
	sb.WriteString("   [\n")
	sb.WriteString("     {\"thread_id\": \"PRRT_xxx\", \"action\": \"fixed\", \"comment\": \"explanation\"}\n")
	sb.WriteString("   ]\n")
	sb.WriteString("   ```\n")
	sb.WriteString("   Valid actions: fixed | skipped | already_fixed\n")
	sb.WriteString("5. Stage, commit, and push all changes.\n\n")

	if len(ci) > 0 {
		sb.WriteString("### Failing CI Checks (must fix)\n")
		for _, c := range ci {
			fmt.Fprintf(&sb, "**%s**\n```\n%s\n```\n\n", c.Name, truncate(c.LogText, 800))
		}
	}

	if vr.Ran && !vr.Success {
		sb.WriteString("### Test Failures (must fix)\n```\n")
		sb.WriteString(vr.Summary)
		sb.WriteString("\n```\n\n")
	}

	fixSet := make(map[string]bool, len(threadIDs))
	for _, id := range threadIDs {
		fixSet[id] = true
	}

	byID := make(map[string]ghapi.Thread, len(threads))
	for _, t := range threads {
		byID[t.ID] = t
	}

	sb.WriteString("### Threads to Fix\n")
	for _, c := range classes {
		if len(fixSet) > 0 && !fixSet[c.ThreadID] {
			continue
		}
		t := byID[c.ThreadID]
		fmt.Fprintf(&sb, "\n**Thread %s**\n", c.ThreadID)
		fmt.Fprintf(&sb, "- File: `%s` line %d\n", c.Path, c.Line)
		fmt.Fprintf(&sb, "- Reason: %s\n", c.Reason)
		sb.WriteString("- Comments:\n")
		for _, cm := range t.Comments {
			fmt.Fprintf(&sb, "  - @%s (%s): %s\n", cm.Author, cm.CreatedAt, cm.Body)
		}
	}

	return sb.String()
}

func buildSummaryComment(skip, auto, alreadyFixed, needsLLM []triage.Classification, resolved, total int) string {
	var sb strings.Builder
	sb.WriteString("gh crfix — Fix Summary\n\n")
	sb.WriteString("| Stage | Count |\n|-------|-------|\n")
	fmt.Fprintf(&sb, "| Total threads | %d |\n", total)
	fmt.Fprintf(&sb, "| Skipped (deterministic) | %d |\n", len(skip))
	fmt.Fprintf(&sb, "| Already likely fixed | %d |\n", len(alreadyFixed))
	fmt.Fprintf(&sb, "| Auto/mechanical | %d |\n", len(auto))
	fmt.Fprintf(&sb, "| Sent to LLM | %d |\n", len(needsLLM))
	fmt.Fprintf(&sb, "| Resolved | %d |\n", resolved)
	return sb.String()
}

func readThreadResponses(wtPath string) ([]ThreadResponse, error) {
	data, err := os.ReadFile(filepath.Join(wtPath, "thread-responses.json"))
	if err != nil {
		return nil, err
	}
	var responses []ThreadResponse
	if err := json.Unmarshal(data, &responses); err != nil {
		return nil, err
	}
	return responses, nil
}

func hasAutofixHook(wtPath string) bool {
	return detectAutofixHook(wtPath) != ""
}

func detectAutofixHook(wtPath string) string {
	for _, rel := range []string{
		".gh-crfix/autofix.sh",
		"scripts/gh-crfix-autofix.sh",
	} {
		p := filepath.Join(wtPath, rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// runHook runs a shell hook in dir and propagates cmd.Run()'s error so the
// caller can log or surface the failure. ctx is honored so Ctrl+C can stop
// a hanging hook; hook stdout/stderr stream to the process's stdout/stderr.
func runHook(ctx context.Context, hookPath, dir string) error {
	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n...(truncated)"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// prefixWriter wraps w so that every newline-terminated line written to the
// returned writer is prefixed with prefix. This is how validation output gets
// indented under the per-PR log when streamed live. A trailing partial line
// (no newline) is buffered until the next write completes it — safe enough
// since cmd output from bun/npm tests always ends with a final newline.
func prefixWriter(w io.Writer, prefix string) io.Writer {
	return &prefixWriterImpl{w: w, prefix: []byte(prefix), atLineStart: true}
}

type prefixWriterImpl struct {
	w           io.Writer
	prefix      []byte
	atLineStart bool
}

func (p *prefixWriterImpl) Write(data []byte) (int, error) {
	written := 0
	for i, b := range data {
		if p.atLineStart {
			if _, err := p.w.Write(p.prefix); err != nil {
				return written, err
			}
			p.atLineStart = false
		}
		if b == '\n' {
			if _, err := p.w.Write(data[written : i+1]); err != nil {
				return written, err
			}
			written = i + 1
			p.atLineStart = true
		}
	}
	if written < len(data) {
		if _, err := p.w.Write(data[written:]); err != nil {
			return written, err
		}
	}
	return len(data), nil
}
