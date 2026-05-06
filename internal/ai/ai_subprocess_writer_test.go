package ai

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRunFix_StreamsStdoutToWriter asserts that subprocess stdout from the AI
// fix call (claude/codex) is routed to the writer passed in by the caller,
// rather than directly to os.Stdout. Without this, dashboard mode leaks
// subprocess output onto the raw-mode TTY and produces stair-stepped lines.
func TestRunFix_StreamsStdoutToWriter(t *testing.T) {
	dir := isolatePATH(t)
	// Fake claude prints a unique marker on stdout, then exits 0.
	writeScript(t, dir, "claude", `#!/bin/sh
printf "FIX_STDOUT_MARKER\n"
`)

	var out, errw bytes.Buffer
	err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", t.TempDir(), &out, &errw)
	if err != nil {
		t.Fatalf("RunFix returned %v; want nil", err)
	}
	if !strings.Contains(out.String(), "FIX_STDOUT_MARKER") {
		t.Errorf("subprocess stdout was not routed to writer; out=%q errw=%q", out.String(), errw.String())
	}
}

// TestRunFix_StreamsStderrToWriter is the stderr counterpart. This is the one
// the dashboard-stair-step bug actually hinges on: bun/vitest/pnpm test runners
// dump their output on stderr and that's what was leaking to the raw TTY.
func TestRunFix_StreamsStderrToWriter(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
printf "FIX_STDERR_MARKER\n" 1>&2
`)

	var out, errw bytes.Buffer
	err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", t.TempDir(), &out, &errw)
	if err != nil {
		t.Fatalf("RunFix returned %v; want nil", err)
	}
	if !strings.Contains(errw.String(), "FIX_STDERR_MARKER") {
		t.Errorf("subprocess stderr was not routed to writer; out=%q errw=%q", out.String(), errw.String())
	}
}

// TestRunFix_NilWritersFallBackToOSStreams keeps the back-compat for callers
// that don't care about routing (plain mode, no dashboard). Passing nil should
// leave the subprocess inheriting the parent's stdio — same behavior as before
// the writer parameters were added.
func TestRunFix_NilWritersFallBackToOSStreams(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
exit 0
`)
	// Just verifying no panic / no error when nil writers are passed. The
	// subprocess output, if any, lands on the test process's stdio.
	if err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", t.TempDir(), nil, nil); err != nil {
		t.Fatalf("RunFix with nil writers returned %v; want nil", err)
	}
}
