// gh-crfix — GitHub PR review fixer (Go port).
//
// main.go is the integration layer: it wires signals, config, the launcher
// TUI, the dashboard TUI, the workflow batch runner, and the done-notifier
// together. It is the only file in the repo that directly imports all of
// those packages.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/gate"
	"github.com/maszynka/gh-crfix/internal/input"
	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/model"
	"github.com/maszynka/gh-crfix/internal/notify"
	"github.com/maszynka/gh-crfix/internal/progress"
	"github.com/maszynka/gh-crfix/internal/registry"
	"github.com/maszynka/gh-crfix/internal/shutdown"
	"github.com/maszynka/gh-crfix/internal/tui"
	"github.com/maszynka/gh-crfix/internal/workflow"
)

const version = "0.1.0-go"

// exitInterrupted matches the canonical UNIX 130 exit code for a SIGINT'd
// program.
const exitInterrupted = 130

func main() {
	// Install the signal-cancellable root context before any heavy work.
	// A second SIGINT after cancellation is fine: we still exit 130 via
	// the ctx.Err() check in run().
	ctx, stop := shutdown.WithSignals(context.Background())
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

// runPlan is the resolved configuration for a single invocation of gh-crfix.
// It is produced by resolveConfig from CLI args + persisted config, and
// consumed by the batch runner.
type runPlan struct {
	ownerRepo   string
	prNums      []int
	opts        workflow.Options
	concurrency int
	noTUI       bool
	noNotify    bool
}

func run(ctx context.Context, args []string) int {
	// --- Fast-path flags ------------------------------------------------------
	wantSetup := false
	for _, a := range args {
		switch a {
		case "--version", "-v":
			fmt.Printf("gh-crfix %s (Go port)\n", version)
			return 0
		case "--help", "-h", "help":
			usage()
			return 0
		case "--setup", "-s":
			wantSetup = true
		}
	}

	// --- Load persisted defaults ---------------------------------------------
	cfgPath := defaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load config: %v\n", err)
		cfg = config.Defaults()
	}

	// --- First-run / explicit setup wizard -----------------------------------
	// Auto-trigger on first invocation (no config file yet) when both std
	// streams are TTYs. Skip in non-TTY contexts (CI, pipes) so automation
	// doesn't hang on stdin.
	stdinIsTTY := isTerminalFn(os.Stdin)
	stdoutIsTTY := isTerminalFn(stdoutFileFn())
	if wantSetup || (firstRunNeeded(cfgPath) && stdinIsTTY && stdoutIsTTY) {
		newCfg, werr := runSetupWizard(os.Stdin, os.Stdout, cfg, cfgPath)
		if werr != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", werr)
			return 1
		}
		cfg = newCfg
		if wantSetup {
			// Explicit re-config: don't try to also run a PR in the same
			// invocation. The user can re-run with a PR target now.
			return 0
		}
	}

	// Environment overrides mirror the bash script's behaviour. The registry
	// URL is consumed inside registry.Fetch; review-wait is picked up in
	// resolveConfig; notify disable is handled both here and inside the
	// notify package (which re-reads the env on every call).
	if os.Getenv("GH_CRFIX_NO_NOTIFY") == "1" {
		notify.SetDisabled(true)
	}

	// --- If no args AND both std streams are TTYs → launcher ------------------
	if len(args) == 0 {
		if isTerminalFn(stdoutFileFn()) && isTerminalFn(stderrFileFn()) {
			// Load the model registry (best-effort — the launcher handles an
			// empty ModelList gracefully via its own fallbacks).
			ml, _ := registry.Fetch(registry.Options{})
			launcherArgs, ok := runLauncherFn(ctx, cfg, ml)
			if !ok {
				// Launcher cancelled (ctrl+C / esc / q) — silent exit.
				return 0
			}
			// The launcher itself persists defaults when the user hits 's'
			// before submit; main doesn't need to double-save here.
			args = launcherArgs
		} else {
			usage()
			return 0
		}
	}

	plan, err := resolveConfig(args, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Trigger model.Family validation side-effects the old code relied on
	// (keeps parity with the previous main).
	_ = model.Family(cfg.GateModel)
	_ = model.Family(cfg.FixModel)

	// --- Banner --------------------------------------------------------------
	fmt.Printf("gh-crfix %s — Go port\n\n", version)
	fmt.Printf("  repo        : %s\n", plan.ownerRepo)
	fmt.Printf("  PRs         : %v\n", plan.prNums)
	fmt.Printf("  backend     : %s\n", backendName(plan.opts.AIBackend))
	fmt.Printf("  gate model  : %s\n", plan.opts.GateModel)
	fmt.Printf("  fix model   : %s\n", plan.opts.FixModel)
	fmt.Printf("  concurrency : %d\n", plan.concurrency)
	fmt.Printf("  scores      : needs_llm=%.3f  pr_comment=%.3f  test_failure=%.3f\n",
		plan.opts.Weights.NeedsLLM, plan.opts.Weights.PRComment, plan.opts.Weights.TestFailure)
	fmt.Println()

	// Backend auto-detection. Model family wins over executable presence:
	// if the user left the backend on "auto" but configured Codex models (or
	// Claude models), respect that rather than routing e.g. gpt-5.4 to the
	// claude CLI and watching the gate phase fail.
	if plan.opts.AIBackend == ai.BackendAuto {
		detected := backendFromModelFamily(plan.opts.GateModel, plan.opts.FixModel)
		if detected == ai.BackendAuto {
			detected = ai.Detect()
		}
		plan.opts.AIBackend = detected
		switch detected {
		case ai.BackendClaude:
			fmt.Println("  backend auto-detected: claude")
		case ai.BackendCodex:
			fmt.Println("  backend auto-detected: codex")
		default:
			fmt.Fprintln(os.Stderr, "warning: no AI backend found (install claude or codex)")
		}
		fmt.Println()
	}

	// --- Create logs.Run + progress.Tracker once, so TUI + batch share them --
	runLog, err := logs.NewRun()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create log run: %v\n", err)
	}
	var tracker *progress.Tracker
	if runLog != nil {
		tracker = progress.NewTracker(filepath.Join(runLog.Dir(), "progress"))
		for _, n := range plan.prNums {
			_ = tracker.Init(n)
		}
		defer runLog.Close()
	}
	plan.opts.Run = runLog
	plan.opts.Tracker = tracker

	// Propagate no-notify to the notify package so Done() is a no-op.
	if plan.noNotify {
		notify.SetDisabled(true)
	}

	// --- Decide whether to run the dashboard alongside ProcessBatch ----------
	useDashboard := !plan.noTUI &&
		plan.concurrency > 1 &&
		isTerminalFn(stdoutFileFn()) &&
		runLog != nil &&
		tracker != nil

	results := runBatch(ctx, plan, runLog, tracker, useDashboard)

	workflow.PrintResults(os.Stdout, results)

	okN, skipN, failN := 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case "ok":
			okN++
		case "skipped":
			skipN++
		default:
			failN++
		}
	}

	// --- Best-effort done notification ---------------------------------------
	notify.Done(
		"gh crfix done",
		fmt.Sprintf("%d fixed, %d skipped, %d failed", okN, skipN, failN),
	)

	// SIGINT during the run: ctx.Err() is non-nil even if the batch finished
	// normally after the signal was delivered; force exit 130 in that case so
	// callers can distinguish interrupted from clean runs.
	if errors.Is(ctx.Err(), context.Canceled) {
		return exitInterrupted
	}
	if failN > 0 {
		return 1
	}
	return 0
}

