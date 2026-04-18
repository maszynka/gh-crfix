package workflow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/maszynka/gh-crfix/internal/autoresolve"
	"github.com/maszynka/gh-crfix/internal/triage"
)

// fakeRegen is the test double used by the lockfile_regen tests.
type fakeRegen struct {
	calls     []autoresolve.LockfileKind
	regenErr  error // non-nil → Regenerate returns this
}

func (f *fakeRegen) Regenerate(_ context.Context, kind autoresolve.LockfileKind) error {
	f.calls = append(f.calls, kind)
	return f.regenErr
}

// TestRegenerateLockfileThreads_BunSingleThread: the hot path the user
// flagged. A review thread on `bun.lock` should produce a `fixed` response
// without the lists being handed to the fix model.
func TestRegenerateLockfileThreads_BunSingleThread(t *testing.T) {
	fake := &fakeRegen{}
	lockfileRegeneratorFn = func(string) lockfileRegenerator { return fake }
	t.Cleanup(func() {
		lockfileRegeneratorFn = func(wt string) lockfileRegenerator {
			return autoresolve.NewLockfileRegenerator(wt)
		}
	})

	needsLLM := []triage.Classification{
		{ThreadID: "T1", Path: "bun.lock", Line: 10, Reason: "regenerate"},
	}
	var auto []triage.Classification

	resps, handled := regenerateLockfileThreads(
		context.Background(), Options{DryRun: false},
		t.TempDir(), &needsLLM, &auto, noopLog,
	)

	if handled != 1 {
		t.Fatalf("handled=%d; want 1", handled)
	}
	if len(resps) != 1 {
		t.Fatalf("responses=%d; want 1 (fixed/already_fixed)", len(resps))
	}
	if resps[0].ThreadID != "T1" {
		t.Fatalf("response targets %q; want T1", resps[0].ThreadID)
	}
	if len(fake.calls) != 1 || fake.calls[0] != autoresolve.Bun {
		t.Fatalf("regen calls=%v; want [Bun]", fake.calls)
	}
	if len(needsLLM) != 0 {
		t.Fatalf("needsLLM pool should be emptied; got %v", needsLLM)
	}
}

// TestRegenerateLockfileThreads_MultiplePMsSingleInstallPerKind: two threads
// on the same lockfile → install runs once; two threads across different
// kinds → one install per kind.
func TestRegenerateLockfileThreads_MultiplePMsSingleInstallPerKind(t *testing.T) {
	fake := &fakeRegen{}
	lockfileRegeneratorFn = func(string) lockfileRegenerator { return fake }
	t.Cleanup(func() {
		lockfileRegeneratorFn = func(wt string) lockfileRegenerator {
			return autoresolve.NewLockfileRegenerator(wt)
		}
	})

	needsLLM := []triage.Classification{
		{ThreadID: "T1", Path: "bun.lock"},
		{ThreadID: "T2", Path: "apps/x/bun.lock"},      // same kind, different path
		{ThreadID: "T3", Path: "services/y/pnpm-lock.yaml"},
	}
	var auto []triage.Classification

	resps, handled := regenerateLockfileThreads(
		context.Background(), Options{DryRun: false},
		t.TempDir(), &needsLLM, &auto, noopLog,
	)

	if handled != 3 {
		t.Fatalf("handled=%d; want 3", handled)
	}
	if len(resps) != 3 {
		t.Fatalf("responses=%d; want 3", len(resps))
	}
	// 2 distinct kinds → exactly 2 Regenerate() calls.
	if len(fake.calls) != 2 {
		t.Fatalf("regen calls=%d; want 2 (one per kind)", len(fake.calls))
	}
	seen := map[autoresolve.LockfileKind]bool{}
	for _, k := range fake.calls {
		seen[k] = true
	}
	if !seen[autoresolve.Bun] || !seen[autoresolve.Pnpm] {
		t.Fatalf("expected bun + pnpm in calls; got %v", fake.calls)
	}
}

// TestRegenerateLockfileThreads_DoesNotTouchNonLockfileThreads: a mixed pool
// with a source-file thread and a lockfile thread leaves the source thread
// in needsLLM for the gate/fix pipeline.
func TestRegenerateLockfileThreads_DoesNotTouchNonLockfileThreads(t *testing.T) {
	fake := &fakeRegen{}
	lockfileRegeneratorFn = func(string) lockfileRegenerator { return fake }
	t.Cleanup(func() {
		lockfileRegeneratorFn = func(wt string) lockfileRegenerator {
			return autoresolve.NewLockfileRegenerator(wt)
		}
	})

	needsLLM := []triage.Classification{
		{ThreadID: "T_lock", Path: "bun.lock"},
		{ThreadID: "T_code", Path: "src/main.go"},
	}
	var auto []triage.Classification

	resps, handled := regenerateLockfileThreads(
		context.Background(), Options{DryRun: false},
		t.TempDir(), &needsLLM, &auto, noopLog,
	)

	if handled != 1 {
		t.Fatalf("handled=%d; want 1 (only lockfile)", handled)
	}
	if len(resps) != 1 {
		t.Fatalf("responses=%d; want 1", len(resps))
	}
	if len(needsLLM) != 1 || needsLLM[0].ThreadID != "T_code" {
		t.Fatalf("needsLLM should retain only T_code; got %v", needsLLM)
	}
}

