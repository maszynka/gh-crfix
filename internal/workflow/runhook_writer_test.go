package workflow

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunHook_StreamsStdoutToWriter is the autofix-hook counterpart to the
// AI writer test: in dashboard mode we cannot let subprocess stdout/stderr
// fall through to the parent process's stdio, because the parent's TTY is
// in raw mode under bubbletea and writes lacking \r\n cause stair-stepped
// output. The fix is to accept a writer and route both streams through it.
func TestRunHook_StreamsStdoutToWriter(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "autofix.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'HOOK_STDOUT_MARKER\\n'\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	var out, errw bytes.Buffer
	if err := runHook(context.Background(), hook, dir, &out, &errw); err != nil {
		t.Fatalf("runHook returned %v; want nil", err)
	}
	if !strings.Contains(out.String(), "HOOK_STDOUT_MARKER") {
		t.Errorf("hook stdout was not routed to writer; out=%q errw=%q", out.String(), errw.String())
	}
}

// TestRunHook_StreamsStderrToWriter — the stderr case that matches the
// real-world bun/vitest scenario.
func TestRunHook_StreamsStderrToWriter(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "autofix.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'HOOK_STDERR_MARKER\\n' 1>&2\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	var out, errw bytes.Buffer
	if err := runHook(context.Background(), hook, dir, &out, &errw); err != nil {
		t.Fatalf("runHook returned %v; want nil", err)
	}
	if !strings.Contains(errw.String(), "HOOK_STDERR_MARKER") {
		t.Errorf("hook stderr was not routed to writer; out=%q errw=%q", out.String(), errw.String())
	}
}

// TestRunHook_NilWritersFallBackToOSStreams keeps plain-mode back-compat —
// when the caller doesn't pass writers, the subprocess inherits parent stdio
// the same as it did before the writer parameters were added.
func TestRunHook_NilWritersFallBackToOSStreams(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "autofix.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := runHook(context.Background(), hook, dir, nil, nil); err != nil {
		t.Fatalf("runHook with nil writers returned %v; want nil", err)
	}
}
