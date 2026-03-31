#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
  export HOME="$TEST_TMPDIR/home"
  mkdir -p "$HOME"
  unset GH_CRFIX_AI_BACKEND GH_CRFIX_GATE_MODEL GH_CRFIX_FIX_MODEL \
    GH_CRFIX_SCORE_NEEDS_LLM GH_CRFIX_SCORE_PR_COMMENT GH_CRFIX_SCORE_TEST_FAILURE
}

teardown() { teardown_common; }

@test "save_persisted_defaults: writes launcher defaults file" {
  AI_BACKEND="codex"
  GATE_MODEL="gpt-5.4-mini"
  FIX_MODEL="gpt-5.4"
  CONCURRENCY=7
  SCORE_NEEDS_LLM=".2"
  SCORE_PR_COMMENT=".4"
  SCORE_TEST_FAILURE="1"

  save_persisted_defaults
  [ -f "$HOME/.config/gh-crfix/defaults" ]
  grep -q '^AI_BACKEND=codex$' "$HOME/.config/gh-crfix/defaults"
  grep -q '^GATE_MODEL=gpt-5.4-mini$' "$HOME/.config/gh-crfix/defaults"
  grep -q '^CONCURRENCY=7$' "$HOME/.config/gh-crfix/defaults"
  grep -q '^SCORE_NEEDS_LLM=0.200$' "$HOME/.config/gh-crfix/defaults"
}

@test "load_persisted_defaults: loads saved defaults when env does not override" {
  mkdir -p "$HOME/.config/gh-crfix"
  cat > "$HOME/.config/gh-crfix/defaults" <<'EOF'
# gh-crfix persisted defaults
AI_BACKEND=codex
GATE_MODEL=gpt-5.4-mini
FIX_MODEL=gpt-5.4
CONCURRENCY=9
SCORE_NEEDS_LLM=0.200
SCORE_PR_COMMENT=0.400
SCORE_TEST_FAILURE=1.000
EOF

  AI_BACKEND="auto"
  GATE_MODEL="sonnet"
  FIX_MODEL="sonnet"
  CONCURRENCY=3
  SCORE_NEEDS_LLM="1"
  SCORE_PR_COMMENT="0.4"
  SCORE_TEST_FAILURE="1"

  load_persisted_defaults
  [ "$AI_BACKEND" = "codex" ]
  [ "$GATE_MODEL" = "gpt-5.4-mini" ]
  [ "$FIX_MODEL" = "gpt-5.4" ]
  [ "$CONCURRENCY" -eq 9 ]
  [ "$SCORE_NEEDS_LLM" = "0.200" ]
}

@test "load_persisted_defaults: env-backed values win over saved defaults" {
  mkdir -p "$HOME/.config/gh-crfix"
  cat > "$HOME/.config/gh-crfix/defaults" <<'EOF'
AI_BACKEND=codex
GATE_MODEL=gpt-5.4-mini
EOF

  export GH_CRFIX_AI_BACKEND="claude"
  export GH_CRFIX_GATE_MODEL="haiku"
  AI_BACKEND="claude"
  GATE_MODEL="haiku"

  load_persisted_defaults
  [ "$AI_BACKEND" = "claude" ]
  [ "$GATE_MODEL" = "haiku" ]
}

@test "launcher_apply: saves selected defaults after successful submit" {
  LAUNCH_TARGET="https://github.com/owner/repo/pull/123"
  LAUNCH_BACKEND="codex"
  LAUNCH_GATE_MODEL="gpt-5.4-mini"
  LAUNCH_FIX_MODEL="gpt-5.4"
  LAUNCH_CONCURRENCY="5"
  LAUNCH_SCORE_NEEDS_LLM=".2"
  LAUNCH_SCORE_PR_COMMENT=".4"
  LAUNCH_SCORE_TEST_FAILURE="1"

  launcher_apply
  [ "$AI_BACKEND" = "codex" ]
  [ "$GATE_MODEL" = "gpt-5.4-mini" ]
  [ "$FIX_MODEL" = "gpt-5.4" ]
  [ "$CONCURRENCY" -eq 5 ]
  [ -f "$HOME/.config/gh-crfix/defaults" ]
  grep -q '^FIX_MODEL=gpt-5.4$' "$HOME/.config/gh-crfix/defaults"
}
