package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/notify"
	"github.com/maszynka/gh-crfix/internal/registry"
	"github.com/maszynka/gh-crfix/internal/tui"
	"github.com/maszynka/gh-crfix/internal/workflow"
)

// captureStdout runs fn while redirecting os.Stdout to a pipe and returns
// whatever fn printed. It also captures os.Stderr and returns it as a second
// value so tests that need it can inspect warnings.
//
// It blocks until fn returns, the pipe is drained, and stdout/stderr are
// restored. Small helper — no fancy interleaving needed.
func captureStdout(t *testing.T, fn func()) (string, string) {
	t.Helper()

	origOut := os.Stdout
	origErr := os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stdout = wOut
	os.Stderr = wErr

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(rOut)
		outCh <- string(b)
	}()
	go func() {
		b, _ := io.ReadAll(rErr)
		errCh <- string(b)
	}()

	defer func() {
		_ = wOut.Close()
		_ = wErr.Close()
		os.Stdout = origOut
		os.Stderr = origErr
	}()

	fn()

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = origOut
	os.Stderr = origErr
	return <-outCh, <-errCh
}

// withSeams installs test doubles for the package-level test seams and
// restores the originals on t.Cleanup. Tests pass nil for fields they don't
// want to override.
type seamOverrides struct {
	isTerminal   func(*os.File) bool
	runLauncher  func(ctx context.Context, cfg config.Config, ml registry.ModelList) ([]string, bool)
	processBatch func(context.Context, workflow.BatchOptions) []workflow.Result
	runDashboard func(ctx context.Context, cfg tui.DashboardConfig) error
	currentRepo  func() (string, error)
	stdoutFile   func() *os.File
	stderrFile   func() *os.File
}

func withSeams(t *testing.T, o seamOverrides) {
	t.Helper()
	origIsTerminal := isTerminalFn
	origRunLauncher := runLauncherFn
	origProcessBatch := processBatchFn
	origRunDashboard := runDashboardFn
	origCurrentRepo := currentRepoFn
	origStdoutFile := stdoutFileFn
	origStderrFile := stderrFileFn
	if o.isTerminal != nil {
		isTerminalFn = o.isTerminal
	}
	if o.runLauncher != nil {
		runLauncherFn = o.runLauncher
	}
	if o.processBatch != nil {
		processBatchFn = o.processBatch
	}
	if o.runDashboard != nil {
		runDashboardFn = o.runDashboard
	}
	if o.currentRepo != nil {
		currentRepoFn = o.currentRepo
	}
	if o.stdoutFile != nil {
		stdoutFileFn = o.stdoutFile
	}
	if o.stderrFile != nil {
		stderrFileFn = o.stderrFile
	}
	t.Cleanup(func() {
		isTerminalFn = origIsTerminal
		runLauncherFn = origRunLauncher
		processBatchFn = origProcessBatch
		runDashboardFn = origRunDashboard
		currentRepoFn = origCurrentRepo
		stdoutFileFn = origStdoutFile
		stderrFileFn = origStderrFile
	})
}

// isolateHome points HOME and XDG_CONFIG_HOME at a t.TempDir so config load
// and last-run symlink creation don't pollute the real user dir.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// fakeOKResults builds a []workflow.Result with a single "ok" entry per PR.
// Used by tests that want processBatch to return synthetic results.
func fakeOKResults(prs []int) []workflow.Result {
	out := make([]workflow.Result, len(prs))
	for i, n := range prs {
		out[i] = workflow.Result{PRNum: n, Status: "ok", Title: "fake"}
	}
	return out
}