// runBatch drives ProcessBatch to completion, optionally running the dashboard
// TUI in parallel. When the dashboard is active, per-PR stdout is redirected
// to the master log file for the duration so the two don't fight for the
// terminal; see the design note at the bottom of this file.
func runBatch(
	ctx context.Context,
	plan runPlan,
	runLog *logs.Run,
	tracker *progress.Tracker,
	useDashboard bool,
) []workflow.Result {

	if !useDashboard {
		// Plain-text mode: setup-phase progress lines should appear on stdout
		// so users aren't staring at a silent terminal during a 30s git fetch.
		plan.opts.ProgressOut = os.Stdout
		return runBatchPlain(ctx, plan)
	}

	// Launch the dashboard in a goroutine. When the user hits 'q' or ctrl+c
	// the dashboard returns and we cancel the batch ctx.
	dashCtx, dashCancel := context.WithCancel(ctx)
	defer dashCancel()

	// Dashboard mode: a one-liner on stderr before the framebuffer takes over
	// so the user knows something is happening during setup. stderr isn't
	// redirected to the log file the way stdout is below.
	fmt.Fprintf(os.Stderr, "Setting up %d PR(s)...\n", len(plan.prNums))
	// Per-setupOnePR progress goes to stderr too (not stdout, which we're
	// about to redirect into the master log). These lines will interleave
	// with the dashboard briefly during setup, but they land on stderr which
	// bubbletea's framebuffer doesn't touch — they'll scroll above the
	// dashboard at worst. Better than 30s of silence.
	plan.opts.ProgressOut = os.Stderr

	// Silence per-PR stdout during dashboard so the two surfaces don't
	// overlap. All those writes go to the master log via the usual tee in
	// logs.Run, so nothing is lost — only the live terminal mirror is
	// suppressed. See design note below.
	origStdout := os.Stdout
	devNullOrLog, _ := os.OpenFile(runLog.MasterLog(),
		os.O_WRONLY|os.O_APPEND, 0o644)
	if devNullOrLog == nil {
		devNullOrLog, _ = os.Open(os.DevNull)
	}
	os.Stdout = devNullOrLog
	defer func() {
		os.Stdout = origStdout
		if devNullOrLog != nil {
			_ = devNullOrLog.Close()
		}
	}()

	var dashWg sync.WaitGroup
	dashWg.Add(1)
	go func() {
		defer dashWg.Done()
		_ = runDashboardFn(dashCtx, tui.DashboardConfig{
			PRNums:  plan.prNums,
			Tracker: tracker,
			Run:     runLog,
			Refresh: 250 * time.Millisecond,
		})
		// Dashboard exited (user pressed q or ctx done) — cancel so the
		// batch worker also winds up.
		dashCancel()
	}()

	results := runBatchPlain(dashCtx, plan)

	// Batch is done; tell the dashboard to quit and wait for it to unwind.
	dashCancel()
	dashWg.Wait()
	return results
}

