package workflow

import (
	"strings"
	"testing"

	"github.com/maszynka/gh-crfix/internal/gate"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
	"github.com/maszynka/gh-crfix/internal/triage"
	"github.com/maszynka/gh-crfix/internal/validate"
)

// ----- buildGatePrompt --------------------------------------------------------

func TestBuildGatePrompt_IncludesScoreAndThreshold(t *testing.T) {
	threads := []ghapi.Thread{
		{ID: "t1", Path: "a.go", Line: 5, Comments: []ghapi.Comment{
			{Author: "alice", Body: "please fix"},
		}},
	}
	classes := []triage.Classification{
		{ThreadID: "t1", Path: "a.go", Line: 5, Reason: "needs semantic review"},
	}
	vr := validate.Result{}
	gctx := gate.BuildGateContext(
		gate.TriageSummary{NeedsLLM: []gate.TriageEntry{{ThreadID: "t1", Reason: "needs semantic review"}}},
		gate.ValidationResult{},
		gate.ScoreWeights{NeedsLLM: 0.5, PRComment: 0.5, TestFailure: 1.0},
	)

	out := buildGatePrompt(threads, classes, vr, nil, gctx)

	if !strings.Contains(out, "total_score: 0.500") {
		t.Errorf("missing score line: %q", out)
	}
	if !strings.Contains(out, "threshold: 1.0") {
		t.Errorf("missing threshold: %q", out)
	}
	if !strings.Contains(out, "**Thread t1**") {
		t.Errorf("missing thread id: %q", out)
	}
	if !strings.Contains(out, "needs semantic review") {
		t.Errorf("missing reason: %q", out)
	}
	// No CI, no validation failure — sections should not appear.
	if strings.Contains(out, "Failing CI Checks") {
		t.Errorf("unexpected CI section: %q", out)
	}
	if strings.Contains(out, "Validation Failure") {
		t.Errorf("unexpected validation section: %q", out)
	}
}

func TestBuildGatePrompt_IncludesCIAndValidationWhenPresent(t *testing.T) {
	threads := []ghapi.Thread{{ID: "t1", Path: "a.go", Line: 1}}
	classes := []triage.Classification{{ThreadID: "t1", Path: "a.go", Line: 1, Reason: "x"}}
	vr := validate.Result{Ran: true, Success: false, Summary: "test X failed"}
	ci := []ghapi.CICheck{{Name: "ci-name", LogText: "log body"}}
	gctx := gate.BuildGateContext(
		gate.TriageSummary{NeedsLLM: []gate.TriageEntry{{ThreadID: "t1", Reason: "x"}}},
		gate.ValidationResult{TestsFailed: true},
		gate.ScoreWeights{NeedsLLM: 0.5, TestFailure: 1.0},
	)

	out := buildGatePrompt(threads, classes, vr, ci, gctx)

	if !strings.Contains(out, "Failing CI Checks") {
		t.Errorf("missing CI section")
	}
	if !strings.Contains(out, "ci-name") {
		t.Errorf("missing CI check name")
	}
	if !strings.Contains(out, "log body") {
		t.Errorf("missing CI log body")
	}
	if !strings.Contains(out, "Validation Failure") {
		t.Errorf("missing validation failure section")
	}
	if !strings.Contains(out, "test X failed") {
		t.Errorf("missing validation summary")
	}
}

func TestBuildGatePrompt_NoValidationSectionWhenSuccess(t *testing.T) {
	threads := []ghapi.Thread{{ID: "t1", Path: "a.go"}}
	classes := []triage.Classification{{ThreadID: "t1", Reason: "x"}}
	// Ran but Success=true — section must be absent.
	vr := validate.Result{Ran: true, Success: true, Summary: "all passed"}
	gctx := gate.GateContext{Threshold: 1, TotalScore: 0}

	out := buildGatePrompt(threads, classes, vr, nil, gctx)

	if strings.Contains(out, "Validation Failure") {
		t.Errorf("validation section should not appear when success=true")
	}
}

// ----- buildFixPrompt ---------------------------------------------------------

