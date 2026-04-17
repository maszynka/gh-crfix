package workflow

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/gate"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/progress"
)

// seamBackup captures the current values of every package-level seam so a
// test's t.Cleanup can restore them. Each branch test calls installSeams(t)
// which both resets defaults and registers the restore.
type seamBackup struct {
	fetchPR              func(string, int) (ghapi.PRInfo, error)
	fetchThreads         func(string, int, int) ([]ghapi.Thread, error)
	postComment          func(string, int, string) error
	replyToThread        func(string, string) error
	resolveThread        func(string) error
	fetchFailingChecks   func(string, string) ([]ghapi.CICheck, error)
	requestCopilotReview func(string, int) error
	repoRoot             func(string) (string, error)
	setupWorktree        func(string, string, int) (string, error)
	dirtyStatus          func(string) (string, error)
	mergeBase            func(string, string) error
	detectCaseCollisions func(string) ([][]string, error)
	detectMarkers        func(string) ([]string, error)
	runGate              func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error)
	runFix               func(ai.Backend, string, string, string) error
	runPlain             func(ai.Backend, string, string, string) error
	sleep                func(time.Duration)
}

func snapshotSeams() seamBackup {
	return seamBackup{
		fetchPR:              fetchPRFn,
		fetchThreads:         fetchThreadsFn,
		postComment:          postCommentFn,
		replyToThread:        replyToThreadFn,
		resolveThread:        resolveThreadFn,
		fetchFailingChecks:   fetchFailingChecksFn,
		requestCopilotReview: requestCopilotReviewFn,
		repoRoot:             repoRootFn,
		setupWorktree:        setupWorktreeFn,
		dirtyStatus:          dirtyStatusFn,
		mergeBase:            mergeBaseFn,
		detectCaseCollisions: detectCaseCollisionsFn,
		detectMarkers:        detectMarkersFn,
		runGate:              runGateFn,
		runFix:               runFixFn,
		runPlain:             runPlainFn,
		sleep:                sleepFn,
	}
}

func restoreSeams(b seamBackup) {
	fetchPRFn = b.fetchPR
	fetchThreadsFn = b.fetchThreads
	postCommentFn = b.postComment
	replyToThreadFn = b.replyToThread
	resolveThreadFn = b.resolveThread
	fetchFailingChecksFn = b.fetchFailingChecks
	requestCopilotReviewFn = b.requestCopilotReview
	repoRootFn = b.repoRoot
	setupWorktreeFn = b.setupWorktree
	dirtyStatusFn = b.dirtyStatus
	mergeBaseFn = b.mergeBase
	detectCaseCollisionsFn = b.detectCaseCollisions
	detectMarkersFn = b.detectMarkers
	runGateFn = b.runGate
	runFixFn = b.runFix
	runPlainFn = b.runPlain
	sleepFn = b.sleep
}

// installSeams replaces every seam with safe defaults for branch tests
// (empty/no-op/nil-returning) and restores them at test end. Callers
// override specific seams after this returns to drive a specific branch.
func installSeams(t *testing.T) {
	t.Helper()
	prev := snapshotSeams()
	t.Cleanup(func() { restoreSeams(prev) })

	fetchPRFn = func(string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "t", HeadSHA: ""}, nil
	}
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) { return nil, nil }
	postCommentFn = func(string, int, string) error { return nil }
	replyToThreadFn = func(string, string) error { return nil }
	resolveThreadFn = func(string) error { return nil }
	fetchFailingChecksFn = func(string, string) ([]ghapi.CICheck, error) { return nil, nil }
	requestCopilotReviewFn = func(string, int) error { return nil }
	repoRootFn = func(string) (string, error) { return "/repo", nil }
	setupWorktreeFn = func(string, string, int) (string, error) { return t.TempDir(), nil }
	dirtyStatusFn = func(string) (string, error) { return "", nil }
	mergeBaseFn = func(string, string) error { return nil }
	detectCaseCollisionsFn = func(string) ([][]string, error) { return nil, nil }
	detectMarkersFn = func(string) ([]string, error) { return nil, nil }
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		return ai.GateOutput{}, nil
	}
	runFixFn = func(ai.Backend, string, string, string) error { return nil }
	runPlainFn = func(ai.Backend, string, string, string) error { return nil }
	sleepFn = func(time.Duration) {}
}

// branchBaseOpts is a minimal Options that keeps ProcessPR out of trouble:
// real filesystem temp path, NoPostFix set so there's no 3-minute sleep,
// NoAutofix so we don't need a hook, NoResolve/DryRun off by default.
func branchBaseOpts(t *testing.T) Options {
	t.Helper()
	return Options{
		Repo:       "owner/repo",
		PRNum:      42,
		RepoRoot:   t.TempDir(),
		AIBackend:  ai.BackendClaude,
		GateModel:  "gate-model",
		FixModel:   "fix-model",
		MaxThreads: 100,
		NoPostFix:  true,
		NoAutofix:  true,
		Weights: gate.ScoreWeights{
			NeedsLLM:    0.5,
			PRComment:   0.5,
			TestFailure: 1.0,
		},
	}
}

