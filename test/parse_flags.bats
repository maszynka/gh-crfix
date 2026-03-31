#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"mock/repo\"}"
    fi
  '
}

teardown() { teardown_common; }

@test "parse_flags: --seq sets CONCURRENCY=1" {
  parse_flags --seq "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 1 ]
}

@test "parse_flags: -c 2 sets CONCURRENCY=2" {
  parse_flags -c 2 "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 2 ]
}

@test "parse_flags: --concurrency 8" {
  parse_flags --concurrency 8 "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 8 ]
}

@test "parse_flags: --ai-backend codex sets AI_BACKEND=codex" {
  parse_flags --ai-backend codex "https://github.com/o/r/pull/1"
  [ "$AI_BACKEND" = "codex" ]
}

@test "parse_flags: --no-tui sets NO_TUI=true" {
  parse_flags --no-tui "https://github.com/o/r/pull/1"
  [ "$NO_TUI" = true ]
}

@test "parse_flags: --setup-only sets SETUP_ONLY=true" {
  parse_flags --setup-only "https://github.com/o/r/pull/1"
  [ "$SETUP_ONLY" = true ]
}

@test "parse_flags: --validate-hook sets VALIDATE_HOOK" {
  parse_flags --validate-hook "/tmp/validate.sh" "https://github.com/o/r/pull/1"
  [ "$VALIDATE_HOOK" = "/tmp/validate.sh" ]
}

@test "parse_flags: --no-validate sets NO_VALIDATE=true" {
  parse_flags --no-validate "https://github.com/o/r/pull/1"
  [ "$NO_VALIDATE" = true ]
}

@test "parse_flags: score flags accept decimal fractions" {
  parse_flags \
    --score-needs-llm .2 \
    --score-pr-comment 0.4 \
    --score-test-failure 1 \
    "https://github.com/o/r/pull/1"
  [ "$SCORE_NEEDS_LLM" = ".2" ]
  [ "$SCORE_PR_COMMENT" = "0.4" ]
  [ "$SCORE_TEST_FAILURE" = "1" ]
}

@test "parse_flags: --no-resolve sets NO_RESOLVE=true" {
  parse_flags --no-resolve "https://github.com/o/r/pull/1"
  [ "$NO_RESOLVE" = true ]
}

@test "parse_flags: combined flags" {
  parse_flags --seq --no-resolve --no-tui "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 1 ]
  [ "$NO_RESOLVE" = true ]
  [ "$NO_TUI" = true ]
}

@test "parse_flags: -c 0 fails" {
  run bash -c "source '$SCRIPT_PATH'; parse_flags -c 0 https://github.com/o/r/pull/1 2>&1"
  [ "$status" -ne 0 ]
}

@test "parse_flags: -c abc fails" {
  run bash -c "source '$SCRIPT_PATH'; parse_flags -c abc https://github.com/o/r/pull/1 2>&1"
  [ "$status" -ne 0 ]
}

@test "parse_flags: invalid score fails" {
  run bash -c "source '$SCRIPT_PATH'; parse_flags --score-needs-llm 1.5 https://github.com/o/r/pull/1 2>&1"
  [ "$status" -ne 0 ]
}

@test "parse_flags: default concurrency is 3" {
  parse_flags "https://github.com/o/r/pull/1"
  [ "$CONCURRENCY" -eq 3 ]
}

@test "parse_flags: flags after positional still work" {
  parse_flags "https://github.com/o/r/pull/1" --seq
  [ "$CONCURRENCY" -eq 1 ]
}
