// Package notify posts best-effort "done" notifications.
//
// On macOS it invokes osascript's display-notification; on other platforms
// it falls back to writing a BEL (\a) to stderr, matching the behaviour of
// the original bash script. Failures are swallowed — notifications are an
// amenity, not a requirement.
package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"
)

// Swappable package-level hooks so tests can inject fakes without spawning
// real subprocesses. Production code uses exec.Command / exec.LookPath.
var (
	executor = exec.Command
	lookPath = exec.LookPath
)

// disabled gates all notification attempts process-wide.
var disabled atomic.Bool

// doneTimeout bounds how long Done may block the caller.
const doneTimeout = 2 * time.Second

// osascriptTimeout bounds the background osascript invocation. It outlives
// Done (osascript runs in its own goroutine) but is still kept finite so a
// hung osascript eventually gets killed.
const osascriptTimeout = 10 * time.Second

// SetDisabled toggles notifications process-wide. When true, Done is a
// no-op. Useful for tests, CI, and headless environments. The environment
// variable GH_CRFIX_NO_NOTIFY=1 has the same effect on a per-call basis.
func SetDisabled(v bool) {
	disabled.Store(v)
}

// isDisabled reports whether notifications are currently suppressed.
func isDisabled() bool {
	if disabled.Load() {
		return true
	}
	if v := os.Getenv("GH_CRFIX_NO_NOTIFY"); v == "1" {
		return true
	}
	return false
}

// Done posts a "done" notification with the given title and body. It is
// best-effort and never errors; on failure it silently falls back. Done
// is guaranteed to return within ~2s regardless of how the underlying
// notifier behaves.
func Done(title, body string) {
	if isDisabled() {
		return
	}

	if runtime.GOOS == "darwin" {
		doneDarwin(title, body)
		return
	}
	doneFallback()
}

// doneDarwin tries osascript asynchronously, bounded by osascriptTimeout.
// Done returns as soon as the subprocess is launched (or immediately if
// osascript isn't on PATH), never blocking the caller beyond doneTimeout.
func doneDarwin(title, body string) {
	path, err := lookPath("osascript")
	if err != nil || path == "" {
		// Fallback to BEL so the user still gets *some* cue.
		doneFallback()
		return
	}

	// Compose the AppleScript. We intentionally do not shell-escape with
	// %q here because osascript -e takes an exact argv string, not a
	// shell-parsed string. The single worry is embedded double quotes in
	// the user-supplied title/body; escape them.
	script := fmt.Sprintf(
		`display notification "%s" with title "%s" sound name "Glass"`,
		escapeAppleScript(body),
		escapeAppleScript(title),
	)

	ctx, cancel := context.WithTimeout(context.Background(), osascriptTimeout)
	started := make(chan struct{})

	go func() {
		defer cancel()
		defer func() { _ = recover() }()

		cmd := executor(path, "-e", script)
		// Detach stdio so we don't accidentally block on pipes.
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil

		if err := cmd.Start(); err != nil {
			close(started)
			return
		}
		close(started)

		// Wait for the subprocess, but don't let it outlive the ctx.
		waitErr := make(chan error, 1)
		go func() { waitErr <- cmd.Wait() }()
		select {
		case <-waitErr:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-waitErr
		}
	}()

	// Bound our own wait on "subprocess launched" so Done itself never
	// blocks longer than doneTimeout. Even if Start() is somehow slow,
	// we hand back control.
	select {
	case <-started:
	case <-time.After(doneTimeout):
	}
}

// doneFallback writes a single BEL to stderr. Cheap and synchronous.
func doneFallback() {
	_, _ = os.Stderr.Write([]byte{'\a'})
}

// escapeAppleScript escapes embedded double quotes and backslashes so the
// composed AppleScript string literal remains well-formed.
func escapeAppleScript(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
