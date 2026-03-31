#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

@test "launcher_score_token: converts normalized score to arrow option token" {
  [ "$(launcher_score_token "0.200")" = ".2" ]
  [ "$(launcher_score_token ".4")" = ".4" ]
  [ "$(launcher_score_token "1")" = "1" ]
}

@test "launcher_reset: loads current defaults into arrow-friendly values" {
  AI_BACKEND="codex"
  GATE_MODEL="gpt-5.4"
  FIX_MODEL="gpt-5.4-mini"
  CONCURRENCY=10
  SCORE_NEEDS_LLM="0.200"
  SCORE_PR_COMMENT="0.400"
  SCORE_TEST_FAILURE="1.000"

  launcher_reset

  [ "$LAUNCH_BACKEND" = "codex" ]
  [ "$LAUNCH_GATE_MODEL" = "gpt-5.4" ]
  [ "$LAUNCH_FIX_MODEL" = "gpt-5.4-mini" ]
  [ "$LAUNCH_CONCURRENCY" = "10" ]
  [ "$LAUNCH_SCORE_NEEDS_LLM" = ".2" ]
  [ "$LAUNCH_SCORE_PR_COMMENT" = ".4" ]
  [ "$LAUNCH_SCORE_TEST_FAILURE" = "1" ]
}

@test "launcher_cycle_field: backend switch updates invalid model selections" {
  launcher_reset
  LAUNCH_BACKEND="codex"
  LAUNCH_GATE_MODEL="gpt-5.4"
  LAUNCH_FIX_MODEL="gpt-5.4-mini"

  launcher_cycle_field 1 "right"

  [ "$LAUNCH_BACKEND" = "auto" ]

  launcher_cycle_field 1 "right"

  [ "$LAUNCH_BACKEND" = "claude" ]
  [ "$LAUNCH_GATE_MODEL" = "sonnet" ]
  [ "$LAUNCH_FIX_MODEL" = "sonnet" ]
}

@test "launcher_cycle_field: concurrency and score use allowed option lists" {
  launcher_reset
  LAUNCH_CONCURRENCY="10"
  LAUNCH_SCORE_NEEDS_LLM=".2"

  launcher_cycle_field 4 "right"
  launcher_cycle_field 5 "left"

  [ "$LAUNCH_CONCURRENCY" = "11" ]
  [ "$LAUNCH_SCORE_NEEDS_LLM" = ".1" ]
}

@test "launcher_apply: rejects mixed model families in auto mode" {
  LAUNCH_TARGET="https://github.com/owner/repo/pull/123"
  LAUNCH_BACKEND="auto"
  LAUNCH_GATE_MODEL="sonnet"
  LAUNCH_FIX_MODEL="gpt-5.4"
  LAUNCH_CONCURRENCY="5"
  LAUNCH_SCORE_NEEDS_LLM=".2"
  LAUNCH_SCORE_PR_COMMENT=".4"
  LAUNCH_SCORE_TEST_FAILURE="1"

  run launcher_apply
  [ "$status" -ne 0 ]
  [[ "$output" == *"Auto backend cannot mix"* ]] || [ "$LAUNCH_ERROR" = "Auto backend cannot mix Claude and Codex model families." ]
}

@test "launcher_apply: maps parse_input auth failures to a clear launcher error" {
  LAUNCH_TARGET="123"
  LAUNCH_BACKEND="codex"
  LAUNCH_GATE_MODEL="gpt-5.4-mini"
  LAUNCH_FIX_MODEL="gpt-5.4"
  LAUNCH_CONCURRENCY="5"
  LAUNCH_SCORE_NEEDS_LLM=".2"
  LAUNCH_SCORE_PR_COMMENT=".4"
  LAUNCH_SCORE_TEST_FAILURE="1"
  parse_input() { echo "'gh' is not authenticated. Run: gh auth login" >&2; return 1; }

  run launcher_apply
  [ "$status" -ne 0 ]
  [ "$LAUNCH_ERROR" = "GitHub CLI is not authenticated. Run 'gh auth login' and try again." ]
}

@test "launcher_apply: maps non-repo numeric targets to a clear launcher error" {
  LAUNCH_TARGET="123"
  LAUNCH_BACKEND="codex"
  LAUNCH_GATE_MODEL="gpt-5.4-mini"
  LAUNCH_FIX_MODEL="gpt-5.4"
  LAUNCH_CONCURRENCY="5"
  LAUNCH_SCORE_NEEDS_LLM=".2"
  LAUNCH_SCORE_PR_COMMENT=".4"
  LAUNCH_SCORE_TEST_FAILURE="1"
  parse_input() { echo "Not inside a GitHub repo and no URL given." >&2; return 1; }

  run launcher_apply
  [ "$status" -ne 0 ]
  [ "$LAUNCH_ERROR" = "Numeric or shorthand targets require running inside a GitHub git repository." ]
}
