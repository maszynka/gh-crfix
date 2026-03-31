#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

@test "resolve_ai_backend: auto falls back to codex when claude is unavailable" {
  mock_command "codex" 0
  AI_BACKEND="auto"
  GATE_MODEL="sonnet"
  FIX_MODEL="sonnet"

  resolve_ai_backend

  [ "$AI_BACKEND" = "codex" ]
  [ "$GATE_MODEL" = "gpt-5.4-mini" ]
  [ "$FIX_MODEL" = "gpt-5.4" ]
}

@test "resolve_ai_backend: auto infers codex from selected model family" {
  mock_command "claude" 0
  mock_command "codex" 0
  AI_BACKEND="auto"
  GATE_MODEL="gpt-5.4-mini"
  FIX_MODEL="gpt-5.4"

  resolve_ai_backend

  [ "$AI_BACKEND" = "codex" ]
}

@test "resolve_ai_backend: auto rejects mixed model families" {
  mock_command "claude" 0
  mock_command "codex" 0

  run bash -c "source '$SCRIPT_PATH'; AI_BACKEND=auto; GATE_MODEL=sonnet; FIX_MODEL=gpt-5.4; resolve_ai_backend"
  [ "$status" -ne 0 ]
  [[ "$output" == *"cannot mix Claude and Codex"* ]]
}

@test "resolve_ai_backend: explicit codex backend rejects claude-family models" {
  mock_command "codex" 0

  run bash -c "source '$SCRIPT_PATH'; AI_BACKEND=codex; GATE_MODEL=sonnet; FIX_MODEL=sonnet; resolve_ai_backend"
  [ "$status" -ne 0 ]
  [[ "$output" == *"not compatible with --ai-backend codex"* ]]
}