func TestBuildFixPrompt_FiltersThreadsAndMentionsArtifact(t *testing.T) {
	threads := []ghapi.Thread{
		{ID: "t1", Path: "a.go", Line: 1},
		{ID: "t2", Path: "b.go", Line: 2},
	}
	classes := []triage.Classification{
		{ThreadID: "t1", Path: "a.go", Line: 1, Reason: "r1"},
		{ThreadID: "t2", Path: "b.go", Line: 2, Reason: "r2"},
	}
	vr := validate.Result{}
	out := buildFixPrompt(threads, classes, []string{"t1"}, vr, nil)

	if !strings.Contains(out, "thread-responses.json") {
		t.Errorf("instructions block missing thread-responses.json: %q", out)
	}
	if !strings.Contains(out, "**Thread t1**") {
		t.Errorf("selected thread missing: %q", out)
	}
	if strings.Contains(out, "**Thread t2**") {
		t.Errorf("filtered thread leaked into output: %q", out)
	}
}

func TestBuildFixPrompt_IncludesCIAndTestSections(t *testing.T) {
	threads := []ghapi.Thread{{ID: "t1", Path: "a.go"}}
	classes := []triage.Classification{{ThreadID: "t1", Reason: "r"}}
	vr := validate.Result{Ran: true, Success: false, Summary: "boom"}
	ci := []ghapi.CICheck{{Name: "lint", LogText: "eslint failed"}}

	out := buildFixPrompt(threads, classes, []string{"t1"}, vr, ci)

	if !strings.Contains(out, "Failing CI Checks (must fix)") {
		t.Errorf("missing CI section")
	}
	if !strings.Contains(out, "lint") || !strings.Contains(out, "eslint failed") {
		t.Errorf("missing CI content")
	}
	if !strings.Contains(out, "Test Failures (must fix)") {
		t.Errorf("missing test failures section")
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("missing test summary")
	}
}

func TestBuildFixPrompt_NoFilterIncludesAll(t *testing.T) {
	threads := []ghapi.Thread{
		{ID: "t1", Path: "a.go"},
		{ID: "t2", Path: "b.go"},
	}
	classes := []triage.Classification{
		{ThreadID: "t1", Reason: "r1"},
		{ThreadID: "t2", Reason: "r2"},
	}

	// Empty threadIDs slice: every thread in classes is included.
	out := buildFixPrompt(threads, classes, nil, validate.Result{}, nil)

	if !strings.Contains(out, "**Thread t1**") || !strings.Contains(out, "**Thread t2**") {
		t.Errorf("both threads should be present when filter is empty: %q", out)
	}
}

// ----- buildSummaryComment ----------------------------------------------------

func TestBuildSummaryComment_TableRows(t *testing.T) {
	skip := []triage.Classification{{ThreadID: "s1"}, {ThreadID: "s2"}}
	auto := []triage.Classification{{ThreadID: "a1"}}
	already := []triage.Classification{{ThreadID: "f1"}}
	llm := []triage.Classification{{ThreadID: "l1"}, {ThreadID: "l2"}, {ThreadID: "l3"}}

	out := buildSummaryComment(skip, auto, already, llm, 4, 7)

	for _, want := range []string{
		"Total threads | 7",
		"Skipped (deterministic) | 2",
		"Already likely fixed | 1",
		"Auto/mechanical | 1",
		"Sent to LLM | 3",
		"Resolved | 4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in summary:\n%s", want, out)
		}
	}
}

// ----- truncate ---------------------------------------------------------------

func TestTruncate_ShortInputUnchanged(t *testing.T) {
	in := "hello world"
	if got := truncate(in, 100); got != in {
		t.Errorf("short input should be unchanged: got %q", got)
	}
}

func TestTruncate_LongInputHasMarker(t *testing.T) {
	in := strings.Repeat("x", 200)
	got := truncate(in, 50)
	if !strings.Contains(got, "(truncated)") {
		t.Errorf("expected (truncated) marker, got: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 50)) {
		t.Errorf("expected first 50 chars preserved, got: %q", got)
	}
}

// ----- firstLine --------------------------------------------------------------

func TestFirstLine_Multiline(t *testing.T) {
	in := "first\nsecond\nthird"
	if got := firstLine(in); got != "first" {
		t.Errorf("got %q, want %q", got, "first")
	}
}

func TestFirstLine_NoNewline(t *testing.T) {
	in := "only line"
	if got := firstLine(in); got != "only line" {
		t.Errorf("got %q, want %q", got, "only line")
	}
}

func TestFirstLine_Empty(t *testing.T) {
	if got := firstLine(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
