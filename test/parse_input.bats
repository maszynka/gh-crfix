#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
  # Mock gh for bare-number parsing
  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"mock-owner/mock-repo\"}"
    fi
  '
}

teardown() { teardown_common; }

# ── URL parsing ──────────────────────────────────────────────────────────────

@test "parse_input: single PR URL" {
  parse_input "https://github.com/owner/repo/pull/93"
  [ "$OWNER_REPO" = "owner/repo" ]
  [ "$PR_NUMBERS" = "93" ]
}

@test "parse_input: range PR URL" {
  parse_input "https://github.com/owner/repo/pull/93-95"
  [ "$OWNER_REPO" = "owner/repo" ]
  [ "$PR_NUMBERS" = "93 94 95" ]
}

@test "parse_input: bracketed list URL" {
  parse_input "https://github.com/owner/repo/pull/[93,94,95]"
  [ "$OWNER_REPO" = "owner/repo" ]
  [ "$PR_NUMBERS" = "93 94 95" ]
}

@test "parse_input: URL with trailing slash" {
  parse_input "https://github.com/owner/repo/pull/93/"
  [ "$OWNER_REPO" = "owner/repo" ]
  [ "$PR_NUMBERS" = "93" ]
}

@test "parse_input: comma-separated URL" {
  parse_input "https://github.com/owner/repo/pull/1,2,3"
  [ "$OWNER_REPO" = "owner/repo" ]
  [ "$PR_NUMBERS" = "1 2 3" ]
}

@test "parse_input: single-element range URL" {
  parse_input "https://github.com/owner/repo/pull/5-5"
  [ "$OWNER_REPO" = "owner/repo" ]
  [ "$PR_NUMBERS" = "5" ]
}

# ── Bare number parsing (no URL, uses mocked gh) ────────────────────────────

@test "parse_input: bare single number" {
  parse_input "42"
  [ "$OWNER_REPO" = "mock-owner/mock-repo" ]
  [ "$PR_NUMBERS" = "42" ]
}

@test "parse_input: bare range" {
  parse_input "10-12"
  [ "$OWNER_REPO" = "mock-owner/mock-repo" ]
  [ "$PR_NUMBERS" = "10 11 12" ]
}

@test "parse_input: bare comma-separated" {
  parse_input "1,2,3"
  [ "$OWNER_REPO" = "mock-owner/mock-repo" ]
  [ "$PR_NUMBERS" = "1 2 3" ]
}

@test "parse_input: bare bracketed" {
  parse_input "[7,8,9]"
  [ "$OWNER_REPO" = "mock-owner/mock-repo" ]
  [ "$PR_NUMBERS" = "7 8 9" ]
}

# ── Edge cases ───────────────────────────────────────────────────────────────

@test "parse_input: large range generates correct count" {
  parse_input "https://github.com/o/r/pull/1-20"
  local count=0
  for _ in $PR_NUMBERS; do count=$((count + 1)); done
  [ "$count" -eq 20 ]
}

@test "parse_input: invalid input dies" {
  run bash -c "source '$SCRIPT_PATH'; parse_input 'abc' 2>&1"
  [ "$status" -ne 0 ]
}

@test "parse_input: different owner/repo extracted correctly" {
  parse_input "https://github.com/maszynka/cdstn-turbo/pull/100-102"
  [ "$OWNER_REPO" = "maszynka/cdstn-turbo" ]
  [ "$PR_NUMBERS" = "100 101 102" ]
}