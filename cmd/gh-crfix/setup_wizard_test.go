package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maszynka/gh-crfix/internal/config"
)

// TestRunSetupWizard_AcceptsValidModes drives the wizard with each of the
// three accepted answers and checks that the persisted config picks them up.
func TestRunSetupWizard_AcceptsValidModes(t *testing.T) {
	for _, mode := range []string{"temp", "reuse", "stash"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "defaults")
			in := strings.NewReader(mode + "\n")
			var out bytes.Buffer

			cfg := config.Defaults()
			got, err := runSetupWizard(in, &out, cfg, path)
			if err != nil {
				t.Fatalf("wizard: %v", err)
			}
			if got.WorktreeMode != mode {
				t.Errorf("WorktreeMode = %q, want %q", got.WorktreeMode, mode)
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if loaded.WorktreeMode != mode {
				t.Errorf("persisted WorktreeMode = %q, want %q", loaded.WorktreeMode, mode)
			}
		})
	}
}

// TestRunSetupWizard_EmptyKeepsDefault is the "user just hit enter" path:
// the displayed default value (cfg.WorktreeMode) is preserved.
func TestRunSetupWizard_EmptyKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "defaults")
	in := strings.NewReader("\n") // empty input == accept default
	var out bytes.Buffer

	cfg := config.Defaults()
	cfg.WorktreeMode = "stash" // pretend the user previously chose stash

	got, err := runSetupWizard(in, &out, cfg, path)
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	if got.WorktreeMode != "stash" {
		t.Errorf("empty input must keep current default; got %q want %q", got.WorktreeMode, "stash")
	}
}

// TestRunSetupWizard_GarbageFallsBack confirms that an unknown answer warns
// and falls back to the displayed default rather than persisting nonsense.
func TestRunSetupWizard_GarbageFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "defaults")
	in := strings.NewReader("yolo\n")
	var out bytes.Buffer

	cfg := config.Defaults() // default WorktreeMode = "temp"
	got, err := runSetupWizard(in, &out, cfg, path)
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	if got.WorktreeMode != "temp" {
		t.Errorf("garbage input must fall back to default; got %q", got.WorktreeMode)
	}
	if !strings.Contains(out.String(), "warning") {
		t.Errorf("wizard should print a warning on unknown mode; got: %q", out.String())
	}
}

// TestFirstRunNeeded checks the missing-vs-existing branch — the wizard
// auto-trigger logic relies on this returning true exactly when the file
// doesn't exist (so a brand-new install gets the prompt once).
func TestFirstRunNeeded(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	if !firstRunNeeded(missing) {
		t.Errorf("missing path should be first-run; got false")
	}

	existing := filepath.Join(dir, "yes")
	if err := os.WriteFile(existing, []byte("AI_BACKEND=auto\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if firstRunNeeded(existing) {
		t.Errorf("existing config file should not be first-run; got true")
	}
}
