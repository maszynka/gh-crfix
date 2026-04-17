package notify

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// withStderr swaps os.Stderr with a pipe for the duration of fn and returns
// everything written to stderr during the call.
func withStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	w.Close()
	os.Stderr = orig
	return <-done
}

func TestDone_NonBlocking(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript-stub test only meaningful on darwin")
	}

	// Replace executor with one that simulates a slow osascript.
	orig := executor
	defer func() { executor = orig }()
	executor = func(name string, args ...string) *exec.Cmd {
		// /bin/sleep 30 emulates a hung osascript.
		return exec.Command("/bin/sleep", "30")
	}

	// Also force LookPath to succeed on darwin.
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()
	lookPath = func(name string) (string, error) {
		return "/usr/bin/osascript", nil
	}

	start := time.Now()
	Done("title", "body")
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("Done blocked for %v; want <= 2s", elapsed)
	}
}

func TestDone_DisabledViaSetDisabled(t *testing.T) {
	orig := executor
	defer func() { executor = orig }()

	var invoked atomic.Int32
	executor = func(name string, args ...string) *exec.Cmd {
		invoked.Add(1)
		// Return a no-op command; we only care that we counted invocation.
		return exec.Command("/bin/sh", "-c", "true")
	}

	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()
	lookPath = func(name string) (string, error) { return "/usr/bin/osascript", nil }

	SetDisabled(true)
	defer SetDisabled(false)

	// Capture any stderr to avoid noise during the test; BEL should not fire either.
	out := withStderr(t, func() {
		Done("title", "body")
		// Give any (incorrect) goroutine a chance to run.
		time.Sleep(100 * time.Millisecond)
	})

	if invoked.Load() != 0 {
		t.Errorf("executor invoked %d times while disabled; want 0", invoked.Load())
	}
	if out != "" {
		t.Errorf("stderr = %q; want empty when disabled", out)
	}
}

func TestDone_DisabledViaEnv(t *testing.T) {
	orig := executor
	defer func() { executor = orig }()

	var invoked atomic.Int32
	executor = func(name string, args ...string) *exec.Cmd {
		invoked.Add(1)
		return exec.Command("/bin/sh", "-c", "true")
	}

	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()
	lookPath = func(name string) (string, error) { return "/usr/bin/osascript", nil }

	t.Setenv("GH_CRFIX_NO_NOTIFY", "1")

	out := withStderr(t, func() {
		Done("title", "body")
		time.Sleep(100 * time.Millisecond)
	})

	if invoked.Load() != 0 {
		t.Errorf("executor invoked %d times while GH_CRFIX_NO_NOTIFY=1; want 0", invoked.Load())
	}
	if out != "" {
		t.Errorf("stderr = %q; want empty when disabled via env", out)
	}
}

func TestDone_BELFallback(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("BEL fallback is for non-darwin platforms")
	}

	// Ensure no global disable is in effect.
	SetDisabled(false)
	os.Unsetenv("GH_CRFIX_NO_NOTIFY")

	out := withStderr(t, func() {
		Done("title", "body")
		// Allow any goroutine to flush.
		time.Sleep(100 * time.Millisecond)
	})

	if out == "" || out[0] != '\a' {
		t.Errorf("stderr = %q; want it to start with BEL (\\a)", out)
	}
}