// --- 1. PR not OPEN -----------------------------------------------------------

func TestProcessPR_PRClosedSkipped(t *testing.T) {
	installSeams(t)
	fetchPRFn = func(string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{State: "CLOSED", HeadRefName: "feature", Title: "t"}, nil
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "skipped" {
		t.Fatalf("status=%q want skipped", res.Status)
	}
	if res.Reason != "PR is CLOSED" {
		t.Fatalf("reason=%q want %q", res.Reason, "PR is CLOSED")
	}
}

// --- 2. FetchPR error ---------------------------------------------------------

func TestProcessPR_FetchPRError(t *testing.T) {
	installSeams(t)
	fetchPRFn = func(string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{}, errors.New("gh not found")
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "failed" {
		t.Fatalf("status=%q want failed", res.Status)
	}
	if res.Err == nil {
		t.Fatalf("expected Err to be set")
	}
}

// --- 3. Worktree clean, no threads -------------------------------------------

func TestProcessPR_NoThreadsSkipped(t *testing.T) {
	installSeams(t)
	tracker := progress.NewTracker(t.TempDir())
	opts := branchBaseOpts(t)
	opts.Tracker = tracker
	if err := tracker.Init(opts.PRNum); err != nil {
		t.Fatalf("tracker init: %v", err)
	}

	// Threads already default to nil via installSeams.
	res := ProcessPR(opts)
	if res.Status != "skipped" || res.Reason != "no unresolved threads" {
		t.Fatalf("want skipped/no unresolved threads, got %+v", res)
	}

	// Tracker should show StepSetup=Done and StepFetchThreads=Done.
	if st, _, ok := tracker.Get(opts.PRNum, progress.StepSetup); !ok || st != progress.Done {
		t.Fatalf("StepSetup want done; got %v (ok=%v)", st, ok)
	}
	if st, _, ok := tracker.Get(opts.PRNum, progress.StepFetchThreads); !ok || st != progress.Done {
		t.Fatalf("StepFetchThreads want done; got %v (ok=%v)", st, ok)
	}
}

// --- 4. SetupOnly + threads → skipped "setup-only" ---------------------------

func TestProcessPR_SetupOnlySkipped(t *testing.T) {
	installSeams(t)
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{ID: "t1", Path: "a.go", Line: 1, Comments: []ghapi.Comment{
			{Author: "alice", Body: "please fix"},
		}}}, nil
	}
	opts := branchBaseOpts(t)
	opts.SetupOnly = true

	res := ProcessPR(opts)
	if res.Status != "skipped" || res.Reason != "setup-only" {
		t.Fatalf("want skipped/setup-only; got %+v", res)
	}
}

// --- 5. Gate score below threshold, skipped ----------------------------------
//
// With a single needs_llm thread and weights (NeedsLLM=0.5) the total is 0.5 < 1,
// so ShouldRunGate=false. Code path emits "Skipped automatically: score below
// threshold" comments for each active needs_llm thread.

func TestProcessPR_GateBelowThresholdSkipped(t *testing.T) {
	installSeams(t)

	// 1 thread that classifies as needs_llm (file path that exists, no mechanical/non-actionable body).
	wt := t.TempDir()
	// Create the file so the triage step passes the existence check.
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "thoughtful review needing semantic thought"}},
		}}, nil
	}

	// Track reply calls + their bodies so we can assert the skipped-by-score message.
	var mu sync.Mutex
	var replyBodies []string
	replyToThreadFn = func(id, body string) error {
		mu.Lock()
		defer mu.Unlock()
		replyBodies = append(replyBodies, body)
		return nil
	}
	// gate below threshold means RunGate should NOT be called — fail if it is.
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		t.Fatalf("runGate must not be called when score is below threshold")
		return ai.GateOutput{}, nil
	}

	opts := branchBaseOpts(t)
	// Make sure total score is < 1: only NeedsLLM=0.5, PRComment=0 (no empty path), TestFailure=0.
	opts.Weights = gate.ScoreWeights{NeedsLLM: 0.5, PRComment: 0.5, TestFailure: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q (reason=%q) want ok", res.Status, res.Reason)
	}
	// Find at least one reply with the expected text.
	found := false
	mu.Lock()
	for _, b := range replyBodies {
		if contains(b, "Skipped automatically: score below threshold") {
			found = true
		}
	}
	mu.Unlock()
	if !found {
		t.Fatalf("expected a reply with 'Skipped automatically: score below threshold'; got %v", replyBodies)
	}
}

// --- 6. Gate returns NeedsAdvancedModel=false --------------------------------

