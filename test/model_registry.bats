#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script

  # Point registry at a local file (no network)
  export MODEL_REGISTRY_CACHE=""
  export HOME="$TEST_TMPDIR/home"
  mkdir -p "$HOME/.cache/gh-crfix"
}

teardown() { teardown_common; }

# ── fetch_model_registry ────────────────────────────────────────────────────

@test "fetch_model_registry: returns cached file when present" {
  cat > "$HOME/.cache/gh-crfix/models.json" <<'EOF'
{
  "updated_at": "2026-03-30T00:00:00Z",
  "anthropic": ["claude-sonnet-4-6-20250514"],
  "anthropic_aliases": ["sonnet"],
  "openai": ["gpt-5.4"],
  "openai_aliases": ["gpt-5.4"]
}
EOF
  # Touch to make it fresh
  touch "$HOME/.cache/gh-crfix/models.json"

  run fetch_model_registry
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.anthropic | length > 0'
}

@test "fetch_model_registry: returns empty JSON when no cache and no network" {
  MODEL_REGISTRY_URL="http://127.0.0.1:1/nonexistent"
  rm -f "$HOME/.cache/gh-crfix/models.json"

  run fetch_model_registry
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.' >/dev/null
}

# ── get_claude_models ───────────────────────────────────────────────────────

@test "get_claude_models: returns aliases then full IDs" {
  cat > "$HOME/.cache/gh-crfix/models.json" <<'EOF'
{
  "anthropic": ["claude-sonnet-4-6-20250514", "claude-opus-4-6-20250304"],
  "anthropic_aliases": ["sonnet", "opus"],
  "openai": [],
  "openai_aliases": []
}
EOF
  touch "$HOME/.cache/gh-crfix/models.json"

  run get_claude_models
  [ "$status" -eq 0 ]
  # aliases come first
  echo "$output" | head -1 | grep -q "sonnet"
  # full IDs present
  echo "$output" | grep -q "claude-sonnet-4-6-20250514"
}

# ── get_openai_models ──────────────────────────────────────────────────────

@test "get_openai_models: returns aliases then full IDs" {
  cat > "$HOME/.cache/gh-crfix/models.json" <<'EOF'
{
  "anthropic": [],
  "anthropic_aliases": [],
  "openai": ["gpt-5.4", "gpt-5.4-mini", "o4-mini"],
  "openai_aliases": ["gpt-5.4", "gpt-5.4-mini"]
}
EOF
  touch "$HOME/.cache/gh-crfix/models.json"

  run get_openai_models
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "gpt-5.4"
  echo "$output" | grep -q "o4-mini"
}

@test "get_openai_models: deduplicates entries" {
  cat > "$HOME/.cache/gh-crfix/models.json" <<'EOF'
{
  "anthropic": [],
  "anthropic_aliases": [],
  "openai": ["gpt-5.4", "gpt-5.4-mini"],
  "openai_aliases": ["gpt-5.4", "gpt-5.4-mini"]
}
EOF
  touch "$HOME/.cache/gh-crfix/models.json"

  run get_openai_models
  [ "$status" -eq 0 ]
  local count
  count="$(echo "$output" | grep -c "gpt-5.4-mini")"
  [ "$count" -eq 1 ]
}
