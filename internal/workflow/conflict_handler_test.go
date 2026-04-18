package workflow

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/autoresolve"
)

// --- 10. fixConflictMarkers: no markers → nil, no LLM -----------------------

func TestFixConflictMarkers_Clean(t *testing.T) {
	installSeams(t)

	var plainCalls int32
	detectMarkersFn = func(string) ([]string, error) { return nil, nil }
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error {
		atomic.AddInt32(&plainCalls, 1)
		return nil
	}

	if err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
		t.Fatalf("want nil err on clean tree; got %v", err)
	}
	if atomic.LoadInt32(&plainCalls) != 0 {
		t.Fatalf("runPlain must not be called on clean tree; got %d", plainCalls)
	}
}

// --- 11. fixConflictMarkers: dry-run + markers → nil (swallowed) ------------

func TestFixConflictMarkers_DryRun(t *testing.T) {
	installSeams(t)

	detectMarkersFn = func(string) ([]string, error) { return []string{"a.go"}, nil }
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error {
		t.Fatalf("runPlain must not be called in dry-run")
		return nil
	}

	opts := branchBaseOpts(t)
	opts.DryRun = true
	if err := fixConflictMarkers(context.Background(), opts, t.TempDir(), noopLog); err != nil {
		t.Fatalf("want nil err in dry-run (swallow); got %v", err)
	}
}

// --- 12. fixConflictMarkers: LLM resolves → nil -----------------------------

func TestFixConflictMarkers_LLMResolves(t *testing.T) {
	installSeams(t)

	var detectCalls int32
	detectMarkersFn = func(string) ([]string, error) {
		n := atomic.AddInt32(&detectCalls, 1)
		if n == 1 {
			return []string{"a.go", "b.go"}, nil
		}
		return nil, nil
	}
	var ranLLM int32
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error {
		atomic.AddInt32(&ranLLM, 1)
		return nil
	}

	if err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
		t.Fatalf("want nil; got %v", err)
	}
	if atomic.LoadInt32(&ranLLM) != 1 {
		t.Fatalf("LLM calls=%d want 1", ranLLM)
	}
	if atomic.LoadInt32(&detectCalls) != 2 {
		t.Fatalf("detectMarkers calls=%d want 2", detectCalls)
	}
}

// --- 13. fixConflictMarkers: LLM errors → propagates ------------------------

func TestFixConflictMarkers_LLMFails(t *testing.T) {
	installSeams(t)

	detectMarkersFn = func(string) ([]string, error) { return []string{"a.go"}, nil }
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error {
		return errors.New("no model available")
	}

	err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog)
	if err == nil {
		t.Fatal("want error propagated from LLM")
	}
	if !strings.Contains(err.Error(), "no model available") {
		t.Fatalf("want 'no model available'; got %q", err.Error())
	}
}

// --- 14. fixConflictMarkers: markers remain after LLM → error --------------

func TestFixConflictMarkers_MarkersRemainAfterLLM(t *testing.T) {
	installSeams(t)

	detectMarkersFn = func(string) ([]string, error) {
		return []string{"unresolved.go"}, nil
	}
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error { return nil }

	err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog)
	if err == nil {
		t.Fatal("want error when markers persist after LLM")
	}
	if !strings.Contains(err.Error(), "unresolved.go") {
		t.Fatalf("want error listing 'unresolved.go'; got %q", err.Error())
	}
}

// --- 15. fixConflictMarkers: pure lockfile conflict → ZERO LLM calls -------
//
// This is the "token waste" regression guard: when every conflicted file is
// deterministic (lockfiles, changelogs, CI config), the fix-model MUST NOT
// be invoked. Previous behavior burned tokens on these even though `git
// checkout --theirs/--ours` is the correct answer.

func TestFixConflictMarkers_LockfileOnlyDoesNotInvokeLLM(t *testing.T) {
	installSeams(t)

	detectMarkersFn = func(string) ([]string, error) {
		return []string{"bun.lock", "apps/psypapka/CHANGELOG.md"}, nil
	}
	var llmCalls int32
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error {
		atomic.AddInt32(&llmCalls, 1)
		return nil
	}

	// Fake autoresolver: resolves both paths, empty Remaining.
	autoResolveFn = func(context.Context, string) autoResolver {
		return &fakeAutoResolver{
			resolved: map[string]string{
				"bun.lock":                     "theirs",
				"apps/psypapka/CHANGELOG.md":   "ours",
			},
		}
	}

	if err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
		t.Fatalf("want nil; got %v", err)
	}
	if atomic.LoadInt32(&llmCalls) != 0 {
		t.Fatalf("fix-model must NOT be invoked for purely-deterministic conflicts; got %d call(s)", llmCalls)
	}
}

