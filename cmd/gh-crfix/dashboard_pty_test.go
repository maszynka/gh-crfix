//go:build e2e

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestE2E_Dashboard_PTY_NoStairStep_NoSubprocessLeak builds the binary and
// drives it through a real PTY with concurrency≥2 so the dashboard kicks in.
// It plants a noisy autofix hook in the feat-test branch that dumps a unique
// marker on stderr a hundred times. The bug the user reported was that this
// kind of subprocess output leaks onto the raw-mode TTY and stair-steps
// because the parent's os.Stderr (and hence the subprocess's inherited
// stderr) is the bubbletea-managed terminal in raw mode.
//
// After the fix:
//   - dashboard enters the alt screen (\x1b[?1049h must appear)
//   - subprocess marker MUST NOT appear in PTY output (it should land in
//     the master log instead, where the dashboard tails it on demand)
func TestE2E_Dashboard_PTY_NoStairStep_NoSubprocessLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}

	env := newE2EEnv(t)
	writeStubGh(t, env.stubDir)

	// Slow claude stub: sleeps 4s during the gate call so the dashboard
	// has time to render frames before the batch worker finishes. With
	// the production-fast stubs the batch was completing in ~500ms, faster
	// than bubbletea could even emit its alt-screen enter sequence.
	slowClaude := `#!/bin/sh
set -e
for arg in "$@"; do
  if [ "$arg" = "--json-schema" ]; then
    sleep 4
    printf '{"structured_output":{"needs_advanced_model":true,"reason":"e2e-fake","threads_to_fix":["PRRT_test"]}}\n'
    exit 0
  fi
done
cat > thread-responses.json <<'JSON'
[
  {"thread_id":"PRRT_test","action":"fixed","comment":"e2e fake fix"}
]
JSON
exit 0
`
	if err := os.WriteFile(filepath.Join(env.stubDir, "claude"), []byte(slowClaude), 0o755); err != nil {
		t.Fatalf("write slow claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.stubDir, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write codex: %v", err)
	}

	// Pre-seed the config file so the binary doesn't drop into the
	// interactive first-run setup wizard before the dashboard.
	cfgDir := filepath.Join(env.home, ".config", "gh-crfix")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "defaults"),
		[]byte("WORKTREE_MODE=temp\n"), 0o644); err != nil {
		t.Fatalf("write defaults: %v", err)
	}

	// Plant a noisy autofix hook on the feat-test branch so the worktree
	// the dashboard sets up will pick it up and run it during the process
	// phase. The hook dumps a marker on stderr — exactly the behaviour
	// that produced the user's broken screen (bun test output on stderr).
	const leakMarker = "SUBPROCESS_LEAK_MARKER"
	hookDir := filepath.Join(env.repoDir, ".gh-crfix")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hookDir: %v", err)
	}
	hookBody := "#!/bin/sh\n" +
		"for i in $(seq 1 50); do\n" +
		"  printf '" + leakMarker + " line %d\\n' \"$i\" 1>&2\n" +
		"done\n"
	hookPath := filepath.Join(hookDir, "autofix.sh")
	if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	runOrFail(t, env.repoDir, "git", "checkout", "-q", "feat-test")
	runOrFail(t, env.repoDir, "git", "add", ".gh-crfix/autofix.sh")
	runOrFail(t, env.repoDir, "git", "commit", "-q", "-m", "test: noisy autofix hook")
	runOrFail(t, env.repoDir, "git", "push", "-q", "origin", "feat-test")
	runOrFail(t, env.repoDir, "git", "checkout", "-q", "main")

	// Spawn the binary in a PTY. concurrency=2 (with one PR) is enough to
	// satisfy useDashboard's `concurrency > 1` gate.
	cmd := exec.Command(env.binPath,
		"https://github.com/acme/proj/pull/101",
		"-c", "2",
		"--dry-run",
		"--no-notify",
		"--no-post-fix",
	)
	cmd.Env = env.envList()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	defer ptmx.Close()

	// Drain output asynchronously so the PTY buffer doesn't fill up.
	outCh := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, ptmx)
		outCh <- buf.Bytes()
	}()

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	// Give the dashboard time to render and the autofix hook time to dump
	// its noise into the (now-redirected) log, then send ctrl+c so the
	// program tears down cleanly via signal handler. Sending 'q' through
	// the PTY is unreliable when bubbletea's input reader hasn't fully
	// engaged on the test host.
	// Wait for the batch to finish naturally — the slow claude stub gives
	// the dashboard ~4s to render before main exits and tells the dashboard
	// to quit. No need for a kill or a SIGINT.
	select {
	case <-doneCh:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("e2e: batch did not finish in 30s")
	}
	// Closing the PTY unblocks the io.Copy goroutine.
	_ = ptmx.Close()
	captured := <-outCh
	t.Logf("captured %d bytes:\n%s", len(captured), truncForLog(captured, 4096))

	// --- Assertion 1: dashboard actually rendered (alt-screen entered) -------
	const altScreenEnter = "\x1b[?1049h"
	if !bytes.Contains(captured, []byte(altScreenEnter)) {
		t.Errorf("dashboard did not enter alt screen — bubbletea WithAltScreen() missing? captured %d bytes:\n%s",
			len(captured), captured)
	}

	// --- Assertion 2: subprocess output never leaked to the PTY --------------
	if bytes.Contains(captured, []byte(leakMarker)) {
		// Quote a representative slice for a useful failure message.
		t.Errorf("subprocess output leaked into the user's terminal — must be routed to master log instead. PTY contained %q. Captured (first 2KB):\n%s",
			leakMarker, truncForLog(captured, 2048))
	}
}

// truncForLog clips a byte slice to n bytes for terse t.Logf output.
func truncForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "\n... (truncated, " + strings.Repeat("", 0) + itoa(len(b)-n) + " more bytes)"
}

func itoa(n int) string {
	// Tiny helper so we don't pull in strconv just for this.
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