// runBatchPlain invokes workflow.ProcessBatch. The context is checked after
// it returns so an interrupt that arrives mid-batch is surfaced as a
// non-zero exit code without aborting the summary print.
func runBatchPlain(ctx context.Context, plan runPlan) []workflow.Result {
	// ProcessBatch doesn't take a context yet (preserving its signature is a
	// hard constraint), but it does honor the Run/Tracker we plumbed in.
	// Respect ctx by short-circuiting into a synthetic "cancelled" result set
	// when the caller already cancelled before the batch starts.
	select {
	case <-ctx.Done():
		// Produce placeholder results so PrintResults has something to show.
		results := make([]workflow.Result, len(plan.prNums))
		for i, n := range plan.prNums {
			results[i] = workflow.Result{
				PRNum:  n,
				Status: "skipped",
				Reason: "interrupted before start",
			}
		}
		return results
	default:
	}
	return processBatchFn(ctx, workflow.BatchOptions{
		PRNums:      plan.prNums,
		Concurrency: plan.concurrency,
		Base:        plan.opts,
		Out:         os.Stdout,
	})
}

// resolveConfig turns CLI args + persisted config into a concrete runPlan.
// It is the single source of truth for how flags override config values so
// both the launcher-handoff path and the direct-CLI path share the same
// semantics.
func resolveConfig(args []string, cfg config.Config) (runPlan, error) {
	plan := runPlan{}

	prSpec, flags, unknown := splitArgsAndFlags(args)
	if len(unknown) > 0 {
		return plan, fmt.Errorf("unknown flag(s): %s — run `gh crfix --help`",
			strings.Join(unknown, " "))
	}

	// Precedence matches the bash script: flag > env > file > default.
	// applyEnvToConfig fills in anything GH_CRFIX_* declares; applyFlags then
	// layers CLI flags on top so explicit flags always win over env values.
	applyEnvToConfig(&cfg)
	applyFlags(flags, &cfg)

	// Parse PR spec.
	var ownerRepo string
	var prNums []int
	var err error
	if prSpec == "" {
		return plan, fmt.Errorf("missing target: pass a PR number, range, list, or URL")
	}
	if strings.Contains(prSpec, "github.com/") {
		ownerRepo, prNums, err = input.ParseURL(prSpec)
	} else {
		ownerRepo, err = currentRepoFn()
		if err != nil {
			return plan, fmt.Errorf("not in a GitHub repo and no URL given (%w)", err)
		}
		prNums, err = input.ParseBare(prSpec)
	}
	if err != nil {
		return plan, err
	}

	opts := workflow.OptionsFromConfig(cfg, ownerRepo, 0)
	opts.RepoRoot = os.Getenv("GH_CRFIX_DIR")

	// Apply env-var overrides to opts (bash precedence: env > file > default).
	// Flags are applied AFTER this so --review-wait beats GH_CRFIX_REVIEW_WAIT.
	if v := os.Getenv("GH_CRFIX_REVIEW_WAIT"); v != "" {
		var n int
		if _, serr := fmt.Sscanf(v, "%d", &n); serr == nil && n >= 0 {
			opts.ReviewWaitSecs = n
		}
	}

	applyWorkflowFlags(flags, &opts)

	// Score weights come from Config (possibly CLI-overridden above). Mirror
	// them into the gate.ScoreWeights struct OptionsFromConfig built for us,
	// so future flag additions flow through cleanly.
	opts.Weights = gate.ScoreWeights{
		NeedsLLM:    cfg.ScoreNeedsLLM,
		PRComment:   cfg.ScorePRComment,
		TestFailure: cfg.ScoreTestFailure,
	}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--score-needs-llm":
			if i+1 < len(flags) {
				i++
				if v, ok := parseScoreFlag(flags[i]); ok {
					opts.Weights.NeedsLLM = v
				}
			}
		case "--score-pr-comment":
			if i+1 < len(flags) {
				i++
				if v, ok := parseScoreFlag(flags[i]); ok {
					opts.Weights.PRComment = v
				}
			}
		case "--score-test-failure":
			if i+1 < len(flags) {
				i++
				if v, ok := parseScoreFlag(flags[i]); ok {
					opts.Weights.TestFailure = v
				}
			}
		}
	}

	// Collect the toplevel-only flags.
	noTUI, noNotify := false, false
	seq := false
	for _, f := range flags {
		switch f {
		case "--no-tui":
			noTUI = true
		case "--no-notify":
			noNotify = true
		case "--seq":
			seq = true
		}
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if seq {
		concurrency = 1
	}

	plan.ownerRepo = ownerRepo
	plan.prNums = prNums
	plan.opts = opts
	plan.concurrency = concurrency
	plan.noTUI = noTUI
	plan.noNotify = noNotify
	return plan, nil
}

