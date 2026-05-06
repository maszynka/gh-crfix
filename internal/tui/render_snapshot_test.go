package tui

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/progress"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden from current View() output")

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// normalize strips ANSI escapes, trailing whitespace per line, and collapses
// the final newline so snapshots are stable across terminals.
func normalize(s string) string {
	s = stripANSI(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// snapshot compares got against testdata/<name>.golden. Run with
// -update-golden to rewrite the file. The repo's CI must run without that
// flag; the flag is only for the human author when intentionally changing
// TUI layout.
func snapshot(t *testing.T, name, got string) {
	t.Helper()
	got = normalize(got)
	p := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v\nfirst run: go test ./internal/tui/ -run %s -update-golden", p, err, t.Name())
	}
	if got != string(want) {
		t.Errorf("snapshot mismatch for %s\n--- got (%d bytes) ---\n%s\n--- want (%d bytes) ---\n%s\nrun with -update-golden to accept", name, len(got), got, len(want), string(want))
	}
}

// resize sends a WindowSizeMsg to a tea.Model and returns the updated model.
func resize(m tea.Model, w, h int) tea.Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next
}

// --- Launcher snapshots --------------------------------------------------

func TestSnapshot_LauncherInitial(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)
	got := resize(m, 100, 30).(*launcherModel).View()
	snapshot(t, "launcher_initial", got)
}

func TestSnapshot_LauncherFocusedConcurrency(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)
	resized := resize(m, 100, 30).(*launcherModel)
	// Down x4 = move from target to concurrency field.
	final := send(t, resized, "down", "down", "down", "down")
	snapshot(t, "launcher_focused_concurrency", final.View())
}

func TestSnapshot_LauncherSubmitError(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Config{}, Models: stubModels()}
	m := newTestModel(t, cfg)
	resized := resize(m, 100, 30).(*launcherModel)
	// Submit immediately with empty target → validation error.
	final := send(t, resized, "enter")
	snapshot(t, "launcher_submit_error", final.View())
}

// --- Dashboard snapshots --------------------------------------------------

func TestSnapshot_DashboardAllQueued(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{101, 102, 103})
	defer cleanup()
	m := newModel(cfg)
	got := resize(m, 100, 30).(model).View()
	snapshot(t, "dashboard_all_queued", got)
}

func TestSnapshot_DashboardMixedStates(t *testing.T) {
	cfg, tr, _, cleanup := newTestConfig(t, []int{201, 202, 203})
	defer cleanup()
	// PR 201: setup done, gate running.
	mustSet(t, tr, 201, progress.StepSetup, progress.Done)
	mustSet(t, tr, 201, progress.StepGate, progress.Running)
	// PR 202: setup failed.
	mustSet(t, tr, 202, progress.StepSetup, progress.Failed)
	// PR 203: skipped at gate.
	mustSet(t, tr, 203, progress.StepSetup, progress.Done)
	mustSet(t, tr, 203, progress.StepGate, progress.Skipped)

	m := newModel(cfg)
	resized := resize(m, 100, 30).(model)
	updated, _ := resized.Update(refreshMsg{})
	snapshot(t, "dashboard_mixed_states", updated.(model).View())
}

func TestSnapshot_DashboardDetailView(t *testing.T) {
	cfg, tr, _, cleanup := newTestConfig(t, []int{301, 302})
	defer cleanup()
	mustSet(t, tr, 301, progress.StepSetup, progress.Done)
	mustSet(t, tr, 301, progress.StepGate, progress.Running)
	mustSet(t, tr, 302, progress.StepSetup, progress.Done)

	m := newModel(cfg)
	resized := resize(m, 100, 30).(model)
	refreshed, _ := resized.Update(refreshMsg{})
	// Enter drills into detail view of the selected (first) PR.
	entered, _ := refreshed.Update(tea.KeyMsg{Type: tea.KeyEnter})
	snapshot(t, "dashboard_detail", entered.(model).View())
}

func mustSet(t *testing.T, tr *progress.Tracker, pr int, step progress.Step, st progress.Status) {
	t.Helper()
	if err := tr.Set(pr, step, st, ""); err != nil {
		t.Fatalf("tracker.Set pr=%d step=%s: %v", pr, step, err)
	}
}