func TestProcessPR_GateDeclinesAdvancedModel(t *testing.T) {
	installSeams(t)

	// 3 needs_llm threads → total score 0.5 * 3? No — score weights NeedsLLM only fires once (count>0).
	// With NeedsLLM=1.0 score=1.0, ShouldRunGate=true, runGate called, gate says
	// NeedsAdvancedModel=false.
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review of this"}},
		}}, nil
	}

	gateCalled := int32(0)
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		atomic.AddInt32(&gateCalled, 1)
		return ai.GateOutput{NeedsAdvancedModel: false, Reason: "not needed"}, nil
	}
	fixCalled := int32(0)
	runFixFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&fixCalled, 1)
		return nil
	}

	var mu sync.Mutex
	var replyBodies []string
	replyToThreadFn = func(_, body string) error {
		mu.Lock()
		replyBodies = append(replyBodies, body)
		mu.Unlock()
		return nil
	}

	opts := branchBaseOpts(t)
	// NeedsLLM=1.0 guarantees total score ≥ 1 → gate runs.
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
	if atomic.LoadInt32(&gateCalled) != 1 {
		t.Fatalf("gate should be called exactly once; got %d", gateCalled)
	}
	if atomic.LoadInt32(&fixCalled) != 0 {
		t.Fatalf("fix must NOT be called when gate declines; got %d", fixCalled)
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, b := range replyBodies {
		if contains(b, "Reviewed by automation — no code change needed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reply 'Reviewed by automation — no code change needed'; got %v", replyBodies)
	}
}

// --- 7. DryRun=true: no PostComment, no ReplyToThread, no ResolveThread ------

func TestProcessPR_DryRun(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}

	var postCalled, replyCalled, resolveCalled int32
	postCommentFn = func(string, int, string) error { atomic.AddInt32(&postCalled, 1); return nil }
	replyToThreadFn = func(string, string) error { atomic.AddInt32(&replyCalled, 1); return nil }
	resolveThreadFn = func(string) error { atomic.AddInt32(&resolveCalled, 1); return nil }
	// Gate and fix should not run in dry mode.
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		t.Fatalf("RunGate should not run in dry-run mode")
		return ai.GateOutput{}, nil
	}
	runFixFn = func(ai.Backend, string, string, string) error {
		t.Fatalf("RunFix should not run in dry-run mode")
		return nil
	}
	// Copilot re-review seam may still fire if !opts.DryRun — with DryRun=true it should not.
	requestCopilotReviewFn = func(string, int) error {
		t.Fatalf("RequestCopilotReview should not run in dry-run mode")
		return nil
	}

	opts := branchBaseOpts(t)
	opts.DryRun = true

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
	if atomic.LoadInt32(&postCalled) != 0 {
		t.Fatalf("PostComment must not be called in dry-run; got %d", postCalled)
	}
	if atomic.LoadInt32(&replyCalled) != 0 {
		t.Fatalf("ReplyToThread must not be called in dry-run; got %d", replyCalled)
	}
	if atomic.LoadInt32(&resolveCalled) != 0 {
		t.Fatalf("ResolveThread must not be called in dry-run; got %d", resolveCalled)
	}
	if res.Replied != 0 || res.Resolved != 0 || res.Skipped != 0 {
		t.Fatalf("want all counts 0 in dry-run; got replied=%d resolved=%d skipped=%d",
			res.Replied, res.Resolved, res.Skipped)
	}
}

// --- 8. NoResolve=true: ReplyToThread/ResolveThread NOT called --------------

func TestProcessPR_NoResolve(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}

	var replyCalled, resolveCalled int32
	replyToThreadFn = func(string, string) error { atomic.AddInt32(&replyCalled, 1); return nil }
	resolveThreadFn = func(string) error { atomic.AddInt32(&resolveCalled, 1); return nil }

	opts := branchBaseOpts(t)
	opts.NoResolve = true
	// Weights that keep us below threshold so gate doesn't run — keeps the
	// test focused on the "no reply/resolve even outside dry-run" check.
	opts.Weights = gate.ScoreWeights{NeedsLLM: 0.5, TestFailure: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok", res.Status)
	}
	if atomic.LoadInt32(&replyCalled) != 0 {
		t.Fatalf("ReplyToThread must not be called with NoResolve; got %d", replyCalled)
	}
	if atomic.LoadInt32(&resolveCalled) != 0 {
		t.Fatalf("ResolveThread must not be called with NoResolve; got %d", resolveCalled)
	}
}

// --- 9. All threads classified as skip → deterministic only, gate silent -----