// ── Test 1 ──────────────────────────────────────────────────────────────────
// Launcher triggers when no args + TTY stdout+stderr. The launcher result is
// converted to CLI args and forwarded to resolveConfig+processBatch; we
// assert processBatch received the Target PR number.
func TestRun_LauncherTriggersWhenNoArgs_TTY(t *testing.T) {
	isolateHome(t)

	var launcherCalled atomic.Int32
	var processedPRs []int
	var mu sync.Mutex

	withSeams(t, seamOverrides{
		isTerminal: func(*os.File) bool { return true },
		runLauncher: func(ctx context.Context, cfg config.Config, ml registry.ModelList) ([]string, bool) {
			launcherCalled.Add(1)
			// Use URL to avoid currentRepo gh dependency; the spec only
			// requires that the launcher hand-off flows into processBatch.
			return []string{
				"https://github.com/acme/proj/pull/123",
				"--ai-backend", "claude",
				"--gate-model", "sonnet",
				"--fix-model", "opus",
				"-c", "1",
				"--score-needs-llm", "1.0",
				"--score-pr-comment", "0.4",
				"--score-test-failure", "1.0",
				"--no-notify",
				"--no-tui",
			}, true
		},
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			mu.Lock()
			processedPRs = append([]int{}, opts.PRNums...)
			mu.Unlock()
			return fakeOKResults(opts.PRNums)
		},
	})

	out, _ := captureStdout(t, func() {
		code := run(context.Background(), []string{})
		if code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})

	if launcherCalled.Load() != 1 {
		t.Errorf("launcher called %d times, want 1", launcherCalled.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(processedPRs) != 1 || processedPRs[0] != 123 {
		t.Errorf("processBatch saw PRs=%v, want [123]", processedPRs)
	}
	// The banner must mention the repo/PR that the launcher produced so we
	// know the args flowed through resolveConfig.
	if !strings.Contains(out, "acme/proj") {
		t.Errorf("stdout missing 'acme/proj'; got:\n%s", out)
	}
}

// ── Test 2 ──────────────────────────────────────────────────────────────────
// Launcher skipped when no TTY: usage is printed and processBatch is never
// called.
func TestRun_NoTTY_PrintsUsageNoLauncher(t *testing.T) {
	isolateHome(t)

	var launcherCalled atomic.Int32
	var processCalled atomic.Int32

	withSeams(t, seamOverrides{
		isTerminal: func(*os.File) bool { return false },
		runLauncher: func(context.Context, config.Config, registry.ModelList) ([]string, bool) {
			launcherCalled.Add(1)
			return nil, false
		},
		processBatch: func(context.Context, workflow.BatchOptions) []workflow.Result {
			processCalled.Add(1)
			return nil
		},
	})

	out, _ := captureStdout(t, func() {
		code := run(context.Background(), []string{})
		if code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})
	if launcherCalled.Load() != 0 {
		t.Errorf("launcher unexpectedly called %d times", launcherCalled.Load())
	}
	if processCalled.Load() != 0 {
		t.Errorf("processBatch unexpectedly called %d times", processCalled.Load())
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("stdout missing 'Usage:'; got:\n%s", out)
	}
}

// ── Test 3 ──────────────────────────────────────────────────────────────────
// Launcher cancel path: when RunLauncher returns Submitted=false, run exits
// 0 without calling processBatch.
func TestRun_LauncherCancel_SilentExit(t *testing.T) {
	isolateHome(t)

	var processCalled atomic.Int32
	withSeams(t, seamOverrides{
		isTerminal: func(*os.File) bool { return true },
		runLauncher: func(context.Context, config.Config, registry.ModelList) ([]string, bool) {
			return nil, false
		},
		processBatch: func(context.Context, workflow.BatchOptions) []workflow.Result {
			processCalled.Add(1)
			return nil
		},
	})

	_, _ = captureStdout(t, func() {
		if code := run(context.Background(), []string{}); code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})
	if processCalled.Load() != 0 {
		t.Errorf("processBatch called %d times on cancel, want 0", processCalled.Load())
	}
}

// ── Test 4 ──────────────────────────────────────────────────────────────────
// Dashboard triggers when concurrency > 1 + TTY. processBatch is seamed to
// sleep briefly so the dashboard has a chance to observe the live state.
func TestRun_DashboardTriggersOnConcurrencyTTY(t *testing.T) {
	isolateHome(t)

	var dashCalled atomic.Int32
	withSeams(t, seamOverrides{
		isTerminal:  func(*os.File) bool { return true },
		currentRepo: func() (string, error) { return "acme/proj", nil },
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			time.Sleep(20 * time.Millisecond)
			return fakeOKResults(opts.PRNums)
		},
		runDashboard: func(ctx context.Context, cfg tui.DashboardConfig) error {
			dashCalled.Add(1)
			// Return immediately; main.go will cancel and wait on us.
			return nil
		},
	})

	_, _ = captureStdout(t, func() {
		code := run(context.Background(), []string{"123,124", "-c", "2", "--no-notify"})
		if code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})

	if dashCalled.Load() != 1 {
		t.Errorf("dashboard called %d times, want 1", dashCalled.Load())
	}
}

