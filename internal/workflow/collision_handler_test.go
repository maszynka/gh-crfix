package workflow

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maszynka/gh-crfix/internal/ai"
)

// --- 5. handleCaseCollisions: no collisions → no error, no LLM ---------------

func TestHandleCaseCollisions_Clean(t *testing.T) {
	installSeams(t)

	var plainCalls int32
	detectCaseCollisionsFn = func(string) ([][]string, error) { return nil, nil }
	runPlainFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&plainCalls, 1)
		return nil
	}

	if err := handleCaseCollisions(branchBaseOpts(t), t.TempDir(), "feature"); err != nil {
		t.Fatalf("want nil err; got %v", err)
	}
	if atomic.LoadInt32(&plainCalls) != 0 {
		t.Fatalf("runPlain must not be called on clean worktree; got %d", plainCalls)
	}
}

// --- 6. handleCaseCollisions: dry-run + collisions → error ------------------

func TestHandleCaseCollisions_DryRun(t *testing.T) {
	installSeams(t)

	detectCaseCollisionsFn = func(string) ([][]string, error) {
		return [][]string{{"Foo.go", "foo.go"}}, nil
	}
	runPlainFn = func(ai.Backend, string, string, string) error {
		t.Fatalf("runPlain must not be called in dry-run")
		return nil
	}

	opts := branchBaseOpts(t)
	opts.DryRun = true
	err := handleCaseCollisions(opts, t.TempDir(), "feature")
	if err == nil {
		t.Fatal("want error in dry-run mode")
	}
	if !strings.Contains(err.Error(), "dry-run") {
		t.Fatalf("want error mentioning 'dry-run'; got %q", err.Error())
	}
}

// --- 7. handleCaseCollisions: LLM resolves → nil ----------------------------

func TestHandleCaseCollisions_LLMSucceedsCleanAfter(t *testing.T) {
	installSeams(t)

	var detectCalls int32
	// First call: collisions found. Second call (post-LLM): clean.
	detectCaseCollisionsFn = func(string) ([][]string, error) {
		n := atomic.AddInt32(&detectCalls, 1)
		if n == 1 {
			return [][]string{{"Foo.go", "foo.go"}}, nil
		}
		return nil, nil
	}
	var ranLLM int32
	runPlainFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&ranLLM, 1)
		return nil
	}

	if err := handleCaseCollisions(branchBaseOpts(t), t.TempDir(), "feature"); err != nil {
		t.Fatalf("want nil err after clean-up; got %v", err)
	}
	if atomic.LoadInt32(&ranLLM) != 1 {
		t.Fatalf("runPlain calls=%d want 1", ranLLM)
	}
	if atomic.LoadInt32(&detectCalls) != 2 {
		t.Fatalf("detectCaseCollisions calls=%d want 2", detectCalls)
	}
}

// --- 8. handleCaseCollisions: LLM runs but collisions remain → error ---------

func TestHandleCaseCollisions_LLMRemainingAfter(t *testing.T) {
	installSeams(t)

	detectCaseCollisionsFn = func(string) ([][]string, error) {
		return [][]string{{"Foo.go", "foo.go"}}, nil
	}
	runPlainFn = func(ai.Backend, string, string, string) error { return nil }

	err := handleCaseCollisions(branchBaseOpts(t), t.TempDir(), "feature")
	if err == nil {
		t.Fatal("want error when collisions remain after LLM")
	}
	if !strings.Contains(err.Error(), "remaining case collisions") {
		t.Fatalf("want 'remaining case collisions'; got %q", err.Error())
	}
}

// --- 9. handleCaseCollisions: LLM returns error → propagates ----------------

func TestHandleCaseCollisions_LLMError(t *testing.T) {
	installSeams(t)

	detectCaseCollisionsFn = func(string) ([][]string, error) {
		return [][]string{{"Foo.go", "foo.go"}}, nil
	}
	runPlainFn = func(ai.Backend, string, string, string) error {
		return errors.New("model crashed")
	}

	err := handleCaseCollisions(branchBaseOpts(t), t.TempDir(), "feature")
	if err == nil {
		t.Fatal("want error propagated from LLM")
	}
	if !strings.Contains(err.Error(), "model crashed") {
		t.Fatalf("want 'model crashed'; got %q", err.Error())
	}
}
