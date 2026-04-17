package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/progress"
)

// newTestConfig builds a DashboardConfig backed by real tracker + run
// rooted at a tmp dir. The caller owns cleanup of the Run.
func newTestConfig(t *testing.T, prNums []int) (DashboardConfig, *progress.Tracker, *logs.Run, func()) {
	t.Helper()

	tmp := t.TempDir()
	tr := progress.NewTracker(filepath.Join(tmp, "progress"))
	for _, n := range prNums {
		if err := tr.Init(n); err != nil {
			t.Fatalf("tracker init pr %d: %v", n, err)
		}
	}

	// Build a Run with a dir we control so PRLog() resolves under tmp.
	// We avoid logs.NewRun because it creates tmp dirs and symlinks — not
	// needed here. Instead we fabricate a Run with the zero master log
	// (nil) pointing at a per-test dir. The tui package must tolerate a
	// zero/fresh Run with no started/status files.
	run, err := logs.NewRun()
	if err != nil {
		t.Fatalf("logs.NewRun: %v", err)
	}

	titles := map[int]string{}
	for i, n := range prNums {
		titles[n] = fmt.Sprintf("feat: change %d", i+1)
	}

	cfg := DashboardConfig{
		PRNums:  append([]int(nil), prNums...),
		Titles:  titles,
		Tracker: tr,
		Run:     run,
		Refresh: 10 * time.Millisecond,
	}

	cleanup := func() { _ = run.Close() }
	return cfg, tr, run, cleanup
}

func TestFreshModelShowsQueuedRows(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{101, 102, 103})
	defer cleanup()

	m := newModel(cfg)
	view := m.View()

	for _, pr := range []int{101, 102, 103} {
		if !strings.Contains(view, fmt.Sprintf("%d", pr)) {
			t.Errorf("view missing PR %d:\n%s", pr, view)
		}
	}
	// All three should be queued.
	if got := strings.Count(view, "queued"); got < 3 {
		t.Errorf("expected at least 3 'queued' markers, got %d. view:\n%s", got, view)
	}
}

func TestRunningStepShowsUpAfterRefresh(t *testing.T) {
	cfg, tr, _, cleanup := newTestConfig(t, []int{201, 202})
	defer cleanup()

	if err := tr.Set(201, progress.StepGate, progress.Running, ""); err != nil {
		t.Fatalf("Set: %v", err)
	}

	m := newModel(cfg)
	// Simulate a refresh tick that reloads snapshot.
	updated, _ := m.Update(refreshMsg{})
	m2, ok := updated.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", updated)
	}
	view := m2.View()

	if !strings.Contains(view, "running") {
		t.Errorf("view missing 'running' indicator:\n%s", view)
	}
	if !strings.Contains(view, "gate") {
		t.Errorf("view missing 'gate' step:\n%s", view)
	}
}

func TestNavigationToDetailAndBack(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{301, 302, 303})
	defer cleanup()

	m := newModel(cfg)

	// Down, down — select row index 2.
	m1, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := m1.(model)
	m2, _ := mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = m2.(model)

	// Enter — switch to detail for PR 303.
	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = m3.(model)

	view := mm.View()
	if !strings.Contains(view, "PR #303") {
		t.Errorf("detail view missing 'PR #303':\n%s", view)
	}

	// Left — back to dashboard.
	m4, _ := mm.Update(tea.KeyMsg{Type: tea.KeyLeft})
	mm = m4.(model)

	view = mm.View()
	// Dashboard shows totals line.
	if !strings.Contains(view, "queued") {
		t.Errorf("expected dashboard after back, got:\n%s", view)
	}
	if strings.Contains(view, "PR #303") && strings.Contains(view, "Checklist") {
		t.Errorf("expected dashboard, still in detail:\n%s", view)
	}
}