func TestProcessPR_AllSkipped(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	// File exists so triage won't send us to "file no longer exists in worktree"
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	// All threads are "lgtm" (non-actionable → skip, ResolveWhenSkipped=true).
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{
			{ID: "t1", Path: "a.go", Line: 1, Comments: []ghapi.Comment{{Author: "x", Body: "lgtm"}}},
			{ID: "t2", Path: "a.go", Line: 2, Comments: []ghapi.Comment{{Author: "y", Body: "lgtm"}}},
		}, nil
	}

	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		t.Fatalf("gate must not run when all threads are skipped")
		return ai.GateOutput{}, nil
	}
	runFixFn = func(ai.Backend, string, string, string) error {
		t.Fatalf("fix must not run when all threads are skipped")
		return nil
	}

	var replies []string
	replyToThreadFn = func(_, body string) error {
		replies = append(replies, body)
		return nil
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
	// Each deterministic skip reply starts with "Skipped automatically:".
	for _, r := range replies {
		if !contains(r, "Skipped automatically:") {
			t.Fatalf("unexpected non-skip reply: %q", r)
		}
	}
	if len(replies) != 2 {
		t.Fatalf("want 2 replies; got %d: %v", len(replies), replies)
	}
}

// --- 15. replyAndResolve with seam swap --------------------------------------

func TestReplyAndResolve_CountsAndSkipsEmptyBodies(t *testing.T) {
	installSeams(t)

	var (
		repliesMu   sync.Mutex
		replied     []string
		resolves    []string
	)
	replyToThreadFn = func(id, _ string) error {
		repliesMu.Lock()
		replied = append(replied, id)
		repliesMu.Unlock()
		return nil
	}
	resolveThreadFn = func(id string) error {
		repliesMu.Lock()
		resolves = append(resolves, id)
		repliesMu.Unlock()
		return nil
	}

	responses := []ThreadResponse{
		// fixed with comment → reply + resolve
		{ThreadID: "a", Action: "fixed", Comment: "done"},
		// already_fixed with empty comment → no reply, but resolve
		{ThreadID: "b", Action: "already_fixed", Comment: ""},
		// already_fixed with no thread id → no reply (guard), still resolve (id="" passed to resolveThreadFn)
		{ThreadID: "", Action: "already_fixed", Comment: "foo"},
		// skipped without resolveSkipped → no reply (empty comment), not resolved
		{ThreadID: "c", Action: "skipped", Comment: ""},
		// skipped with ResolveWhenSkipped=true → no reply (empty), resolved
		{ThreadID: "d", Action: "skipped", Comment: "", ResolveWhenSkipped: true},
		// skipped with comment → reply, not resolved (resolveSkipped=false & ResolveWhenSkipped=false)
		{ThreadID: "e", Action: "skipped", Comment: "ignore"},
	}

	replies, resolved, skippedUnresolved := replyAndResolve(
		responses,
		false, // resolveSkipped
		func(string, ...interface{}) {},
	)

	if replies != 2 {
		t.Errorf("replied=%d want 2 (a,e)", replies)
	}
	// a (fixed) + b (already_fixed) + "" (already_fixed, empty id still counted) + d (skipped+flag) = 4
	if resolved != 4 {
		t.Errorf("resolved=%d want 4 (a,b,empty,d)", resolved)
	}
	if skippedUnresolved != 2 {
		// c (skipped no flag) + e (skipped no flag) → unresolved
		t.Errorf("skippedUnresolved=%d want 2 (c,e)", skippedUnresolved)
	}

	// Verify replyToThread was only invoked when Comment and ThreadID non-empty.
	for _, id := range replied {
		if id == "" {
			t.Errorf("reply to empty thread id should have been skipped")
		}
	}
}

func TestReplyAndResolve_ResolveSkippedFlag(t *testing.T) {
	installSeams(t)

	var resolved []string
	resolveThreadFn = func(id string) error {
		resolved = append(resolved, id)
		return nil
	}
	replyToThreadFn = func(string, string) error { return nil }

	responses := []ThreadResponse{
		{ThreadID: "a", Action: "skipped", Comment: "c1"},
		{ThreadID: "b", Action: "skipped", Comment: ""},
	}
	_, resolvedCount, skippedUnresolved := replyAndResolve(
		responses,
		true, // resolveSkipped=true → resolve every skipped
		func(string, ...interface{}) {},
	)
	if resolvedCount != 2 {
		t.Errorf("resolvedCount=%d want 2", resolvedCount)
	}
	if skippedUnresolved != 0 {
		t.Errorf("skippedUnresolved=%d want 0", skippedUnresolved)
	}
	if len(resolved) != 2 {
		t.Errorf("resolveThreadFn called %d times; want 2", len(resolved))
	}
}

// --- 15. Fix model writes thread-responses.json → responses rolled in --------

