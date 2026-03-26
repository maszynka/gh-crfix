#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

# ── pr_status ────────────────────────────────────────────────────────────────

@test "pr_status: no files = queued" {
  result="$(pr_status 42)"
  [ "$result" = "queued" ]
}

@test "pr_status: started file = running" {
  date +%s > "$LOG_DIR/pr-42.started"
  result="$(pr_status 42)"
  [ "$result" = "running" ]
}

@test "pr_status: status OK = done" {
  date +%s > "$LOG_DIR/pr-42.started"
  echo "OK" > "$LOG_DIR/pr-42.status"
  result="$(pr_status 42)"
  [ "$result" = "done" ]
}

@test "pr_status: status FAIL = failed" {
  date +%s > "$LOG_DIR/pr-42.started"
  echo "FAIL" > "$LOG_DIR/pr-42.status"
  result="$(pr_status 42)"
  [ "$result" = "failed" ]
}

# ── pr_elapsed ───────────────────────────────────────────────────────────────

@test "pr_elapsed: not started returns spaces" {
  result="$(pr_elapsed 42)"
  [ "$result" = "    " ]
}

@test "pr_elapsed: running shows time" {
  local now
  now="$(date +%s)"
  echo "$((now - 90))" > "$LOG_DIR/pr-42.started"
  result="$(pr_elapsed 42)"
  [ "$result" = "1:30" ]
}

@test "pr_elapsed: just started shows 0:00 or 0:01" {
  date +%s > "$LOG_DIR/pr-42.started"
  result="$(pr_elapsed 42)"
  # Allow for 0:00 or 0:01 depending on timing
  echo "$result" | grep -qE '^0:0[01]$'
}

# ── pr_last_line ─────────────────────────────────────────────────────────────

@test "pr_last_line: no log returns empty" {
  result="$(pr_last_line 42)"
  [ -z "$result" ]
}

@test "pr_last_line: returns last non-empty line" {
  printf "line one\nline two\nline three\n" > "$LOG_DIR/pr-42.log"
  result="$(pr_last_line 42)"
  [ "$result" = "line three" ]
}

@test "pr_last_line: skips trailing empty lines" {
  printf "line one\nline two\n\n\n" > "$LOG_DIR/pr-42.log"
  result="$(pr_last_line 42)"
  [ "$result" = "line two" ]
}

@test "pr_last_line: truncates to max_width" {
  printf "this is a very long line that should be truncated at some point because it exceeds the maximum width\n" > "$LOG_DIR/pr-42.log"
  result="$(pr_last_line 42 30)"
  [ ${#result} -le 30 ]
}

# ── pr_at_index ──────────────────────────────────────────────────────────────

@test "pr_at_index: first element" {
  TUI_READY_LIST="10 20 30"
  result="$(pr_at_index 0)"
  [ "$result" = "10" ]
}

@test "pr_at_index: middle element" {
  TUI_READY_LIST="10 20 30"
  result="$(pr_at_index 1)"
  [ "$result" = "20" ]
}

@test "pr_at_index: last element" {
  TUI_READY_LIST="10 20 30"
  result="$(pr_at_index 2)"
  [ "$result" = "30" ]
}

@test "pr_at_index: out of bounds returns empty" {
  TUI_READY_LIST="10 20 30"
  result="$(pr_at_index 5)"
  [ -z "$result" ]
}