func TestTotalsLineMatchesState(t *testing.T) {
	cfg, tr, _, cleanup := newTestConfig(t, []int{401, 402, 403, 404})
	defer cleanup()

	// 1 running, 1 done, 1 failed, 1 queued.
	if err := tr.Set(401, progress.StepGate, progress.Running, ""); err != nil {
		t.Fatal(err)
	}
	// For Done we need all steps done. Simpler: mark postfix done, which
	// dashboard logic treats as overall done.
	for _, s := range progress.AllSteps() {
		if err := tr.Set(402, s, progress.Done, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Set(403, progress.StepFix, progress.Failed, "boom"); err != nil {
		t.Fatal(err)
	}

	m := newModel(cfg)
	updated, _ := m.Update(refreshMsg{})
	mm := updated.(model)
	view := mm.View()

	for _, want := range []string{"1 running", "1 done", "1 failed", "1 queued"} {
		if !strings.Contains(view, want) {
			t.Errorf("totals line missing %q. view:\n%s", want, view)
		}
	}
}

func TestWindowResizeDoesNotPanic(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{501})
	defer cleanup()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on resize: %v", r)
		}
	}()

	m := newModel(cfg)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm := updated.(model)
	_ = mm.View()

	// Tiny terminal shouldn't panic either.
	updated2, _ := mm.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
	mm2 := updated2.(model)
	_ = mm2.View()
}

func TestRefreshTickAdvancesRows(t *testing.T) {
	cfg, tr, _, cleanup := newTestConfig(t, []int{601, 602})
	defer cleanup()

	m := newModel(cfg)

	// Tick 1: set 601 → running.
	if err := tr.Set(601, progress.StepGate, progress.Running, ""); err != nil {
		t.Fatal(err)
	}
	updated, _ := m.Update(refreshMsg{})
	mm := updated.(model)
	v1 := mm.View()
	if !strings.Contains(v1, "running") {
		t.Errorf("tick 1: expected running, got:\n%s", v1)
	}

	// Tick 2: set all steps done for 601 → overall done.
	for _, s := range progress.AllSteps() {
		if err := tr.Set(601, s, progress.Done, ""); err != nil {
			t.Fatal(err)
		}
	}
	updated2, _ := mm.Update(refreshMsg{})
	mm2 := updated2.(model)
	v2 := mm2.View()
	if !strings.Contains(v2, "done") {
		t.Errorf("tick 2: expected done, got:\n%s", v2)
	}
}

func TestNilRunAndEmptyTrackerDoNotCrash(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with nil inputs: %v", r)
		}
	}()

	cfg := DashboardConfig{
		PRNums:  []int{999},
		Titles:  map[int]string{999: "empty test"},
		Tracker: progress.NewTracker(t.TempDir()), // not Init'd
		Run:     nil,
		Refresh: 10 * time.Millisecond,
	}
	m := newModel(cfg)
	_ = m.View()
	updated, _ := m.Update(refreshMsg{})
	mm := updated.(model)
	_ = mm.View()

	// Enter detail view.
	m2, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm2 := m2.(model)
	_ = mm2.View()
}

func TestQuitKeyReturnsQuitCmd(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{701})
	defer cleanup()

	m := newModel(cfg)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatalf("expected quit command, got nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestDetailViewScrollDoesNotCrashWithoutLog(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{801})
	defer cleanup()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic scrolling empty detail: %v", r)
		}
	}()

	m := newModel(cfg)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(model)
	// scroll down, up
	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = m3.(model)
	m4, _ := mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm = m4.(model)
	_ = mm.View()
}

func TestDetailShowsLogTail(t *testing.T) {
	cfg, _, run, cleanup := newTestConfig(t, []int{901})
	defer cleanup()

	// Write a short log file with > 20 lines to test tail trimming.
	logPath := run.PRLog(901)
	var sb strings.Builder
	for i := 1; i <= 30; i++ {
		sb.WriteString(fmt.Sprintf("log-line-%d\n", i))
	}
	if err := os.WriteFile(logPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	m := newModel(cfg)
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(model)
	// Execute the log-tail cmd returned by entering detail view.
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m3, _ := mm.Update(msg)
			mm = m3.(model)
		}
	}

	view := mm.View()
	if !strings.Contains(view, "log-line-30") {
		t.Errorf("expected last log line in detail, got:\n%s", view)
	}
	// Should not contain very early lines — tail only.
	if strings.Contains(view, "log-line-1\n") && strings.Contains(view, "log-line-2\n") {
		// (loose check — if both early lines appear *as separate lines*,
		// we aren't tailing)
		t.Errorf("detail view appears to show full log instead of tail:\n%s", view)
	}
}
