package workflow

import (
	"testing"

	"github.com/maszynka/gh-crfix/internal/triage"
)

func TestDeterministicResponses(t *testing.T) {
	skip := []triage.Classification{
		{ThreadID: "s1", Reason: "question-only thread"},
		{ThreadID: "s2", Reason: "non-actionable comment", ResolveWhenSkipped: true},
	}
	already := []triage.Classification{
		{ThreadID: "a1", Reason: "file no longer exists in worktree"},
	}
	out := deterministicResponses(skip, already)
	if len(out) != 3 {
		t.Fatalf("want 3 responses, got %d", len(out))
	}
	// Already-fixed should come first (matches Bash emit order).
	if out[0].ThreadID != "a1" || out[0].Action != "already_fixed" {
		t.Fatalf("unexpected first: %+v", out[0])
	}
	if out[1].ThreadID != "s1" || out[1].Action != "skipped" || out[1].ResolveWhenSkipped {
		t.Fatalf("unexpected second: %+v", out[1])
	}
	if out[2].ThreadID != "s2" || !out[2].ResolveWhenSkipped {
		t.Fatalf("unexpected third: %+v", out[2])
	}
}

func TestUncoveredResponses(t *testing.T) {
	autoList := []triage.Classification{
		{ThreadID: "auto1", Reason: "mechanical/simple comment"},
		{ThreadID: "auto2", Reason: "mechanical/simple comment"},
	}
	needsLLM := []triage.Classification{
		{ThreadID: "llm1", Reason: "needs semantic review"},
		{ThreadID: "llm2", Reason: "needs semantic review"},
	}
	existing := []ThreadResponse{
		{ThreadID: "auto1", Action: "fixed"},
		{ThreadID: "llm1", Action: "fixed"},
	}
	selected := []string{"llm1", "llm2"}

	out := uncoveredResponses(autoList, needsLLM, existing, selected)
	if len(out) != 2 {
		t.Fatalf("want 2 uncovered, got %d: %+v", len(out), out)
	}
	// auto2 — not covered, not in auto-fix
	if out[0].ThreadID != "auto2" {
		t.Fatalf("unexpected[0]: %+v", out[0])
	}
	// llm2 — selected but no code change applied
	if out[1].ThreadID != "llm2" {
		t.Fatalf("unexpected[1]: %+v", out[1])
	}
	if out[1].Comment == "" {
		t.Fatalf("empty comment")
	}
}