// ── Test 5 ──────────────────────────────────────────────────────────────────
// --no-tui suppresses the dashboard even with concurrency>1 and a TTY.
func TestRun_NoTUIFlagSkipsDashboard(t *testing.T) {
	isolateHome(t)

	var dashCalled atomic.Int32
	withSeams(t, seamOverrides{
		isTerminal:  func(*os.File) bool { return true },
		currentRepo: func() (string, error) { return "acme/proj", nil },
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			return fakeOKResults(opts.PRNums)
		},
		runDashboard: func(ctx context.Context, cfg tui.DashboardConfig) error {
			dashCalled.Add(1)
			return nil
		},
	})

	_, _ = captureStdout(t, func() {
		code := run(context.Background(), []string{"123,124", "-c", "2", "--no-tui", "--no-notify"})
		if code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})
	if dashCalled.Load() != 0 {
		t.Errorf("dashboard called %d times with --no-tui, want 0", dashCalled.Load())
	}
}

// ── Test 6 ──────────────────────────────────────────────────────────────────
// Concurrency=1 (serial) skips the dashboard even with TTY.
func TestRun_Concurrency1_SkipsDashboard(t *testing.T) {
	isolateHome(t)

	var dashCalled atomic.Int32
	withSeams(t, seamOverrides{
		isTerminal:  func(*os.File) bool { return true },
		currentRepo: func() (string, error) { return "acme/proj", nil },
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			return fakeOKResults(opts.PRNums)
		},
		runDashboard: func(ctx context.Context, cfg tui.DashboardConfig) error {
			dashCalled.Add(1)
			return nil
		},
	})

	_, _ = captureStdout(t, func() {
		code := run(context.Background(), []string{"123", "-c", "1", "--no-notify"})
		if code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})
	if dashCalled.Load() != 0 {
		t.Errorf("dashboard called %d times with -c 1, want 0", dashCalled.Load())
	}
}

// ── Test 7 ──────────────────────────────────────────────────────────────────
// SIGINT mid-run returns 130. We simulate the signal by passing an
// already-cancelled context; run() checks ctx.Err() after the batch and
// converts a cancelled ctx into exit code 130.
func TestRun_CancelledContextReturns130(t *testing.T) {
	isolateHome(t)

	withSeams(t, seamOverrides{
		isTerminal:  func(*os.File) bool { return false }, // no-TTY, no dashboard
		currentRepo: func() (string, error) { return "acme/proj", nil },
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			// runBatchPlain checks ctx.Done() before calling this, but to be
			// safe, return placeholder results if ever reached.
			return fakeOKResults(opts.PRNums)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = captureStdout(t, func() {
		code := run(ctx, []string{"123", "--no-tui", "--no-notify"})
		if code != exitInterrupted {
			t.Errorf("run() = %d, want %d", code, exitInterrupted)
		}
	})
}

// ── Test 8 ──────────────────────────────────────────────────────────────────
// --help / --version fast paths print to stdout and return 0 without any
// other side effects.
func TestRun_HelpAndVersion_FastPaths(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"help long", []string{"--help"}, "Usage:"},
		{"help short", []string{"-h"}, "Usage:"},
		{"help bare", []string{"help"}, "Usage:"},
		{"version long", []string{"--version"}, "gh-crfix"},
		{"version short", []string{"-v"}, "gh-crfix"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Fast-path runs before any seam matters; no TTY / seams needed.
			out, _ := captureStdout(t, func() {
				if code := run(context.Background(), tc.args); code != 0 {
					t.Errorf("run(%v) = %d, want 0", tc.args, code)
				}
			})
			if !strings.Contains(out, tc.want) {
				t.Errorf("stdout missing %q; got:\n%s", tc.want, out)
			}
		})
	}
}