func TestProcessPR_FixModelWritesResponses(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review please"}},
		}}, nil
	}

	// Gate says "run the advanced model".
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		return ai.GateOutput{NeedsAdvancedModel: true, ThreadsToFix: []string{"t1"}}, nil
	}

	fixCalled := int32(0)
	runFixFn = func(_ ai.Backend, _ string, _ string, workdir string) error {
		atomic.AddInt32(&fixCalled, 1)
		body := `[{"thread_id":"t1","action":"fixed","comment":"replaced the bug with a fix"}]`
		return os.WriteFile(filepath.Join(workdir, "thread-responses.json"), []byte(body), 0o644)
	}

	var replyMu sync.Mutex
	var replyBodies []string
	var replyIDs []string
	replyToThreadFn = func(id, body string) error {
		replyMu.Lock()
		replyIDs = append(replyIDs, id)
		replyBodies = append(replyBodies, body)
		replyMu.Unlock()
		return nil
	}
	resolveCount := int32(0)
	resolveThreadFn = func(string) error {
		atomic.AddInt32(&resolveCount, 1)
		return nil
	}

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q (reason=%q) want ok", res.Status, res.Reason)
	}
	if atomic.LoadInt32(&fixCalled) != 1 {
		t.Fatalf("runFix calls=%d want 1", fixCalled)
	}
	if !res.FixModelRan {
		t.Fatalf("want FixModelRan=true")
	}
	if res.Replied == 0 {
		t.Fatalf("want Replied > 0; got %d", res.Replied)
	}
	if res.Resolved == 0 {
		t.Fatalf("want Resolved > 0; got %d", res.Resolved)
	}

	replyMu.Lock()
	defer replyMu.Unlock()
	found := false
	for i, body := range replyBodies {
		if replyIDs[i] == "t1" && strings.Contains(body, "replaced the bug") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected t1 reply with fix-model text; got ids=%v bodies=%v",
			replyIDs, replyBodies)
	}
}

// --- 16. ProcessPR detects autofix hook at .gh-crfix/autofix.sh -------------

func TestProcessPR_AutofixHookDetectedAndRan(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	// Set up an executable autofix hook that writes a marker file we can
	// check for afterwards.
	hookDir := filepath.Join(wt, ".gh-crfix")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir .gh-crfix: %v", err)
	}
	marker := filepath.Join(wt, "autofix-ran.txt")
	hookPath := filepath.Join(hookDir, "autofix.sh")
	hookBody := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}

	opts := branchBaseOpts(t)
	// Use NoAutofix=false so the autofix branch actually runs.
	opts.NoAutofix = false
	// Gate should stay below threshold → keep the test focused on the autofix branch.
	opts.Weights = gate.ScoreWeights{NeedsLLM: 0.5, TestFailure: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q (reason=%q) want ok", res.Status, res.Reason)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("autofix hook marker %q not created: %v", marker, err)
	}
}

// --- 17. Validation failure path — not plumbable without invasive changes.
// SKIPPED: internal/validate uses a detected runner (package.json / hook
// script on disk), not a seam, so we can't inject a "tests failed" result
// without either running real tests or refactoring validate.Detect/Run into
// package-level seams. Leaving this uncovered in workflow tests for now;
// the individual validate.Result=TestsFailed branches are already exercised
// in prompts_test.go through buildGatePrompt/buildFixPrompt.

// --- 18. cleanupThreadResponsesArtifact: no artifact + real git repo ---------

func TestCleanupThreadResponsesArtifact_NoOp(t *testing.T) {
	// Create a real (empty) git repo so `git -C <wt>` calls don't explode
	// with "not a git repository" and we still exercise every branch of the
	// function (git rm fails → fallback rm; commit fails silently; push
	// fails silently).
	wt := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := gitCmdInDir(wt, args...)
		_ = cmd.Run()
	}
	runGit("init", "-q")
	runGit("config", "user.email", "t@example.com")
	runGit("config", "user.name", "test")

	// Should return without panicking even with no artifact present.
	cleanupThreadResponsesArtifact(wt)

	// File should still be absent.
	if _, err := os.Stat(filepath.Join(wt, "thread-responses.json")); !os.IsNotExist(err) {
		t.Fatalf("thread-responses.json should not exist; err=%v", err)
	}
}

// --- 19. ProcessPR: auto-detect repo root when RepoRoot is empty ------------

func TestProcessPR_RepoRootAutoDetectError(t *testing.T) {
	installSeams(t)
	repoRootFn = func(string) (string, error) { return "", errors.New("no git") }

	opts := branchBaseOpts(t)
	opts.RepoRoot = "" // force auto-detect branch.

	res := ProcessPR(opts)
	if res.Status != "failed" {
		t.Fatalf("status=%q want failed", res.Status)
	}
	if res.Err == nil {
		t.Fatalf("want Err set")
	}
	if !strings.Contains(res.Err.Error(), "no git") {
		t.Fatalf("want error to wrap 'no git'; got %v", res.Err)
	}
}

// --- 20. ProcessPR: worktree setup error -----------------------------------

