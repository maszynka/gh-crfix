package main

import (
	"testing"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/config"
)

// TestResolveConfig_FlagsOverrideConfig verifies that CLI flags override the
// persisted defaults and that positional PR specs are parsed correctly.
func TestResolveConfig_FlagsOverrideConfig(t *testing.T) {
	cfg := config.Config{
		AIBackend:        "auto",
		GateModel:        "sonnet",
		FixModel:         "sonnet",
		Concurrency:      3,
		ScoreNeedsLLM:    1.0,
		ScorePRComment:   0.4,
		ScoreTestFailure: 1.0,
	}
	plan, err := resolveConfig(
		[]string{
			"https://github.com/acme/proj/pull/42",
			"--ai-backend", "claude",
			"--gate-model", "haiku",
			"--fix-model", "opus",
			"-c", "7",
			"--dry-run",
			"--no-tui",
			"--no-notify",
		},
		cfg,
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if plan.ownerRepo != "acme/proj" {
		t.Errorf("ownerRepo=%q, want acme/proj", plan.ownerRepo)
	}
	if len(plan.prNums) != 1 || plan.prNums[0] != 42 {
		t.Errorf("prNums=%v, want [42]", plan.prNums)
	}
	if plan.opts.AIBackend != ai.BackendClaude {
		t.Errorf("AIBackend=%v, want claude", plan.opts.AIBackend)
	}
	if plan.opts.GateModel != "haiku" {
		t.Errorf("GateModel=%q, want haiku", plan.opts.GateModel)
	}
	if plan.opts.FixModel != "opus" {
		t.Errorf("FixModel=%q, want opus", plan.opts.FixModel)
	}
	if plan.concurrency != 7 {
		t.Errorf("concurrency=%d, want 7", plan.concurrency)
	}
	if !plan.opts.DryRun {
		t.Errorf("DryRun=false, want true")
	}
	if !plan.noTUI {
		t.Errorf("noTUI=false, want true")
	}
	if !plan.noNotify {
		t.Errorf("noNotify=false, want true")
	}
}

// TestResolveConfig_ConfigDefaultsWin verifies that when no CLI flag overrides
// a setting, the persisted config value is used.
func TestResolveConfig_ConfigDefaultsWin(t *testing.T) {
	cfg := config.Config{
		AIBackend:        "codex",
		GateModel:        "gpt-5.4-mini",
		FixModel:         "gpt-5.4",
		Concurrency:      5,
		ScoreNeedsLLM:    0.5,
		ScorePRComment:   0.2,
		ScoreTestFailure: 0.9,
	}
	plan, err := resolveConfig(
		[]string{"https://github.com/a/b/pull/1"},
		cfg,
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if plan.opts.AIBackend != ai.BackendCodex {
		t.Errorf("AIBackend=%v, want codex", plan.opts.AIBackend)
	}
	if plan.opts.GateModel != "gpt-5.4-mini" {
		t.Errorf("GateModel=%q, want gpt-5.4-mini", plan.opts.GateModel)
	}
	if plan.concurrency != 5 {
		t.Errorf("concurrency=%d, want 5", plan.concurrency)
	}
}

// TestResolveConfig_LauncherHandoff simulates the TUI launcher submitting a
// result that main.go converts back into a CLI-equivalent runPlan. The test
// makes sure Target, backend, models, concurrency, and score weights all
// survive the handoff unchanged.
func TestResolveConfig_LauncherHandoff(t *testing.T) {
	cfg := config.Defaults()
	// Launcher builds CLI-style args from the user's choices. The main-wiring
	// contract is that resolveConfig handles those args identically to a CLI
	// invocation.
	args := []string{
		"https://github.com/owner/repo/pull/7",
		"--ai-backend", "claude",
		"--gate-model", "sonnet",
		"--fix-model", "opus",
		"-c", "4",
		"--score-needs-llm", "0.8",
		"--score-pr-comment", "0.3",
		"--score-test-failure", "0.7",
	}
	plan, err := resolveConfig(args, cfg)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if plan.ownerRepo != "owner/repo" || len(plan.prNums) != 1 || plan.prNums[0] != 7 {
		t.Errorf("owner/repo/prNums: got %q %v", plan.ownerRepo, plan.prNums)
	}
	if plan.opts.AIBackend != ai.BackendClaude {
		t.Errorf("backend=%v", plan.opts.AIBackend)
	}
	if plan.opts.Weights.NeedsLLM != 0.8 ||
		plan.opts.Weights.PRComment != 0.3 ||
		plan.opts.Weights.TestFailure != 0.7 {
		t.Errorf("weights=%+v", plan.opts.Weights)
	}
	if plan.concurrency != 4 {
		t.Errorf("concurrency=%d", plan.concurrency)
	}
}

// TestResolveConfig_EnvOverridesAreApplied verifies that GH_CRFIX_* environment
// variables documented by the bash script are honored by resolveConfig. Per
// the bash precedence rule (flag > env > file > default), env vars override
// persisted config only when no CLI flag explicitly sets them.
func TestResolveConfig_EnvOverridesAreApplied(t *testing.T) {
	// A persisted config that differs from what we'll set via env vars.
	cfg := config.Config{
		AIBackend:        "auto",
		GateModel:        "sonnet",
		FixModel:         "sonnet",
		Concurrency:      3,
		ScoreNeedsLLM:    1.0,
		ScorePRComment:   0.4,
		ScoreTestFailure: 1.0,
	}

	t.Setenv("GH_CRFIX_AI_BACKEND", "claude")
	t.Setenv("GH_CRFIX_GATE_MODEL", "env-gate")
	t.Setenv("GH_CRFIX_FIX_MODEL", "env-fix")
	t.Setenv("GH_CRFIX_REVIEW_WAIT", "45")
	t.Setenv("GH_CRFIX_SCORE_NEEDS_LLM", "0.9")
	t.Setenv("GH_CRFIX_SCORE_PR_COMMENT", "0.2")
	t.Setenv("GH_CRFIX_SCORE_TEST_FAILURE", "0.8")

	plan, err := resolveConfig(
		[]string{"https://github.com/a/b/pull/1"},
		cfg,
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if plan.opts.AIBackend != ai.BackendClaude {
		t.Errorf("AIBackend=%v, want claude (from GH_CRFIX_AI_BACKEND)", plan.opts.AIBackend)
	}
	if plan.opts.GateModel != "env-gate" {
		t.Errorf("GateModel=%q, want env-gate (from GH_CRFIX_GATE_MODEL)", plan.opts.GateModel)
	}
	if plan.opts.FixModel != "env-fix" {
		t.Errorf("FixModel=%q, want env-fix (from GH_CRFIX_FIX_MODEL)", plan.opts.FixModel)
	}
	if plan.opts.ReviewWaitSecs != 45 {
		t.Errorf("ReviewWaitSecs=%d, want 45 (from GH_CRFIX_REVIEW_WAIT)", plan.opts.ReviewWaitSecs)
	}
	if plan.opts.Weights.NeedsLLM != 0.9 {
		t.Errorf("Weights.NeedsLLM=%v, want 0.9 (from env)", plan.opts.Weights.NeedsLLM)
	}
	if plan.opts.Weights.PRComment != 0.2 {
		t.Errorf("Weights.PRComment=%v, want 0.2 (from env)", plan.opts.Weights.PRComment)
	}
	if plan.opts.Weights.TestFailure != 0.8 {
		t.Errorf("Weights.TestFailure=%v, want 0.8 (from env)", plan.opts.Weights.TestFailure)
	}
}

// TestResolveConfig_FlagBeatsEnv verifies bash precedence: CLI flags take
// precedence over GH_CRFIX_* environment variables.
func TestResolveConfig_FlagBeatsEnv(t *testing.T) {
	cfg := config.Defaults()

	t.Setenv("GH_CRFIX_AI_BACKEND", "codex")
	t.Setenv("GH_CRFIX_GATE_MODEL", "env-gate")
	t.Setenv("GH_CRFIX_REVIEW_WAIT", "45")

	plan, err := resolveConfig(
		[]string{
			"https://github.com/a/b/pull/1",
			"--ai-backend", "claude",
			"--gate-model", "flag-gate",
			"--review-wait", "90",
		},
		cfg,
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if plan.opts.AIBackend != ai.BackendClaude {
		t.Errorf("flag should beat env: AIBackend=%v, want claude", plan.opts.AIBackend)
	}
	if plan.opts.GateModel != "flag-gate" {
		t.Errorf("flag should beat env: GateModel=%q, want flag-gate", plan.opts.GateModel)
	}
	if plan.opts.ReviewWaitSecs != 90 {
		t.Errorf("flag should beat env: ReviewWaitSecs=%d, want 90", plan.opts.ReviewWaitSecs)
	}
}

// TestResolveConfig_NoValidateFlag verifies that --no-validate sets
// opts.NoValidate so ProcessPR can skip the validation step.
func TestResolveConfig_NoValidateFlag(t *testing.T) {
	plan, err := resolveConfig(
		[]string{"https://github.com/a/b/pull/1", "--no-validate"},
		config.Defaults(),
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if !plan.opts.NoValidate {
		t.Errorf("--no-validate did not set opts.NoValidate")
	}
}

// TestResolveConfig_ErrorOnMissingTarget verifies that running with no
// positional args and no URL produces a helpful error.
func TestResolveConfig_ErrorOnMissingTarget(t *testing.T) {
	// Empty args with a config means the caller should have gone through the
	// launcher; resolveConfig itself should return an error when it has
	// nothing to target.
	_, err := resolveConfig([]string{}, config.Defaults())
	if err == nil {
		t.Fatal("resolveConfig([]) returned nil error; want missing-target error")
	}
}