// TestRegenerateLockfileThreads_PMMissingKeepsThreadInLLMPool: when the
// package manager isn't on PATH, leave the thread for the fix model —
// don't fake-resolve it.
func TestRegenerateLockfileThreads_PMMissingKeepsThreadInLLMPool(t *testing.T) {
	fake := &fakeRegen{regenErr: autoresolve.ErrPMMissing}
	lockfileRegeneratorFn = func(string) lockfileRegenerator { return fake }
	t.Cleanup(func() {
		lockfileRegeneratorFn = func(wt string) lockfileRegenerator {
			return autoresolve.NewLockfileRegenerator(wt)
		}
	})

	needsLLM := []triage.Classification{
		{ThreadID: "T1", Path: "yarn.lock"},
	}
	var auto []triage.Classification

	resps, handled := regenerateLockfileThreads(
		context.Background(), Options{DryRun: false},
		t.TempDir(), &needsLLM, &auto, noopLog,
	)

	if handled != 0 {
		t.Fatalf("handled=%d; want 0 (PM missing → fallthrough)", handled)
	}
	if len(resps) != 0 {
		t.Fatalf("responses=%d; want 0", len(resps))
	}
	if len(needsLLM) != 1 || needsLLM[0].ThreadID != "T1" {
		t.Fatalf("thread should remain in needsLLM for fallthrough; got %v", needsLLM)
	}
}

// TestRegenerateLockfileThreads_GenericInstallFailureFallsThrough: unlike
// ErrPMMissing the thread is sent to the LLM so *something* tries to
// resolve it. We don't want a transient npm registry blip to silently
// skip the review thread.
func TestRegenerateLockfileThreads_GenericInstallFailureFallsThrough(t *testing.T) {
	fake := &fakeRegen{regenErr: errors.New("npm ERR! 503")}
	lockfileRegeneratorFn = func(string) lockfileRegenerator { return fake }
	t.Cleanup(func() {
		lockfileRegeneratorFn = func(wt string) lockfileRegenerator {
			return autoresolve.NewLockfileRegenerator(wt)
		}
	})

	needsLLM := []triage.Classification{
		{ThreadID: "T1", Path: "package-lock.json"},
	}
	var auto []triage.Classification

	_, handled := regenerateLockfileThreads(
		context.Background(), Options{DryRun: false},
		t.TempDir(), &needsLLM, &auto, noopLog,
	)
	if handled != 0 {
		t.Fatalf("handled=%d; want 0 on install failure", handled)
	}
	if len(needsLLM) != 1 {
		t.Fatalf("needsLLM should still contain the thread; got %v", needsLLM)
	}
}

// TestRegenerateLockfileThreads_NoLockfileThreadsIsNoOp: a pool without any
// lockfile-pointed threads should produce zero calls and zero responses.
func TestRegenerateLockfileThreads_NoLockfileThreadsIsNoOp(t *testing.T) {
	var fakeCalls int32
	lockfileRegeneratorFn = func(string) lockfileRegenerator {
		atomic.AddInt32(&fakeCalls, 1)
		return &fakeRegen{}
	}
	t.Cleanup(func() {
		lockfileRegeneratorFn = func(wt string) lockfileRegenerator {
			return autoresolve.NewLockfileRegenerator(wt)
		}
	})

	needsLLM := []triage.Classification{
		{ThreadID: "T1", Path: "src/a.go"},
		{ThreadID: "T2", Path: "README.md"},
	}
	var auto []triage.Classification

	resps, handled := regenerateLockfileThreads(
		context.Background(), Options{DryRun: false},
		t.TempDir(), &needsLLM, &auto, noopLog,
	)
	if handled != 0 {
		t.Fatalf("handled=%d; want 0", handled)
	}
	if len(resps) != 0 {
		t.Fatalf("responses=%d; want 0", len(resps))
	}
	if atomic.LoadInt32(&fakeCalls) != 0 {
		t.Fatalf("lockfileRegeneratorFn should not be constructed when there are no lockfile threads; calls=%d", fakeCalls)
	}
}
