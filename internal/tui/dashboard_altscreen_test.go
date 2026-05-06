package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestDashboardRun_EntersAltScreen asserts that the dashboard launches
// bubbletea with WithAltScreen(), so its rendering happens on the
// alternate-screen buffer instead of the user's scrollback. Without this,
// the dashboard's frames interleave with the rest of the terminal's
// scrollback and produce the visual chaos the user reported.
//
// The check looks for the well-known "enter alt screen" CSI sequence
// \x1b[?1049h that bubbletea emits at program startup when WithAltScreen
// is set. There is no public API to inspect ProgramOptions, so we capture
// real output via tea.WithOutput.
func TestDashboardRun_EntersAltScreen(t *testing.T) {
	cfg, _, _, cleanup := newTestConfig(t, []int{1})
	defer cleanup()

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run the dashboard with our captured output. Cancel ctx after a
	// brief moment so the program quits — we only need to observe the
	// startup sequence.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_ = runWithIO(ctx, cfg, &out, strings.NewReader(""))

	const altScreenEnter = "\x1b[?1049h"
	if !strings.Contains(out.String(), altScreenEnter) {
		t.Errorf("dashboard did not emit alt-screen enter sequence (\\x1b[?1049h); got %q",
			out.String())
	}
}
