package workflow

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maszynka/gh-crfix/internal/ai"
)

// --- 10. fixConflictMarkers: no markers → nil, no LLM -----------------------

func TestFixConflictMarkers_Clean(t *testing.T) {
	installSeams(t)

	var plainCalls int32
	detectMarkersFn = func(string) ([]string, error) { return nil, nil }
	runPlainFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&plainCalls, 1)
		return nil
	}

	if err := fixConflictMarkers(branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
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
	runPlainFn = func(ai.Backend, string, string, string) error {
		t.Fatalf("runPlain must not be called in dry-run")
		return nil
	}

	opts := branchBaseOpts(t)
	opts.DryRun = true
	if err := fixConflictMarkers(opts, t.TempDir(), noopLog); err != nil {
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
	runPlainFn = func(ai.Backend, string, string, string) error {
		atomic.AddInt32(&ranLLM, 1)
		return nil
	}

	if err := fixConflictMarkers(branchBaseOpts(t), t.TempDir(), noopLog); err != nil {
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
	runPlainFn = func(ai.Backend, string, string, string) error {
		return errors.New("no model available")
	}

	err := fixConflictMarkers(branchBaseOpts(t), t.TempDir(), noopLog)
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
	runPlainFn = func(ai.Backend, string, string, string) error { return nil }

	err := fixConflictMarkers(branchBaseOpts(t), t.TempDir(), noopLog)
	if err == nil {
		t.Fatal("want error when markers persist after LLM")
	}
	if !strings.Contains(err.Error(), "unresolved.go") {
		t.Fatalf("want error listing 'unresolved.go'; got %q", err.Error())
	}
}