// --- 16. fixConflictMarkers: mixed conflicts → LLM only sees undeterministic
//
// Lockfile handled deterministically, source file still needs LLM — the
// prompt should target only the source file, not the lockfile.

func TestFixConflictMarkers_MixedPromptsLLMOnlyForUndeterministic(t *testing.T) {
	installSeams(t)

	// detectMarkers returns the initial conflicted set, and then after LLM
	// (which in this test does nothing) the same "src/main.go" remains — so
	// the helper we wire up below for the test must itself track state.
	var detectCalls int32
	detectMarkersFn = func(string) ([]string, error) {
		n := atomic.AddInt32(&detectCalls, 1)
		if n == 1 {
			return []string{"bun.lock", "src/main.go"}, nil
		}
		// After the fake LLM "ran", pretend it resolved src/main.go.
		return nil, nil
	}

	var capturedPrompt string
	runPlainFn = func(_ context.Context, _ ai.Backend, _ string, prompt, _ string) error {
		capturedPrompt = prompt
		return nil
	}

	// autoresolve handles bun.lock, leaves src/main.go in Remaining.
	autoResolveFn = func(context.Context, string) autoResolver {
		return &fakeAutoResolver{
			resolved:  map[string]string{"bun.lock": "theirs"},
			remaining: []string{"src/main.go"},
		}
	}

	if err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
		t.Fatalf("want nil; got %v", err)
	}
	if !strings.Contains(capturedPrompt, "src/main.go") {
		t.Fatalf("LLM prompt should target src/main.go; got:\n%s", capturedPrompt)
	}
	if strings.Contains(capturedPrompt, "bun.lock") {
		t.Fatalf("LLM prompt should NOT mention bun.lock (already handled); got:\n%s", capturedPrompt)
	}
}

// --- 17. fixConflictMarkers: commit/push after autoresolve fails → falls through
//
// If the deterministic resolve succeeds but the final commit/push errors
// (rare: shallow fetch, no upstream, etc.), we should fall through to the
// LLM rather than silently returning nil and leaving a dirty tree.

func TestFixConflictMarkers_AutoResolveCommitFailureFallsThrough(t *testing.T) {
	installSeams(t)

	// Simulate: autoresolve modified bun.lock (remove from conflicted set)
	// but commit/push failed. On the post-LLM detection, the tree is clean
	// (LLM "ran" and didn't break anything new).
	var detectCalls int32
	detectMarkersFn = func(string) ([]string, error) {
		if atomic.AddInt32(&detectCalls, 1) == 1 {
			return []string{"bun.lock"}, nil
		}
		return nil, nil
	}
	var llmCalls int32
	runPlainFn = func(context.Context, ai.Backend, string, string, string) error {
		atomic.AddInt32(&llmCalls, 1)
		return nil
	}

	autoResolveFn = func(context.Context, string) autoResolver {
		return &fakeAutoResolver{
			resolved:  map[string]string{"bun.lock": "theirs"},
			commitErr: errors.New("no upstream"),
		}
	}

	if err := fixConflictMarkers(context.Background(), branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
		t.Fatalf("want nil; got %v", err)
	}
	if atomic.LoadInt32(&llmCalls) != 1 {
		t.Fatalf("fix-model must run when commit/push fails post-autoresolve; got %d calls", llmCalls)
	}
}

// fakeAutoResolver implements the unexported autoResolver interface.
type fakeAutoResolver struct {
	resolved  map[string]string
	remaining []string
	commitErr error
}

func (f *fakeAutoResolver) Apply() (autoresolve.Result, error) {
	res := autoresolve.Result{Resolved: map[string]autoresolve.Side{}, Remaining: f.remaining}
	for p, side := range f.resolved {
		res.Resolved[p] = autoresolve.Side(side)
	}
	return res, nil
}

func (f *fakeAutoResolver) CommitAndPush() error { return f.commitErr }