// ── Test 9 ──────────────────────────────────────────────────────────────────
// --no-notify propagates into notify.SetDisabled. We observe the effect by
// querying notify.isDisabled indirectly through SetDisabled(false) + Done's
// lookPath hook, but the simplest fully-visible check is to confirm the
// global flag is true after run returns.
func TestRun_NoNotifyFlag_DisablesNotify(t *testing.T) {
	isolateHome(t)

	// Reset to a known state and restore after the test.
	notify.SetDisabled(false)
	t.Cleanup(func() { notify.SetDisabled(false) })
	t.Setenv("GH_CRFIX_NO_NOTIFY", "")

	withSeams(t, seamOverrides{
		isTerminal:  func(*os.File) bool { return false },
		currentRepo: func() (string, error) { return "acme/proj", nil },
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			return fakeOKResults(opts.PRNums)
		},
	})

	_, _ = captureStdout(t, func() {
		if code := run(context.Background(), []string{"123", "--no-notify"}); code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})

	// The runtime flag should be true after run returns. We read it by
	// calling Done — with disabled=true it is a no-op regardless of platform
	// and completes instantly; we assert via a short-duration timer.
	done := make(chan struct{})
	start := time.Now()
	go func() {
		notify.Done("x", "y")
		close(done)
	}()
	select {
	case <-done:
		// Good — disabled means Done returns ~instantly.
		if time.Since(start) > 500*time.Millisecond {
			t.Errorf("notify.Done too slow (%v); SetDisabled may not have taken effect", time.Since(start))
		}
	case <-time.After(1 * time.Second):
		t.Error("notify.Done hung; --no-notify did not disable the notify package")
	}
}