func TestProcessPR_WorktreeSetupError(t *testing.T) {
	installSeams(t)
	setupWorktreeFn = func(string, string, int) (string, error) {
		return "", errors.New("worktree add failed")
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "failed" {
		t.Fatalf("status=%q want failed", res.Status)
	}
	if !strings.Contains(res.Err.Error(), "worktree add failed") {
		t.Fatalf("want error containing 'worktree add failed'; got %v", res.Err)
	}
}

// --- 21. ProcessPR: fetchThreads error -------------------------------------

func TestProcessPR_FetchThreadsError(t *testing.T) {
	installSeams(t)
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return nil, errors.New("gh api threads boom")
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "failed" {
		t.Fatalf("status=%q want failed (reason=%q)", res.Status, res.Reason)
	}
	if !strings.Contains(res.Err.Error(), "gh api threads boom") {
		t.Fatalf("want wrapped fetch error; got %v", res.Err)
	}
}

// --- 22. ProcessPR: mergeBase error is logged but doesn't fail -------------

func TestProcessPR_MergeBaseErrorContinues(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	mergeBaseFn = func(string, string) error { return errors.New("merge conflict") }
	// Keep below threshold so we don't also call gate.
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "x", Body: "lgtm"}},
		}}, nil
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
}

// --- 23. ProcessPR: fixConflictMarkers propagates and aborts ---------------

func TestProcessPR_ConflictMarkersUnresolvable(t *testing.T) {
	installSeams(t)

	// Simulate: detectMarkersFn always reports markers → runPlainFn swallows,
	// markers remain, so fixConflictMarkers returns an error → early exit.
	detectMarkersFn = func(string) ([]string, error) {
		return []string{"bad.go"}, nil
	}
	runPlainFn = func(ai.Backend, string, string, string) error { return nil }

	res := ProcessPR(branchBaseOpts(t))
	if res.Status == "ok" {
		t.Fatalf("want non-ok status when conflict markers persist; got %+v", res)
	}
	if !strings.Contains(res.Reason, "committed conflict markers could not be auto-fixed") {
		t.Fatalf("want conflict-marker error reason; got %q", res.Reason)
	}
}

// --- 24. ProcessPR: runFix returns error → FixModelRan stays false ---------

func TestProcessPR_FixModelError(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "semantic thought needed"}},
		}}, nil
	}
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		// Return empty ThreadsToFix → exercises the `len(selected) == 0 &&
		// len(activeNeedsLLM) > 0` branch where ProcessPR populates from
		// activeNeedsLLM.
		return ai.GateOutput{NeedsAdvancedModel: true, ThreadsToFix: nil}, nil
	}
	fixErr := errors.New("claude exited 1")
	var fixCalled int32
	runFixFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&fixCalled, 1)
		return fixErr
	}

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok even after fix error (reason=%q)", res.Status, res.Reason)
	}
	if res.FixModelRan {
		t.Fatalf("FixModelRan must stay false on fix-model error")
	}
	if atomic.LoadInt32(&fixCalled) != 1 {
		t.Fatalf("fix called %d want 1", fixCalled)
	}
}

// --- 25. ProcessPR: post-fix cycle is triggered when NoPostFix=false -------

func TestProcessPR_PostFixCycleTriggered(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	var fetchCount int32
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		// First fetch = initial threads; second fetch = post-fix cycle.
		n := atomic.AddInt32(&fetchCount, 1)
		if n == 1 {
			return []ghapi.Thread{{
				ID: "t1", Path: "a.go", Line: 1,
				Comments: []ghapi.Comment{{Author: "x", Body: "lgtm"}},
			}}, nil
		}
		return nil, nil
	}

	var sleepCount int32
	sleepFn = func(time.Duration) { atomic.AddInt32(&sleepCount, 1) }

	opts := branchBaseOpts(t)
	opts.NoPostFix = false // enable postFixReviewCycle
	opts.ReviewWaitSecs = 1

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
	if atomic.LoadInt32(&sleepCount) != 1 {
		t.Fatalf("post-fix sleep should have been called once; got %d", sleepCount)
	}
	if atomic.LoadInt32(&fetchCount) < 2 {
		t.Fatalf("fetch should have been called twice (initial + post-fix); got %d", fetchCount)
	}
}

// --- 26. ProcessPR: dirty worktree triggers case collision handler ---------