// runLauncher shows the interactive form and, on success, returns a synthetic
// []string of CLI args that produces the same runPlan as if the user had typed
// them. This keeps launcher and CLI paths sharing resolveConfig.
//
// Returns (args, submitted). Submitted=false means the user cancelled.
func runLauncher(ctx context.Context, cfg config.Config, ml registry.ModelList) ([]string, bool) {
	res, err := tui.RunLauncher(ctx, tui.LauncherConfig{
		Initial: cfg,
		Models:  ml,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "launcher: %v\n", err)
		return nil, false
	}
	if !res.Submitted {
		return nil, false
	}
	args := []string{
		res.Target,
		"--ai-backend", res.Backend,
		"--gate-model", res.GateModel,
		"--fix-model", res.FixModel,
		"-c", fmt.Sprintf("%d", res.Concurrency),
		"--score-needs-llm", trimTrailingZero(res.ScoreNeedsLLM),
		"--score-pr-comment", trimTrailingZero(res.ScorePRComment),
		"--score-test-failure", trimTrailingZero(res.ScoreTestFailure),
	}
	return args, true
}

// trimTrailingZero renders a 0-to-1 score weight with one decimal place,
// matching the launcher display (`0.5`, `1.0`, `0.0`).
func trimTrailingZero(v float64) string {
	return fmt.Sprintf("%.1f", v)
}

