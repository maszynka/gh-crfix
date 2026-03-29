#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"mock/repo\"}"
    fi
  '
}

teardown() { teardown_common; }

# ── individual flags ─────────────────────────────────────────────────────────

@test "parse_flags: --no-tui sets NO_TUI=true" {
  parse_flags --no-tui "https://github.com/o/r/pull/1"
  [ "$NO_TUI" = "true" ]
}

@test "parse_flags: --no-post-fix sets NO_POST_FIX=true" {
  parse_flags --no-post-fix "https://github.com/o/r/pull/1"
  [ "$NO_POST_FIX" = "true" ]
}

@test "parse_flags: --dry-run sets DRY_RUN=true" {
  parse_flags --dry-run "https://github.com/o/r/pull/1"
  [ "$DRY_RUN" = "true" ]
}

@test "parse_flags: --ai-backend sets AI_BACKEND" {
  parse_flags --ai-backend "codex" "https://github.com/o/r/pull/1"
  [ "$AI_BACKEND" = "codex" ]
}

@test "parse_flags: --gate-model sets GATE_MODEL" {
  parse_flags --gate-model "gpt-4o-mini" "https://github.com/o/r/pull/1"
  [ "$GATE_MODEL" = "gpt-4o-mini" ]
}

@test "parse_flags: --fix-model sets FIX_MODEL" {
  parse_flags --fix-model "opus" "https://github.com/o/r/pull/1"
  [ "$FIX_MODEL" = "opus" ]
}

@test "parse_flags: --resolve-skipped sets RESOLVE_SKIPPED=true" {
  parse_flags --resolve-skipped "https://github.com/o/r/pull/1"
  [ "$RESOLVE_SKIPPED" = "true" ]
}

@test "parse_flags: --seq sets CONCURRENCY=1" {
  parse_flags --seq "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 1 ]
}

@test "parse_flags: --include-outdated sets INCLUDE_OUTDATED=true" {
  parse_flags --include-outdated "https://github.com/o/r/pull/1"
  [ "$INCLUDE_OUTDATED" = "true" ]
}

@test "parse_flags: --no-resolve sets NO_RESOLVE=true" {
  parse_flags --no-resolve "https://github.com/o/r/pull/1"
  [ "$NO_RESOLVE" = "true" ]
}

@test "parse_flags: --no-autofix sets NO_AUTOFIX=true" {
  parse_flags --no-autofix "https://github.com/o/r/pull/1"
  [ "$NO_AUTOFIX" = "true" ]
}

@test "parse_flags: --setup-only sets SETUP_ONLY=true" {
  parse_flags --setup-only "https://github.com/o/r/pull/1"
  [ "$SETUP_ONLY" = "true" ]
}

@test "parse_flags: --autofix-hook sets AUTO_FIX_HOOK" {
  parse_flags --autofix-hook "/path/to/hook.sh" "https://github.com/o/r/pull/1"
  [ "$AUTO_FIX_HOOK" = "/path/to/hook.sh" ]
}

@test "parse_flags: --validate-hook sets VALIDATE_HOOK" {
  parse_flags --validate-hook "/path/to/validate.sh" "https://github.com/o/r/pull/1"
  [ "$VALIDATE_HOOK" = "/path/to/validate.sh" ]
}

@test "parse_flags: --no-validate sets NO_VALIDATE=true" {
  parse_flags --no-validate "https://github.com/o/r/pull/1"
  [ "$NO_VALIDATE" = "true" ]
}

@test "parse_flags: score flags set weights" {
  parse_flags \
    --score-needs-llm .2 \
    --score-pr-comment 0.4 \
    --score-test-failure 1 \
    "https://github.com/o/r/pull/1"

  [ "$SCORE_NEEDS_LLM" = ".2" ]
  [ "$SCORE_PR_COMMENT" = "0.4" ]
  [ "$SCORE_TEST_FAILURE" = "1" ]
}

@test "parse_flags: --max-threads sets MAX_THREADS" {
  parse_flags --max-threads 50 "https://github.com/o/r/pull/1"
  [ "$MAX_THREADS" -eq 50 ]
}

@test "parse_flags: -c N sets CONCURRENCY" {
  parse_flags -c 5 "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 5 ]
}

@test "parse_flags: default CONCURRENCY is 3" {
  parse_flags "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 3 ]
}

