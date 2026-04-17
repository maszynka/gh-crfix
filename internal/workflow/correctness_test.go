package workflow

import (
	"context"
	"errors"
	"os"
	"testing"
)

// writeExecHook writes a shell script to path with +x perms, returning any error.
func writeExecHook(path, body string) error {
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// TestReplyAndResolve_ResolveErrorsDoNotIncrement asserts that resolve failures
// do not inflate the resolved counter. Reproduces the bug where the `skipped`
// branch incremented `resolved` unconditionally and the fixed/already_fixed
// branch did so on error paths too.
func TestReplyAndResolve_ResolveErrorsDoNotIncrement(t *testing.T) {
	installSeams(t)

	replyToThreadFn = func(context.Context, string, string) error { return nil }
	resolveThreadFn = func(context.Context, string) error { return errors.New("resolve boom") }

	responses := []ThreadResponse{
		{ThreadID: "a", Action: "fixed", Comment: "x"},
		{ThreadID: "b", Action: "already_fixed", Comment: "y"},
		{ThreadID: "c", Action: "skipped", Comment: "", ResolveWhenSkipped: true},
		{ThreadID: "d", Action: "skipped", Comment: "w"},
	}
	replied, resolved, skippedUnresolved := replyAndResolve(
		context.Background(),
		responses,
		true, // resolveSkipped=true
		func(string, ...interface{}) {},
	)

	if resolved != 0 {
		t.Errorf("resolved=%d want 0 when every ResolveThread call errors", resolved)
	}
	// Reply succeeds for a,b,d (c has empty comment).
	if replied != 3 {
		t.Errorf("replied=%d want 3", replied)
	}
	// skipped+resolveSkipped attempted — but we lost the race, so no bucket should claim d/c.
	// Our contract: skippedUnresolved is only incremented when resolveSkipped=false.
	if skippedUnresolved != 0 {
		t.Errorf("skippedUnresolved=%d want 0 (resolveSkipped=true)", skippedUnresolved)
	}
}

// TestReplyAndResolve_MixedResolveSuccessAndFailure mixes some successful
// ResolveThread calls with failing ones and asserts the resolved counter
// matches the number of successful calls exactly.
func TestReplyAndResolve_MixedResolveSuccessAndFailure(t *testing.T) {
	installSeams(t)

	replyToThreadFn = func(context.Context, string, string) error { return nil }
	// Succeed for "ok-*" ids, fail for anything else.
	resolveThreadFn = func(_ context.Context, id string) error {
		if len(id) >= 3 && id[:3] == "ok-" {
			return nil
		}
		return errors.New("resolve boom")
	}

	responses := []ThreadResponse{
		{ThreadID: "ok-1", Action: "fixed", Comment: "x"},        // success -> +1
		{ThreadID: "bad-1", Action: "fixed", Comment: "y"},       // fail -> 0
		{ThreadID: "ok-2", Action: "already_fixed", Comment: ""}, // success -> +1
		{ThreadID: "bad-2", Action: "already_fixed", Comment: ""},// fail -> 0
		{ThreadID: "ok-3", Action: "skipped", Comment: "", ResolveWhenSkipped: true}, // success -> +1
		{ThreadID: "bad-3", Action: "skipped", Comment: "", ResolveWhenSkipped: true}, // fail -> 0
	}
	_, resolved, _ := replyAndResolve(context.Background(), responses, false, func(string, ...interface{}) {})
	if resolved != 3 {
		t.Errorf("resolved=%d want 3 (count only successful ResolveThread calls)", resolved)
	}
}

// TestReplyAndResolve_ReplyErrorsDoNotIncrement confirms the `replied`
// counter is only bumped on successful ReplyToThread calls.
func TestReplyAndResolve_ReplyErrorsDoNotIncrement(t *testing.T) {
	installSeams(t)

	replyToThreadFn = func(context.Context, string, string) error { return errors.New("reply boom") }
	resolveThreadFn = func(context.Context, string) error { return nil }

	responses := []ThreadResponse{
		{ThreadID: "a", Action: "fixed", Comment: "x"},
		{ThreadID: "b", Action: "already_fixed", Comment: "y"},
	}
	replied, _, _ := replyAndResolve(context.Background(), responses, false, func(string, ...interface{}) {})
	if replied != 0 {
		t.Errorf("replied=%d want 0 when every ReplyToThread call errors", replied)
	}
}

// TestRunHook_ReturnsErrorOnFailure asserts that runHook propagates a
// non-nil error when the hook exits non-zero, so callers can log or surface it.
func TestRunHook_ReturnsErrorOnFailure(t *testing.T) {
	dir := t.TempDir()
	hook := dir + "/fail.sh"
	if err := writeExecHook(hook, "#!/bin/sh\nexit 7\n"); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := runHook(context.Background(), hook, dir); err == nil {
		t.Fatalf("runHook returned nil; want error for non-zero exit")
	}
}

// TestRunHook_NilOnSuccess confirms the success path still returns nil.
func TestRunHook_NilOnSuccess(t *testing.T) {
	dir := t.TempDir()
	hook := dir + "/ok.sh"
	if err := writeExecHook(hook, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := runHook(context.Background(), hook, dir); err != nil {
		t.Fatalf("runHook returned %v; want nil", err)
	}
}

// TestRunHook_ReturnsErrorWhenHookMissing confirms an error is returned when
// the script path doesn't exist (exec will fail to spawn).
func TestRunHook_ReturnsErrorWhenHookMissing(t *testing.T) {
	dir := t.TempDir()
	if err := runHook(context.Background(), dir+"/does-not-exist.sh", dir); err == nil {
		t.Fatalf("runHook returned nil; want error when binary missing")
	}
}