// isTerminal reports whether f is attached to a TTY. Used to decide whether
// to launch the interactive form or the dashboard.
//
// Implementation note: we avoid adding golang.org/x/term as a direct dep
// (per the hard constraint "no new external deps beyond what's already in
// go.mod"). os.File.Stat + ModeCharDevice is good enough for the two call
// sites — pipes and regular files produce a non-zero ModeNamedPipe/ModeDir,
// so the combined check is conservative.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// splitArgsAndFlags separates the first positional argument from flags. It
// also returns any unknown-looking flag tokens so the caller can reject them
// before mutating state. Typos like `--dryrun` (instead of `--dry-run`)
// previously slid through silently and could skip safety guards at runtime.
func splitArgsAndFlags(args []string) (prSpec string, flags, unknown []string) {
	valueFlags := map[string]bool{
		"-c": true, "--concurrency": true, "--ai-backend": true,
		"--gate-model": true, "--fix-model": true,
		"--score-needs-llm": true, "--score-pr-comment": true, "--score-test-failure": true,
		"--max-threads": true, "--autofix-hook": true, "--validate-hook": true,
		"--review-wait": true, "--worktree-mode": true,
	}
	booleanFlags := map[string]bool{
		"--dry-run": true, "--no-resolve": true, "--resolve-skipped": true,
		"--no-post-fix": true, "--no-autofix": true, "--no-validate": true,
		"--setup-only": true, "--exclude-outdated": true, "--include-outdated": true,
		"--verbose": true, "--no-tui": true, "--no-notify": true,
		"--version": true, "-v": true, "--help": true, "-h": true, "--seq": true,
		"--setup": true, "-s": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if !valueFlags[a] && !booleanFlags[a] {
				unknown = append(unknown, a)
				continue
			}
			flags = append(flags, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else if prSpec == "" {
			prSpec = a
		}
	}
	return
}

// applyEnvToConfig overlays GH_CRFIX_* environment variables on top of the
// persisted config. These mirror the exports the bash script honors (see
// `gh-crfix` around lines 60–90). Invalid values are ignored — never fatal —
// so a typo in the environment doesn't break a run. Flags (applyFlags) are
// applied AFTER this so CLI flags still take precedence over env.
func applyEnvToConfig(cfg *config.Config) {
	if v := os.Getenv("GH_CRFIX_AI_BACKEND"); v != "" {
		cfg.AIBackend = v
	}
	if v := os.Getenv("GH_CRFIX_GATE_MODEL"); v != "" {
		cfg.GateModel = v
	}
	if v := os.Getenv("GH_CRFIX_FIX_MODEL"); v != "" {
		cfg.FixModel = v
	}
	if v := os.Getenv("GH_CRFIX_SCORE_NEEDS_LLM"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil && f >= 0 && f <= 1 {
			cfg.ScoreNeedsLLM = f
		}
	}
	if v := os.Getenv("GH_CRFIX_SCORE_PR_COMMENT"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil && f >= 0 && f <= 1 {
			cfg.ScorePRComment = f
		}
	}
	if v := os.Getenv("GH_CRFIX_SCORE_TEST_FAILURE"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil && f >= 0 && f <= 1 {
			cfg.ScoreTestFailure = f
		}
	}
}

// applyFlags overlays CLI flags on top of the loaded config.
func applyFlags(flags []string, cfg *config.Config) {
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--ai-backend":
			if i+1 < len(flags) {
				i++
				cfg.AIBackend = flags[i]
			}
		case "--gate-model":
			if i+1 < len(flags) {
				i++
				cfg.GateModel = flags[i]
			}
		case "--fix-model":
			if i+1 < len(flags) {
				i++
				cfg.FixModel = flags[i]
			}
		case "-c", "--concurrency":
			if i+1 < len(flags) {
				i++
				var n int
				fmt.Sscanf(flags[i], "%d", &n)
				if n > 0 {
					cfg.Concurrency = n
				}
			}
		case "--score-needs-llm":
			if i+1 < len(flags) {
				i++
				var v float64
				if _, err := fmt.Sscanf(flags[i], "%f", &v); err == nil && v >= 0 && v <= 1 {
					cfg.ScoreNeedsLLM = v
				}
			}
		case "--score-pr-comment":
			if i+1 < len(flags) {
				i++
				var v float64
				if _, err := fmt.Sscanf(flags[i], "%f", &v); err == nil && v >= 0 && v <= 1 {
					cfg.ScorePRComment = v
				}
			}
		case "--score-test-failure":
			if i+1 < len(flags) {
				i++
				var v float64
				if _, err := fmt.Sscanf(flags[i], "%f", &v); err == nil && v >= 0 && v <= 1 {
					cfg.ScoreTestFailure = v
				}
			}
		case "--seq":
			cfg.Concurrency = 1
		case "--worktree-mode":
			if i+1 < len(flags) {
				i++
				switch flags[i] {
				case "temp", "reuse", "stash":
					cfg.WorktreeMode = flags[i]
				}
			}
		}
	}
}