// ── Test 10 ─────────────────────────────────────────────────────────────────
// applyFlags: config has gate_model=opus, --gate-model haiku wins.
func TestResolveConfig_FlagsOverrideGateModel(t *testing.T) {
	cfg := config.Defaults()
	cfg.GateModel = "opus"

	plan, err := resolveConfig(
		[]string{"https://github.com/a/b/pull/1", "--gate-model", "haiku"},
		cfg,
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if plan.opts.GateModel != "haiku" {
		t.Errorf("GateModel=%q, want haiku", plan.opts.GateModel)
	}
}

// ── Test 11 ─────────────────────────────────────────────────────────────────
// backendName maps all three ai.Backend values.
func TestBackendName_AllValues(t *testing.T) {
	cases := []struct {
		in   ai.Backend
		want string
	}{
		{ai.BackendClaude, "claude"},
		{ai.BackendCodex, "codex"},
		{ai.BackendAuto, "auto"},
	}
	for _, tc := range cases {
		if got := backendName(tc.in); got != tc.want {
			t.Errorf("backendName(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── Test 12 ─────────────────────────────────────────────────────────────────
// splitArgsAndFlags: positional target plus a mix of value-taking and boolean
// flags in various orders.
func TestSplitArgsAndFlags_Mixed(t *testing.T) {
	args := []string{
		"--dry-run",
		"https://github.com/a/b/pull/1",
		"--ai-backend", "claude",
		"42", // second positional — should be ignored, the first one wins
		"-c", "4",
		"--no-tui",
	}
	prSpec, flags, _ := splitArgsAndFlags(args)
	if prSpec != "https://github.com/a/b/pull/1" {
		t.Errorf("prSpec=%q, want the URL", prSpec)
	}
	// Flags should include --ai-backend + claude (consumed value), -c + 4,
	// --dry-run, --no-tui.
	joined := strings.Join(flags, " ")
	for _, want := range []string{"--ai-backend", "claude", "-c", "4", "--dry-run", "--no-tui"} {
		if !strings.Contains(joined, want) {
			t.Errorf("flags missing %q; got %v", want, flags)
		}
	}
	// The second positional "42" must not end up in flags (it's silently
	// dropped by splitArgsAndFlags).
	for _, f := range flags {
		if f == "42" {
			t.Errorf("second positional leaked into flags: %v", flags)
		}
	}
}

// ── Test 13 ─────────────────────────────────────────────────────────────────
// defaultConfigPath respects XDG_CONFIG_HOME and falls back to
// ~/.config/gh-crfix/defaults.
func TestDefaultConfigPath_XDGAndFallback(t *testing.T) {
	// Case 1: explicit XDG_CONFIG_HOME.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got := defaultConfigPath()
	want := filepath.Join(xdg, "gh-crfix", "defaults")
	if got != want {
		t.Errorf("with XDG: got %q, want %q", got, want)
	}

	// Case 2: XDG unset → $HOME/.config/gh-crfix/defaults.
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got = defaultConfigPath()
	want = filepath.Join(home, ".config", "gh-crfix", "defaults")
	if got != want {
		t.Errorf("fallback: got %q, want %q", got, want)
	}
}

// ── Test 14 ─────────────────────────────────────────────────────────────────
// applyWorkflowFlags covers the non-value-taking overlay paths plus the
// value-taking --review-wait and hook flags that aren't exercised by the
// existing resolveConfig tests.
func TestApplyWorkflowFlags_BooleanAndValueFlags(t *testing.T) {
	opts := workflow.Options{IncludeOutdated: true}
	flags := []string{
		"--dry-run",
		"--no-resolve",
		"--resolve-skipped",
		"--no-post-fix",
		"--no-autofix",
		"--setup-only",
		"--exclude-outdated",
		"--verbose",
		"--review-wait", "42",
		"--max-threads", "9",
		"--autofix-hook", "/tmp/fix.sh",
		"--validate-hook", "/tmp/val.sh",
	}
	applyWorkflowFlags(flags, &opts)

	if !opts.DryRun || !opts.NoResolve || !opts.ResolveSkipped ||
		!opts.NoPostFix || !opts.NoAutofix || !opts.SetupOnly ||
		!opts.Verbose {
		t.Errorf("boolean flags not applied: %+v", opts)
	}
	if opts.IncludeOutdated {
		t.Errorf("--exclude-outdated did not flip IncludeOutdated")
	}
	if opts.ReviewWaitSecs != 42 {
		t.Errorf("ReviewWaitSecs=%d, want 42", opts.ReviewWaitSecs)
	}
	if opts.MaxThreads != 9 {
		t.Errorf("MaxThreads=%d, want 9", opts.MaxThreads)
	}
	if opts.AutofixHook != "/tmp/fix.sh" {
		t.Errorf("AutofixHook=%q, want /tmp/fix.sh", opts.AutofixHook)
	}
	if opts.ValidateHook != "/tmp/val.sh" {
		t.Errorf("ValidateHook=%q, want /tmp/val.sh", opts.ValidateHook)
	}

	// --include-outdated flips it back.
	applyWorkflowFlags([]string{"--include-outdated"}, &opts)
	if !opts.IncludeOutdated {
		t.Errorf("--include-outdated did not restore IncludeOutdated")
	}
}

// ── Test 15 ─────────────────────────────────────────────────────────────────
// usage() writes a help string containing the known flags to stdout.
func TestUsage_WritesToStdout(t *testing.T) {
	out, _ := captureStdout(t, func() {
		usage()
	})
	for _, want := range []string{"Usage:", "--gate-model", "--no-tui", "GH_CRFIX_DIR"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage stdout missing %q", want)
		}
	}
}

// ── Test 16 ─────────────────────────────────────────────────────────────────
// trimTrailingZero renders a float with one decimal place.
func TestTrimTrailingZero(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.0, "0.0"},
		{1.0, "1.0"},
		{0.5, "0.5"},
		{0.75, "0.8"}, // %.1f rounds half-to-even at the fmt level
	}
	for _, tc := range cases {
		if got := trimTrailingZero(tc.in); got != tc.want {
			t.Errorf("trimTrailingZero(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── Test 17 ─────────────────────────────────────────────────────────────────
// isTerminal returns false for a pipe file and (we can't easily test true in
// `go test` because stdout is piped). Assert the false path.
func TestIsTerminal_FalseForPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) {
		t.Error("isTerminal(pipe) = true, want false")
	}
}

// ── Test 18 ─────────────────────────────────────────────────────────────────
// currentRepo returns an error when `gh` is absent or doesn't know the repo.
// We drop the PATH so exec.Command("gh", ...) fails with a lookup error.
func TestCurrentRepo_ErrorsWhenGHUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only path manipulation")
	}
	// Empty PATH → exec.LookPath("gh") fails → currentRepo returns a wrapped
	// error.
	t.Setenv("PATH", "")
	_, err := currentRepo()
	if err == nil {
		t.Error("currentRepo() with empty PATH: err=nil, want non-nil")
	}
}

// ── Test 19 ─────────────────────────────────────────────────────────────────
// run with args that can't be parsed returns exit 1 and prints an error.
// This exercises the error-propagation path in run() that the happy-path
// tests skip over.
func TestRun_BadTargetReturnsErrExit(t *testing.T) {
	isolateHome(t)

	withSeams(t, seamOverrides{
		isTerminal: func(*os.File) bool { return false },
		currentRepo: func() (string, error) {
			// Bare input forces currentRepo; returning an error forces the
			// wrapped error path inside resolveConfig.
			return "", io.ErrUnexpectedEOF
		},
	})

	// Capture stderr — our error message lands there.
	var stderrBuf bytes.Buffer
	origErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	doneCh := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrBuf, r)
		close(doneCh)
	}()

	code := run(context.Background(), []string{"totally-not-a-pr", "--no-notify"})
	_ = w.Close()
	os.Stderr = origErr
	<-doneCh

	if code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderrBuf.String(), "error:") {
		t.Errorf("stderr missing 'error:' prefix; got %q", stderrBuf.String())
	}
}

