package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRunGate_ContextCancellationAborts ensures a cancelled ctx kills the
// subprocess quickly and returns an error wrapping context.Canceled.
func TestRunGate_ContextCancellationAborts(t *testing.T) {
	dir := isolatePATH(t)
	// Fake claude sleeps forever so we can prove ctx actually cancels it.
	writeScript(t, dir, "claude", `#!/bin/sh
sleep 30
`)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 50ms so we give the subprocess a moment to spawn.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := RunGate(ctx, BackendClaude, "sonnet", "p", map[string]interface{}{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
	// Must not wait the full 30s sleep.
	if elapsed > 5*time.Second {
		t.Fatalf("RunGate took %v after cancel; expected <5s", elapsed)
	}
	// Should signal via context error (not necessarily wrapped, but detectable).
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "signal") &&
		!strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("error should indicate cancellation/kill; got: %v", err)
	}
}

// TestRunGate_DeadlineExceeded asserts a ctx deadline kills the subprocess
// and surfaces context.DeadlineExceeded (or an error containing it).
func TestRunGate_DeadlineExceeded(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
sleep 30
`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := RunGate(ctx, BackendClaude, "sonnet", "p", map[string]interface{}{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on deadline exceeded")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunGate took %v after deadline; expected <5s", elapsed)
	}
}

// TestRunFix_ContextCancellationAborts ensures RunFix also honors ctx.
func TestRunFix_ContextCancellationAborts(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
sleep 30
`)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := RunFix(ctx, BackendClaude, "sonnet", "prompt", t.TempDir())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunFix took %v after cancel; expected <5s", elapsed)
	}
}

// TestRunGate_RespectsEnvGateTimeout asserts GH_CRFIX_GATE_TIMEOUT narrows
// the default 5min gate timeout.
func TestRunGate_RespectsEnvGateTimeout(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
sleep 30
`)
	t.Setenv("GH_CRFIX_GATE_TIMEOUT", "150ms")

	start := time.Now()
	_, err := RunGate(context.Background(), BackendClaude, "sonnet", "p", map[string]interface{}{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunGate took %v; env GH_CRFIX_GATE_TIMEOUT=150ms should kill it quickly", elapsed)
	}
}

// TestRunFix_RespectsEnvFixTimeout asserts GH_CRFIX_FIX_TIMEOUT narrows the
// default fix timeout.
func TestRunFix_RespectsEnvFixTimeout(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
sleep 30
`)
	t.Setenv("GH_CRFIX_FIX_TIMEOUT", "150ms")

	start := time.Now()
	err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", t.TempDir())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunFix took %v; env GH_CRFIX_FIX_TIMEOUT=150ms should kill it quickly", elapsed)
	}
}