func TestProcessPR_DirtyWorktreeHandledByCollisions(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	// Dirty first, clean after LLM resolves.
	var dirtyCalls int32
	dirtyStatusFn = func(string) (string, error) {
		n := atomic.AddInt32(&dirtyCalls, 1)
		if n == 1 {
			return " M Foo.go", nil
		}
		return "", nil
	}
	var detectCalls int32
	detectCaseCollisionsFn = func(string) ([][]string, error) {
		n := atomic.AddInt32(&detectCalls, 1)
		if n == 1 {
			return [][]string{{"Foo.go", "foo.go"}}, nil
		}
		return nil, nil
	}
	var plainCalls int32
	runPlainFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&plainCalls, 1)
		return nil
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "skipped" && res.Status != "ok" {
		// With no threads returned the overall result is "skipped (no
		// unresolved threads)". Either way, what we care about is that the
		// collision handler ran.
		t.Fatalf("unexpected status=%q", res.Status)
	}
	if atomic.LoadInt32(&plainCalls) == 0 {
		t.Fatalf("runPlain (case-collision fix) should have been called")
	}
}

// --- 27. ProcessPR: dirty worktree remaining after handler → reason set ----

func TestProcessPR_DirtyWorktreePersistsReason(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	dirtyStatusFn = func(string) (string, error) { return " M x.go", nil }
	// No collisions → handler returns immediately, re-check still dirty.
	detectCaseCollisionsFn = func(string) ([][]string, error) { return nil, nil }

	res := ProcessPR(branchBaseOpts(t))
	if !strings.Contains(res.Reason, "dirty") {
		t.Fatalf("want reason mentioning 'dirty'; got %q", res.Reason)
	}
}

// --- 28. OptionsFromConfig: maps Config fields onto Options ----------------

func TestOptionsFromConfig_MapsFields(t *testing.T) {
	cfg := config.Config{
		AIBackend:        "claude",
		GateModel:        "gate-m",
		FixModel:         "fix-m",
		ScoreNeedsLLM:    0.75,
		ScorePRComment:   0.25,
		ScoreTestFailure: 0.5,
	}
	opts := OptionsFromConfig(cfg, "owner/repo", 99)

	if opts.Repo != "owner/repo" || opts.PRNum != 99 {
		t.Fatalf("Repo/PRNum mismatch: %+v", opts)
	}
	if opts.GateModel != cfg.GateModel {
		t.Fatalf("GateModel=%q want %q", opts.GateModel, cfg.GateModel)
	}
	if opts.FixModel != cfg.FixModel {
		t.Fatalf("FixModel=%q want %q", opts.FixModel, cfg.FixModel)
	}
	if opts.Weights.NeedsLLM != cfg.ScoreNeedsLLM {
		t.Fatalf("weights.NeedsLLM mismatch")
	}
	if opts.ReviewWaitSecs != 180 {
		t.Fatalf("ReviewWaitSecs=%d want 180", opts.ReviewWaitSecs)
	}
	if opts.MaxThreads != 100 {
		t.Fatalf("MaxThreads=%d want 100", opts.MaxThreads)
	}
	if !opts.IncludeOutdated {
		t.Fatalf("IncludeOutdated should default true")
	}
}

// --- 29. readThreadResponses: missing file + malformed JSON error paths ----

func TestReadThreadResponses_ErrorPaths(t *testing.T) {
	wt := t.TempDir()
	// Missing → ReadFile error.
	if _, err := readThreadResponses(wt); err == nil {
		t.Fatalf("want error on missing thread-responses.json")
	}
	// Malformed → unmarshal error.
	if werr := os.WriteFile(filepath.Join(wt, "thread-responses.json"), []byte("not-json"), 0o644); werr != nil {
		t.Fatalf("write bad json: %v", werr)
	}
	if _, err := readThreadResponses(wt); err == nil {
		t.Fatalf("want error on malformed json")
	}
}

// --- 30. ProcessPR: gate model errors → logged, fix not run --------------

func TestProcessPR_GateModelError(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		return ai.GateOutput{}, errors.New("gate crashed")
	}
	var fixCalled int32
	runFixFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&fixCalled, 1)
		return nil
	}

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}
	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (gate error shouldn't fail the run)", res.Status)
	}
	if atomic.LoadInt32(&fixCalled) != 0 {
		t.Fatalf("fix must not run when gate returned an error; got %d", fixCalled)
	}
}

// --- 31. ProcessPR: HeadSHA present → fetchFailingChecks is invoked -------

func TestProcessPR_FetchFailingChecksInvoked(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchPRFn = func(string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{
			State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "t", HeadSHA: "abc123",
		}, nil
	}
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "lgtm"}},
		}}, nil
	}
	var ciCalls int32
	fetchFailingChecksFn = func(_ string, sha string) ([]ghapi.CICheck, error) {
		atomic.AddInt32(&ciCalls, 1)
		if sha != "abc123" {
			t.Errorf("CI called with sha=%q want abc123", sha)
		}
		return []ghapi.CICheck{{Name: "build", LogText: "boom"}}, nil
	}

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
	if atomic.LoadInt32(&ciCalls) != 1 {
		t.Fatalf("fetchFailingChecks calls=%d want 1", ciCalls)
	}
}

// --- 32. ProcessPR: postComment + copilot review errors are swallowed ------

