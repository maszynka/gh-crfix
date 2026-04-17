package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRun_HookDoesNotWriteJSON_StaleFileIsDiscarded pre-populates
// .gh-crfix/validation.json with a stale result (from a prior run) and uses a
// hook that DOES NOT write the file. Run must not read the stale file —
// instead it should return a Result built from the hook's stdout/exit code.
func TestRun_HookDoesNotWriteJSON_StaleFileIsDiscarded(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()

	// Pre-populate stale JSON from a previous run.
	outPath := filepath.Join(wt, ".gh-crfix", "validation.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := Result{
		Available:   true,
		Ran:         true,
		Success:     false,
		TestsFailed: true,
		Summary:     "stale-data-from-previous-run",
	}
	staleBytes, _ := json.Marshal(stale)
	if err := os.WriteFile(outPath, staleBytes, 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	// Hook that does NOT write to GH_CRFIX_VALIDATION_OUT but succeeds.
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	writeExec(t, hook, "#!/bin/sh\necho fresh-success\nexit 0\n")

	res := Run(nil, wt, Runner{Kind: RunnerHook, Command: hook}, nil)

	if res.Summary == "stale-data-from-previous-run" {
		t.Fatalf("Run returned stale JSON contents; want fresh stdout-based Result. Got: %+v", res)
	}
	if !res.Success {
		t.Fatalf("expected Success=true from fresh hook run, got %+v", res)
	}
	if res.TestsFailed {
		t.Fatalf("expected TestsFailed=false from fresh hook run, got %+v", res)
	}
}

// TestRun_HookWritesFreshJSONOverwritingStale demonstrates the positive path:
// when the hook writes a NEW JSON file the new content is returned (and the
// stale content is not merged in).
func TestRun_HookWritesFreshJSONOverwritingStale(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()

	outPath := filepath.Join(wt, ".gh-crfix", "validation.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := `{"available":true,"ran":true,"success":true,"tests_failed":false,"summary":"OLD"}`
	if err := os.WriteFile(outPath, []byte(stale), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	// Hook writes a fresh JSON file with a different summary.
	script := "#!/bin/sh\n" +
		"mkdir -p \"$(dirname \"$GH_CRFIX_VALIDATION_OUT\")\"\n" +
		"cat > \"$GH_CRFIX_VALIDATION_OUT\" <<'EOF'\n" +
		`{"available":true,"ran":true,"success":false,"tests_failed":true,"summary":"NEW"}` + "\n" +
		"EOF\n" +
		"exit 0\n"
	writeExec(t, hook, script)

	res := Run(nil, wt, Runner{Kind: RunnerHook, Command: hook}, nil)
	if res.Summary != "NEW" {
		t.Fatalf("expected summary=NEW, got %q", res.Summary)
	}
}