// ── Test 20 ─────────────────────────────────────────────────────────────────
// runBatchPlain returns placeholder "skipped" results when ctx is already
// cancelled before the batch starts — exercised independently from run().
func TestRunBatchPlain_CancelledCtxProducesSkipped(t *testing.T) {
	plan := runPlan{prNums: []int{1, 2, 3}, concurrency: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := runBatchPlain(ctx, plan)
	if len(results) != 3 {
		t.Fatalf("results len=%d, want 3", len(results))
	}
	for i, r := range results {
		if r.Status != "skipped" {
			t.Errorf("results[%d].Status=%q, want skipped", i, r.Status)
		}
		if r.Reason == "" {
			t.Errorf("results[%d].Reason is empty", i)
		}
	}
}

// ── Test 21 ─────────────────────────────────────────────────────────────────
// run forwards the GH_CRFIX_NO_NOTIFY=1 env to notify.SetDisabled before
// dispatching, matching the bash script's behaviour.
func TestRun_EnvNoNotifyTriggersDisable(t *testing.T) {
	isolateHome(t)

	notify.SetDisabled(false)
	t.Cleanup(func() { notify.SetDisabled(false) })
	t.Setenv("GH_CRFIX_NO_NOTIFY", "1")

	withSeams(t, seamOverrides{
		isTerminal:  func(*os.File) bool { return false },
		currentRepo: func() (string, error) { return "acme/proj", nil },
		processBatch: func(_ context.Context, opts workflow.BatchOptions) []workflow.Result {
			return fakeOKResults(opts.PRNums)
		},
	})

	_, _ = captureStdout(t, func() {
		if code := run(context.Background(), []string{"1"}); code != 0 {
			t.Errorf("run() = %d, want 0", code)
		}
	})

	// The env-check path inside notify should keep Done() a no-op. We can't
	// assert the internal state directly without exporting it, but we can
	// confirm Done returns ~instantly.
	done := make(chan struct{})
	go func() {
		notify.Done("x", "y")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Error("notify.Done hung despite GH_CRFIX_NO_NOTIFY=1")
	}
}