# ── combined flags ───────────────────────────────────────────────────────────

@test "parse_flags: all gpt-5.4 flags combined" {
  parse_flags \
    --ai-backend "codex" \
    --gate-model "gpt-5.4-mini" \
    --fix-model "gpt-5.4" \
    --validate-hook "/repo/.gh-crfix/validate.sh" \
    --score-needs-llm .2 \
    --score-pr-comment 0.4 \
    --score-test-failure 1 \
    --no-tui \
    --no-post-fix \
    --dry-run \
    --resolve-skipped \
    --include-outdated \
    --max-threads 200 \
    -c 8 \
    "https://github.com/o/r/pull/55"

  [ "$AI_BACKEND" = "codex" ]
  [ "$GATE_MODEL" = "gpt-5.4-mini" ]
  [ "$FIX_MODEL" = "gpt-5.4" ]
  [ "$VALIDATE_HOOK" = "/repo/.gh-crfix/validate.sh" ]
  [ "$SCORE_NEEDS_LLM" = ".2" ]
  [ "$SCORE_PR_COMMENT" = "0.4" ]
  [ "$SCORE_TEST_FAILURE" = "1" ]
  [ "$NO_TUI" = "true" ]
  [ "$NO_POST_FIX" = "true" ]
  [ "$DRY_RUN" = "true" ]
  [ "$RESOLVE_SKIPPED" = "true" ]
  [ "$INCLUDE_OUTDATED" = "true" ]
  [ "$MAX_THREADS" -eq 200 ]
  [ "$CONCURRENCY" -eq 8 ]
  [ "$PR_NUMBERS" = "55" ]
}

@test "parse_flags: Claude flags combined" {
  parse_flags \
    --ai-backend "claude" \
    --gate-model "haiku" \
    --fix-model "sonnet" \
    --no-tui \
    --dry-run \
    --seq \
    --no-autofix \
    --no-validate \
    --no-resolve \
    --setup-only \
    --autofix-hook "/repo/.gh-crfix/fix.sh" \
    "https://github.com/owner/repo/pull/10"

  [ "$AI_BACKEND" = "claude" ]
  [ "$GATE_MODEL" = "haiku" ]
  [ "$FIX_MODEL" = "sonnet" ]
  [ "$NO_TUI" = "true" ]
  [ "$DRY_RUN" = "true" ]
  [ "$CONCURRENCY" -eq 1 ]
  [ "$NO_AUTOFIX" = "true" ]
  [ "$NO_VALIDATE" = "true" ]
  [ "$NO_RESOLVE" = "true" ]
  [ "$SETUP_ONLY" = "true" ]
  [ "$AUTO_FIX_HOOK" = "/repo/.gh-crfix/fix.sh" ]
  [ "$PR_NUMBERS" = "10" ]
}

@test "parse_flags: all gpt-5.4 + Claude flags combined" {
  parse_flags \
    --ai-backend "codex" \
    --gate-model "gpt-5.4-mini" \
    --fix-model "claude-sonnet-4-20250514" \
    --no-tui \
    --no-post-fix \
    --dry-run \
    --resolve-skipped \
    --include-outdated \
    --no-resolve \
    --no-autofix \
    --setup-only \
    --max-threads 500 \
    --autofix-hook "/tmp/hook.sh" \
    -c 2 \
    "https://github.com/org/monorepo/pull/123"

  [ "$AI_BACKEND" = "codex" ]
  [ "$GATE_MODEL" = "gpt-5.4-mini" ]
  [ "$FIX_MODEL" = "claude-sonnet-4-20250514" ]
  [ "$NO_TUI" = "true" ]
  [ "$NO_POST_FIX" = "true" ]
  [ "$DRY_RUN" = "true" ]
  [ "$RESOLVE_SKIPPED" = "true" ]
  [ "$INCLUDE_OUTDATED" = "true" ]
  [ "$NO_RESOLVE" = "true" ]
  [ "$NO_AUTOFIX" = "true" ]
  [ "$SETUP_ONLY" = "true" ]
  [ "$MAX_THREADS" -eq 500 ]
  [ "$AUTO_FIX_HOOK" = "/tmp/hook.sh" ]
  [ "$CONCURRENCY" -eq 2 ]
  [ "$OWNER_REPO" = "org/monorepo" ]
  [ "$PR_NUMBERS" = "123" ]
}