func TestProcessPR_PostCommentAndCopilotErrorsSwallowed(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "lgtm"}},
		}}, nil
	}
	postCommentFn = func(string, int, string) error { return errors.New("post boom") }
	requestCopilotReviewFn = func(string, int) error { return errors.New("copilot boom") }

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (errors should be swallowed)", res.Status)
	}
}

// --- 33. ProcessPR: reply/resolve errors are swallowed --------------------

func TestProcessPR_ReplyResolveErrors(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	// Produce a thread that classifies as already-fixed/skipped.
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "x", Body: "lgtm"}},
		}}, nil
	}
	replyToThreadFn = func(string, string) error { return errors.New("reply boom") }
	resolveThreadFn = func(string) error { return errors.New("resolve boom") }

	res := ProcessPR(branchBaseOpts(t))
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reply/resolve errors should be swallowed)", res.Status)
	}
}

// --- 34. Gate returns contradictory signal (flag=false + ThreadsToFix non-empty) --
//
// Some models respond with `needs_advanced_model=false` + a non-empty
// `threads_to_fix`. Treat this as "fix these threads" — do not drop them on
// the floor as "no code change needed".

func TestProcessPR_GateFlagFalseButThreadsToFixNonEmpty(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}, {
			ID: "t2", Path: "a.go", Line: 2,
			Comments: []ghapi.Comment{{Author: "bob", Body: "also needs review"}},
		}}, nil
	}

	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		// The contradictory shape: flag says "no", but caller still lists 2 IDs.
		return ai.GateOutput{
			NeedsAdvancedModel: false,
			Reason:             "simple fixes",
			ThreadsToFix:       []string{"t1", "t2"},
		}, nil
	}

	fixCalled := int32(0)
	var fixedIDs []string
	runFixFn = func(_ ai.Backend, _ string, prompt, _ string) error {
		atomic.AddInt32(&fixCalled, 1)
		// Record which IDs the prompt targets so the assertion can compare.
		for _, id := range []string{"t1", "t2"} {
			if strings.Contains(prompt, id) {
				fixedIDs = append(fixedIDs, id)
			}
		}
		return nil
	}

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}
	if atomic.LoadInt32(&fixCalled) != 1 {
		t.Fatalf("fix model must run when ThreadsToFix non-empty regardless of flag; got calls=%d", fixCalled)
	}
	if len(fixedIDs) != 2 {
		t.Fatalf("fix prompt should target both thread IDs; got %v", fixedIDs)
	}
	if !res.FixModelRan {
		t.Fatal("result.FixModelRan should be true")
	}
}

// --- 35. Master log captures process-phase entries --------------------------
//
// The per-PR `log()` closure in ProcessPR used to only write to stdout, leaving
// $LOG_DIR/run.log missing the process-phase narrative (the bash `[process-pr]`
// lines that the e2e harness greps for). Assert that when opts.Run is set, the
// master log is populated.

func TestProcessPR_MasterLogCapturesProcessPhase(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(string, string, int) (string, error) { return wt, nil }

	// One thread that flows all the way through.
	fetchThreadsFn = func(string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}
	runGateFn = func(ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		return ai.GateOutput{NeedsAdvancedModel: true, ThreadsToFix: []string{"t1"}}, nil
	}

	// Stand up a real logs.Run in a temp $HOME so the symlink/mkdir succeed.
	t.Setenv("HOME", t.TempDir())
	run, err := newTestRun(t)
	if err != nil {
		t.Fatalf("newTestRun: %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })

	opts := branchBaseOpts(t)
	opts.Run = run
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}

	res := ProcessPR(opts)
	if res.Status != "ok" {
		t.Fatalf("status=%q want ok (reason=%q)", res.Status, res.Reason)
	}

	master, err := os.ReadFile(run.MasterLog())
	if err != nil {
		t.Fatalf("read master log: %v", err)
	}
	body := string(master)
	for _, want := range []string{
		"[process-pr]",
		"fetching PR metadata",
		"fetching review threads",
		"running gate model",
		"running fix model",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("master log missing %q; body=%s", want, body)
		}
	}
}

// --- Helpers ------------------------------------------------------------------

// contains is a tiny wrapper that lets us keep the substring checks readable.
func contains(s, sub string) bool { return strings.Contains(s, sub) }

// gitCmdInDir shells out to git inside a directory. Kept small and
// dependency-free so the cleanup test doesn't need a full helper package.
func gitCmdInDir(dir string, args ...string) *exec.Cmd {
	full := append([]string{"-C", dir}, args...)
	return exec.Command("git", full...)
}

// newTestRun creates a fresh logs.Run rooted at a temp HOME. The caller is
// responsible for closing it via t.Cleanup.
func newTestRun(t *testing.T) (*logs.Run, error) {
	t.Helper()
	return logs.NewRun()
}

