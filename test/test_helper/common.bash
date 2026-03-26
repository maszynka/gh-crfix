#!/usr/bin/env bash
# Shared test helper for gh-fix tests

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT_PATH="$SCRIPT_DIR/gh-fix"

load 'test_helper/bats-support/load'
load 'test_helper/bats-assert/load'

# Create temp dirs for each test
setup_common() {
  TEST_TMPDIR="$(mktemp -d)"
  export LOG_DIR="$TEST_TMPDIR/log"
  mkdir -p "$LOG_DIR/kv"

  # Mock bin directory — prepended to PATH
  MOCK_BIN="$TEST_TMPDIR/mock-bin"
  mkdir -p "$MOCK_BIN"
  export PATH="$MOCK_BIN:$PATH"

  # Track mock calls
  MOCK_CALLS="$TEST_TMPDIR/mock-calls"
  mkdir -p "$MOCK_CALLS"
  export MOCK_CALLS
}

teardown_common() {
  rm -rf "$TEST_TMPDIR"
}

# Source the script functions without running main
source_script() {
  # Temporarily relax strict mode for sourcing
  set +euo pipefail 2>/dev/null || true
  source "$SCRIPT_PATH"
  set +e  # bats needs non-errexit
}

# Alias for tests that used the old v2-specific name
source_script_v2() {
  source_script
}

# Create a mock command that logs calls and returns canned output
# Usage: mock_command <name> <exit_code> [output]
mock_command() {
  local name="$1" exit_code="${2:-0}" output="${3:-}"
  cat > "$MOCK_BIN/$name" <<MOCK_EOF
#!/usr/bin/env bash
echo "\$@" >> "$MOCK_CALLS/${name}.log"
$([ -n "$output" ] && echo "echo '$output'")
exit $exit_code
MOCK_EOF
  chmod +x "$MOCK_BIN/$name"
}

# Create a mock command that runs a custom script body
# Usage: mock_command_script <name> <script_body>
mock_command_script() {
  local name="$1" body="$2"
  cat > "$MOCK_BIN/$name" <<MOCK_EOF
#!/usr/bin/env bash
echo "\$@" >> "$MOCK_CALLS/${name}.log"
$body
MOCK_EOF
  chmod +x "$MOCK_BIN/$name"
}

# Assert a mock was called with specific args (grep pattern)
assert_mock_called() {
  local name="$1" pattern="$2"
  local log="$MOCK_CALLS/${name}.log"
  [ -f "$log" ] || fail "Mock '$name' was never called"
  grep -q "$pattern" "$log" || fail "Mock '$name' not called with '$pattern'. Calls: $(cat "$log")"
}

# Assert a mock was called N times
assert_mock_call_count() {
  local name="$1" expected="$2"
  local log="$MOCK_CALLS/${name}.log"
  local actual=0
  [ -f "$log" ] && actual=$(wc -l < "$log" | tr -d ' ')
  [ "$actual" -eq "$expected" ] || fail "Mock '$name' called $actual times, expected $expected"
}

# Create a minimal git repo for testing
create_test_repo() {
  local dir="$1" remote_url="${2:-https://github.com/test-owner/test-repo.git}"
  mkdir -p "$dir"
  git -C "$dir" init -q
  git -C "$dir" remote add origin "$remote_url" 2>/dev/null || true
  # Create initial commit so branches work
  touch "$dir/.gitkeep"
  git -C "$dir" add .
  git -C "$dir" commit -q -m "init" --allow-empty
}
