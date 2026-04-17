package workflow

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maszynka/gh-crfix/internal/ai"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
)

// TestProcessPR_OutWriterReceivesLogs asserts that when opts.Out is set the
// per-PR `log()` helper writes to that writer instead of os.Stdout. This is
// the review-thread fix for the hardcoded fmt.Printf in ProcessPR.
func TestProcessPR_OutWriterReceivesLogs(t *testing.T) {
	installSeams(t)

	fetchPRFn = func(string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{State: "CLOSED", HeadRefName: "feature", BaseRefName: "main", Title: "T"}, nil
	}

	var buf bytes.Buffer
	opts := Options{
		Repo:      "owner/repo",
		PRNum:     42,
		RepoRoot:  t.TempDir(),
		AIBackend: ai.BackendClaude,
		GateModel: "g",
		FixModel:  "f",
		Out:       &buf,
	}
	res := ProcessPR(opts)
	if res.Status != "skipped" {
		t.Fatalf("want skipped, got %q", res.Status)
	}

	got := buf.String()
	if !strings.Contains(got, "[PR #42]") {
		t.Errorf("Out buffer missing '[PR #42]' tag: %q", got)
	}
	if !strings.Contains(got, "fetching PR metadata") {
		t.Errorf("Out buffer missing 'fetching PR metadata': %q", got)
	}
}

// TestProcessPR_NilOutFallsBackToStdout documents the default: when opts.Out
// is nil the writer falls back to os.Stdout (same as legacy behavior). We
// verify by NOT providing Out and confirming the call does not panic and
// the result shape is correct. The fact that fmt.Printf still works is
// covered by pre-existing tests.
func TestProcessPR_NilOutFallsBackToStdout(t *testing.T) {
	installSeams(t)

	fetchPRFn = func(string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{State: "CLOSED", HeadRefName: "feature", BaseRefName: "main", Title: "T"}, nil
	}

	opts := Options{
		Repo:      "owner/repo",
		PRNum:     99,
		RepoRoot:  t.TempDir(),
		AIBackend: ai.BackendClaude,
		GateModel: "g",
		FixModel:  "f",
	}
	res := ProcessPR(opts)
	if res.Status != "skipped" {
		t.Fatalf("want skipped, got %q", res.Status)
	}
}
