package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/gate"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
)

// --- Context propagation into ProcessPR ------------------------------------

// TestProcessPR_AcceptsContext asserts that ProcessPR's signature threads a
// context.Context through so callers can cancel mid-run. The test just makes
// sure cancellation reaches the fix-model seam with ctx.Err() != nil.
func TestProcessPR_AcceptsContext(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(_ context.Context, _ string, _ string, _ int) (string, error) {
		return wt, nil
	}

	fetchThreadsFn = func(_ context.Context, _ string, _ int, _ int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before running gate so runGate sees ctx.Err() != nil.
	cancel()

	var gateCtxErr atomic.Value
	runGateFn = func(ctx context.Context, _ ai.Backend, _, _ string, _ map[string]interface{}) (ai.GateOutput, error) {
		gateCtxErr.Store(ctx.Err() != nil)
		return ai.GateOutput{}, nil
	}

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}
	_ = ProcessPR(ctx, opts)

	// If gate ran, its ctx should have been the cancelled one.
	if v, ok := gateCtxErr.Load().(bool); ok && !v {
		t.Fatalf("runGateFn received a live ctx; expected cancelled ctx to be propagated")
	}
}

// TestProcessPR_CancellationAbortsBeforeFix asserts ctx cancellation short-
// circuits ProcessPR: a cancelled ctx yields a failed/cancelled Result
// without invoking runFix.
func TestProcessPR_CancellationAbortsBeforeFix(t *testing.T) {
	installSeams(t)

	fetchPRFn = func(ctx context.Context, _ string, _ int) (ghapi.PRInfo, error) {
		// Respect ctx: if caller already cancelled, return ctx.Err().
		if err := ctx.Err(); err != nil {
			return ghapi.PRInfo{}, err
		}
		return ghapi.PRInfo{State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "t"}, nil
	}

	var fixCalled int32
	runFixFn = func(_ context.Context, _ ai.Backend, _, _, _ string) error {
		atomic.AddInt32(&fixCalled, 1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := ProcessPR(ctx, branchBaseOpts(t))
	if res.Status == "ok" {
		t.Fatalf("want non-ok when ctx cancelled before start; got %+v", res)
	}
	if atomic.LoadInt32(&fixCalled) != 0 {
		t.Fatalf("runFix must not be called when ctx cancelled; got %d", fixCalled)
	}
}

// --- ai exec timeouts ------------------------------------------------------

// TestRunGate_ContextDeadline asserts that ai.RunGate honors a ctx deadline
// and returns an error that wraps context.DeadlineExceeded.
func TestRunGate_ContextDeadline(t *testing.T) {
	// Use a real fake claude script that sleeps forever; ctx deadline should
	// kill it. This test lives in the ai package test — but we assert the
	// observable behavior here indirectly by asserting runGateFn receives ctx.
	// The ai-package test TestRunGate_DeadlineExceeded covers real cancellation.

	// Set up a very short ctx deadline; runGateFn should receive it and we
	// assert on propagation only — the ai package tests validate actual
	// timeout behavior.
	installSeams(t)
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(_ context.Context, _ string, _ string, _ int) (string, error) {
		return wt, nil
	}
	fetchThreadsFn = func(_ context.Context, _ string, _, _ int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}

	var seenDeadline atomic.Value
	runGateFn = func(ctx context.Context, _ ai.Backend, _, _ string, _ map[string]interface{}) (ai.GateOutput, error) {
		_, has := ctx.Deadline()
		seenDeadline.Store(has)
		return ai.GateOutput{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}
	_ = ProcessPR(ctx, opts)

	if v, ok := seenDeadline.Load().(bool); !ok || !v {
		t.Fatalf("runGate ctx should have a deadline propagated from ProcessPR's ctx")
	}
}

// --- Gate-failure handling --------------------------------------------------

// TestProcessPR_GateErrorDoesNotMarkThreadsAlreadyFixed asserts that when the
// gate model fails (returns err), ProcessPR must NOT fall through to the
// "already_fixed" path that resolves all needs_llm threads. Threads should
// either get a "failed" reply or stay unresolved — but must not be silently
// marked already_fixed.
func TestProcessPR_GateErrorDoesNotMarkThreadsAlreadyFixed(t *testing.T) {
	installSeams(t)

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	setupWorktreeFn = func(_ context.Context, _, _ string, _ int) (string, error) { return wt, nil }

	fetchThreadsFn = func(_ context.Context, _ string, _, _ int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{
			ID: "t1", Path: "a.go", Line: 1,
			Comments: []ghapi.Comment{{Author: "alice", Body: "needs semantic review"}},
		}}, nil
	}
	// Gate errors out — simulate crash.
	runGateFn = func(context.Context, ai.Backend, string, string, map[string]interface{}) (ai.GateOutput, error) {
		return ai.GateOutput{}, errors.New("gate boom")
	}
	var fixCalled int32
	runFixFn = func(context.Context, ai.Backend, string, string, string) error {
		atomic.AddInt32(&fixCalled, 1)
		return nil
	}

	var replies []string
	var resolves int32
	replyToThreadFn = func(_ context.Context, _, body string) error {
		replies = append(replies, body)
		return nil
	}
	resolveThreadFn = func(_ context.Context, _ string) error {
		atomic.AddInt32(&resolves, 1)
		return nil
	}

	opts := branchBaseOpts(t)
	opts.Weights = gate.ScoreWeights{NeedsLLM: 1.0}
	_ = ProcessPR(ctxBG(), opts)

	// Thread must NOT have been resolved with an "already_fixed"/"no code change needed" body.
	for _, body := range replies {
		if strings.Contains(body, "no code change needed") ||
			strings.Contains(body, "Likely already addressed") {
			t.Fatalf("gate-error path must not emit already_fixed reply; got %q", body)
		}
	}
	// Must not have been resolved silently (at most zero resolves from this path,
	// any resolves came from deterministic skips — here the only thread is needs_llm).
	if atomic.LoadInt32(&resolves) != 0 {
		t.Fatalf("needs_llm threads must not be auto-resolved on gate error; got %d resolves", resolves)
	}
	if atomic.LoadInt32(&fixCalled) != 0 {
		t.Fatalf("fix model must not run after gate error; got %d", fixCalled)
	}
}

// --- Wrong-dir UX -----------------------------------------------------------

// TestSetupOnePR_RepoRootErrorHasActionableReason asserts that when
// wt.RepoRoot(".") fails, the reason includes guidance to cd or set
// GH_CRFIX_DIR rather than the generic "worktree setup failed".
func TestSetupOnePR_RepoRootErrorHasActionableReason(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetupWithRepoRootErr{repoRootErr: errors.New("not a git repo")}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{{ID: "t1"}}}

	opts := baseOpts()
	opts.RepoRoot = "" // force RepoRoot lookup
	got := setupOnePR(opts, prf, wt, tf, nil, nil)
	if got.Status != "failed" {
		t.Fatalf("want failed; got %+v", got)
	}
	// Actionable hint.
	if !strings.Contains(got.Reason, "GH_CRFIX_DIR") {
		t.Fatalf("reason should mention GH_CRFIX_DIR; got %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "git") {
		t.Fatalf("reason should mention git repository; got %q", got.Reason)
	}
}

// fakeWorktreeSetupWithRepoRootErr lets RepoRoot fail (fakeWorktreeSetup
// always returns "/repo", nil).
type fakeWorktreeSetupWithRepoRootErr struct {
	repoRootErr error
}

func (w *fakeWorktreeSetupWithRepoRootErr) Setup(_, _ string, _ int) (string, error) {
	return "/wt", nil
}
func (w *fakeWorktreeSetupWithRepoRootErr) DirtyStatus(_ string) (string, error)          { return "", nil }
func (w *fakeWorktreeSetupWithRepoRootErr) DetectCaseCollisions(_ string) ([][]string, error) {
	return nil, nil
}
func (w *fakeWorktreeSetupWithRepoRootErr) DetectMarkers(_ string) ([]string, error) {
	return nil, nil
}
func (w *fakeWorktreeSetupWithRepoRootErr) RepoRoot(_ string) (string, error) {
	return "", w.repoRootErr
}

// ctxBG is a tiny helper so the test file reads cleanly.
func ctxBG() context.Context { return context.Background() }