// applyWorkflowFlags overlays CLI flags on workflow options.
func applyWorkflowFlags(flags []string, opts *workflow.Options) {
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--ai-backend":
			if i+1 < len(flags) {
				i++
				opts.AIBackend = ai.ParseBackend(flags[i])
			}
		case "--gate-model":
			if i+1 < len(flags) {
				i++
				opts.GateModel = flags[i]
			}
		case "--fix-model":
			if i+1 < len(flags) {
				i++
				opts.FixModel = flags[i]
			}
		case "--max-threads":
			if i+1 < len(flags) {
				i++
				var n int
				fmt.Sscanf(flags[i], "%d", &n)
				if n > 0 {
					opts.MaxThreads = n
				}
			}
		case "--autofix-hook":
			if i+1 < len(flags) {
				i++
				opts.AutofixHook = flags[i]
			}
		case "--validate-hook":
			if i+1 < len(flags) {
				i++
				opts.ValidateHook = flags[i]
			}
		case "--review-wait":
			if i+1 < len(flags) {
				i++
				var n int
				fmt.Sscanf(flags[i], "%d", &n)
				if n >= 0 {
					opts.ReviewWaitSecs = n
				}
			}
		case "--dry-run":
			opts.DryRun = true
		case "--no-resolve":
			opts.NoResolve = true
		case "--resolve-skipped":
			opts.ResolveSkipped = true
		case "--no-post-fix":
			opts.NoPostFix = true
		case "--no-autofix":
			opts.NoAutofix = true
		case "--no-validate":
			opts.NoValidate = true
		case "--setup-only":
			opts.SetupOnly = true
		case "--exclude-outdated":
			opts.IncludeOutdated = false
		case "--include-outdated":
			opts.IncludeOutdated = true
		case "--verbose":
			opts.Verbose = true
		case "--worktree-mode":
			if i+1 < len(flags) {
				i++
				switch flags[i] {
				case "temp", "reuse", "stash":
					opts.WorktreeMode = flags[i]
				}
			}
		}
	}
}

func currentRepo() (string, error) {
	out, err := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").Output()
	if err != nil {
		// exec.LookPath("gh") is worth a try for a more helpful error msg,
		// but for now: just fall through with the original error.
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	// `gh repo view -q` adds a trailing newline.
	return strings.TrimSpace(string(out)), nil
}

func defaultConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gh-crfix", "defaults")
}

func backendName(b ai.Backend) string {
	switch b {
	case ai.BackendClaude:
		return "claude"
	case ai.BackendCodex:
		return "codex"
	default:
		return "auto"
	}
}

// backendFromModelFamily infers the backend from the configured models.
// Returns BackendAuto when the models are ambiguous (e.g. one claude + one
// openai, or an unknown family) so the caller can fall through to executable
// detection. Both models must agree on a family for this to return a
// concrete backend.
// parseScoreFlag parses a score-weight string and enforces the documented
// [0, 1] bound. Out-of-range or unparseable values are rejected (ok=false)
// so bad CLI input never silently alters gate decisions at runtime.
func parseScoreFlag(raw string) (float64, bool) {
	var v float64
	if _, err := fmt.Sscanf(raw, "%f", &v); err != nil {
		return 0, false
	}
	if v < 0 || v > 1 {
		return 0, false
	}
	return v, true
}

