package github

import (
	"context"
	"testing"
	"time"
)

// TestFetchPR_ContextCancellationAborts ensures FetchPR's gh invocation
// honors ctx cancellation.
func TestFetchPR_ContextCancellationAborts(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
sleep 30
`)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := FetchPR(ctx, "owner/repo", 1)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on cancellation")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("FetchPR took %v after cancel; expected <5s", elapsed)
	}
}

// TestGH_RespectsEnvTimeout asserts GH_CRFIX_GH_TIMEOUT overrides the 2-min
// default timeout so hung gh calls don't block forever.
func TestGH_RespectsEnvTimeout(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
sleep 30
`)
	t.Setenv("GH_CRFIX_GH_TIMEOUT", "150ms")

	start := time.Now()
	_, err := FetchPR(context.Background(), "owner/repo", 1)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("FetchPR took %v after env timeout; expected <5s", elapsed)
	}
}
