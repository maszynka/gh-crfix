#!/usr/bin/env bash
# registry/update.sh — fetch live model lists from Anthropic + OpenAI APIs
#
# Requires:
#   ANTHROPIC_API_KEY  — Anthropic API key
#   OPENAI_API_KEY     — OpenAI API key
#
# Usage:
#   bash registry/update.sh                    # update registry/models.json in-place
#   ANTHROPIC_API_KEY=sk-... bash registry/update.sh  # with explicit keys
#
# The script merges live API data with known aliases and writes models.json.
# Safe to run repeatedly — only updates if API calls succeed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUT_FILE="$SCRIPT_DIR/models.json"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

ANTHROPIC_RAW="$TMP_DIR/anthropic.txt"
OPENAI_RAW="$TMP_DIR/openai.txt"

# ── Anthropic ───────────────────────────────────────────────────────────────

fetch_anthropic() {
  if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "WARN: ANTHROPIC_API_KEY not set — using cached Anthropic models" >&2
    jq -r '.anthropic[]' "$OUT_FILE" 2>/dev/null > "$ANTHROPIC_RAW" || true
    return 0
  fi

  echo "Fetching Anthropic models..." >&2
  if curl --max-time 15 -fsS https://api.anthropic.com/v1/models \
    -H "x-api-key: ${ANTHROPIC_API_KEY}" \
    -H "anthropic-version: 2023-06-01" \
    | jq -r '.data[].id' \
    | grep -E '^claude-' \
    | sort -u > "$ANTHROPIC_RAW"; then
    echo "  $(wc -l < "$ANTHROPIC_RAW" | tr -d ' ') Anthropic models fetched" >&2
  else
    echo "WARN: Anthropic API call failed — using cached models" >&2
    jq -r '.anthropic[]' "$OUT_FILE" 2>/dev/null > "$ANTHROPIC_RAW" || true
  fi
}

# ── OpenAI ──────────────────────────────────────────────────────────────────

fetch_openai() {
  if [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "WARN: OPENAI_API_KEY not set — using cached OpenAI models" >&2
    jq -r '.openai[]' "$OUT_FILE" 2>/dev/null > "$OPENAI_RAW" || true
    return 0
  fi

  echo "Fetching OpenAI models..." >&2
  if curl --max-time 15 -fsS https://api.openai.com/v1/models \
    -H "Authorization: Bearer ${OPENAI_API_KEY}" \
    | jq -r '.data[].id' \
    | grep -E '^(gpt-[45]|o[134]-|o[134]$)' \
    | grep -vE '(audio|realtime|search|tts|whisper|dall-e|embedding)' \
    | sort -u > "$OPENAI_RAW"; then
    echo "  $(wc -l < "$OPENAI_RAW" | tr -d ' ') OpenAI models fetched" >&2
  else
    echo "WARN: OpenAI API call failed — using cached models" >&2
    jq -r '.openai[]' "$OUT_FILE" 2>/dev/null > "$OPENAI_RAW" || true
  fi
}

# ── Build aliases ───────────────────────────────────────────────────────────

build_anthropic_aliases() {
  local models_file="$1"
  {
    # Always include short names
    echo "opus"
    echo "sonnet"
    echo "haiku"
    # Generate family aliases: claude-opus-4-6, claude-sonnet-4-5, etc.
    sed -n 's/^\(claude-[a-z]*-[0-9]*-[0-9]*\)-.*/\1/p' "$models_file" | sort -u
  } | sort -u
}

build_openai_aliases() {
  local models_file="$1"
  {
    # Include base model names without date suffixes
    sed -n 's/^\(gpt-[0-9.]*\(-mini\|-nano\)\?\)-.*/\1/p' "$models_file"
    sed -n 's/^\(o[0-9]*\(-mini\)\?\)-.*/\1/p' "$models_file"
    # Also include exact matches that don't have date suffixes
    grep -vE '-[0-9]{4}' "$models_file" || true
  } | sort -u
}

# ── Merge + write ───────────────────────────────────────────────────────────

fetch_anthropic
fetch_openai

ANTHROPIC_ALIASES="$TMP_DIR/anthropic_aliases.txt"
OPENAI_ALIASES="$TMP_DIR/openai_aliases.txt"

build_anthropic_aliases "$ANTHROPIC_RAW" > "$ANTHROPIC_ALIASES"
build_openai_aliases "$OPENAI_RAW" > "$OPENAI_ALIASES"

# Ensure files are non-empty (fallback to empty array)
[ -s "$ANTHROPIC_RAW" ] || echo "" > "$ANTHROPIC_RAW"
[ -s "$OPENAI_RAW" ] || echo "" > "$OPENAI_RAW"
[ -s "$ANTHROPIC_ALIASES" ] || echo "" > "$ANTHROPIC_ALIASES"
[ -s "$OPENAI_ALIASES" ] || echo "" > "$OPENAI_ALIASES"

jq -n \
  --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --rawfile anthropic_raw "$ANTHROPIC_RAW" \
  --rawfile openai_raw "$OPENAI_RAW" \
  --rawfile anthropic_aliases_raw "$ANTHROPIC_ALIASES" \
  --rawfile openai_aliases_raw "$OPENAI_ALIASES" \
  '{
    updated_at: $updated_at,
    anthropic: ($anthropic_raw | split("\n") | map(select(length > 0))),
    anthropic_aliases: ($anthropic_aliases_raw | split("\n") | map(select(length > 0))),
    openai: ($openai_raw | split("\n") | map(select(length > 0))),
    openai_aliases: ($openai_aliases_raw | split("\n") | map(select(length > 0)))
  }' > "$OUT_FILE.tmp"

mv "$OUT_FILE.tmp" "$OUT_FILE"
echo "Updated $OUT_FILE ($(date -u +%Y-%m-%dT%H:%M:%SZ))" >&2