func backendFromModelFamily(gateModel, fixModel string) ai.Backend {
	g := model.Family(gateModel)
	f := model.Family(fixModel)
	if g == "" || f == "" || g != f {
		return ai.BackendAuto
	}
	switch g {
	case "claude":
		return ai.BackendClaude
	case "codex":
		return ai.BackendCodex
	}
	return ai.BackendAuto
}

func usage() {
	var b bytes.Buffer
	fmt.Fprintf(&b, `gh-crfix %s (Go port)

Usage:
  gh crfix                 interactive launcher (TTY only)
  gh crfix <url>           single PR URL
  gh crfix <url-range>     range   e.g. .../pull/93-95
  gh crfix <url-list>      list    e.g. .../pull/[93,94,95]
  gh crfix <number>        bare number — uses current repo
  gh crfix <n1>-<n2>       bare range
  gh crfix <n1>,<n2>,...   bare list

Flags:
  -c N, --concurrency N    parallel workers (default: 3)
  --seq                    sequential mode (same as -c 1)
  --ai-backend BACKEND     auto|claude|codex
  --gate-model MODEL       small gate model (default: sonnet)
  --fix-model  MODEL       advanced fix model (default: sonnet)
  --max-threads N          max threads fetched per PR (default: 100)
  --validate-hook PATH     repo-local validation script
  --autofix-hook PATH      repo-local autofix script
  --no-autofix             skip autofix hook
  --no-validate            skip the validation step entirely
  --dry-run                no GitHub writes, no AI run
  --exclude-outdated       skip outdated threads
  --include-outdated       include outdated threads (default)
  --resolve-skipped        resolve skipped threads too
  --no-resolve             do not reply or resolve
  --no-post-fix            skip post-fix review cycle
  --review-wait SECS       post-fix wait before re-check (default: 180)
  --setup-only             set up worktrees and exit
  --score-needs-llm N      gate score weight [0,1]
  --score-pr-comment N     gate score weight [0,1]
  --score-test-failure N   gate score weight [0,1]
  --no-tui                 disable the Bubble Tea dashboard even on TTY
  --no-notify              suppress the completion notification
  --worktree-mode MODE     temp|reuse|stash (default: temp)
  --setup, -s              run the interactive setup wizard
  --verbose                verbose output
  --version, -v            show version
  --help, -h               show this help

Env:
  GH_CRFIX_DIR                   local repo path (defaults to current git root)
  GH_CRFIX_MODEL_REGISTRY        override the registry JSON URL
  GH_CRFIX_AI_BACKEND            auto|claude|codex (overrides persisted config)
  GH_CRFIX_GATE_MODEL            gate model name (overrides persisted config)
  GH_CRFIX_FIX_MODEL             fix model name (overrides persisted config)
  GH_CRFIX_REVIEW_WAIT           seconds to wait for post-fix re-review
  GH_CRFIX_SCORE_NEEDS_LLM       gate score weight [0,1]
  GH_CRFIX_SCORE_PR_COMMENT      gate score weight [0,1]
  GH_CRFIX_SCORE_TEST_FAILURE    gate score weight [0,1]
  GH_CRFIX_NO_NOTIFY=1           process-wide suppression of completion notifications

Precedence for config values: CLI flag > GH_CRFIX_* env var > config file > default.

Config: %s
`, version, defaultConfigPath())
	fmt.Print(b.String())
}

// --- Design note: dashboard vs per-PR stdout --------------------------------
//
// ProcessPR writes per-PR status lines to os.Stdout via plain fmt.Printf.
// The dashboard runs in-place (no altscreen) and repaints the terminal on
// every 250ms tick, so if we let ProcessPR keep writing, the two surfaces
// tear into each other.
//
// The simplest fix that doesn't require changing ProcessPR's signature is
// to swap os.Stdout to the master log file handle for the duration of the
// dashboard. Per-PR narration still lands in run.log and is visible in the
// dashboard's detail pane (it tails that file). After the batch returns we
// restore the real stdout so PrintResults + the done banner render normally.
//
// This is documented here because it is load-bearing for the e2e test:
// without it the "Setup" / "Done" banners still appear (they are written
// after stdout is restored), but the per-PR lines move to the log as
// intended.
