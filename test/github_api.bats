#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
  OWNER_REPO="test-owner/test-repo"
}

teardown() { teardown_common; }

# ── reply_and_resolve_from_responses ─────────────────────────────────────────

@test "reply_and_resolve_from_responses: with valid responses file" {
  cat > "$TEST_TMPDIR/responses.json" <<'EOF'
[
  {"thread_id": "PRRT_1", "action": "fixed", "comment": "Fixed X"},
  {"thread_id": "PRRT_2", "action": "skipped", "comment": "Not applicable"}
]
EOF

  mock_command "gh" 0

  run reply_and_resolve_from_responses "$TEST_TMPDIR/responses.json"
  [ "$status" -eq 0 ]
  assert_output "replied=2 resolved=1 unresolved_skipped=1"
}

@test "reply_and_resolve_from_responses: missing file returns empty output" {
  mock_command "gh" 0

  run reply_and_resolve_from_responses "$TEST_TMPDIR/nonexistent.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "reply_and_resolve_from_responses: malformed file returns empty output" {
  echo "NOT JSON" > "$TEST_TMPDIR/bad.json"
  mock_command "gh" 0

  run reply_and_resolve_from_responses "$TEST_TMPDIR/bad.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "reply_and_resolve_from_responses: empty array" {
  echo "[]" > "$TEST_TMPDIR/responses.json"
  mock_command "gh" 0

  run reply_and_resolve_from_responses "$TEST_TMPDIR/responses.json"
  [ "$status" -eq 0 ]
  assert_output "replied=0 resolved=0 unresolved_skipped=0"
}

@test "reply_and_resolve_from_responses: RESOLVE_SKIPPED resolves skipped entries" {
  cat > "$TEST_TMPDIR/responses.json" <<'EOF'
[
  {"thread_id": "PRRT_1", "action": "skipped", "comment": "Skipped but resolve"}
]
EOF

  RESOLVE_SKIPPED=true
  mock_command "gh" 0

  run reply_and_resolve_from_responses "$TEST_TMPDIR/responses.json"
  [ "$status" -eq 0 ]
  assert_output "replied=1 resolved=1 unresolved_skipped=0"
}

# ── resolve_thread ───────────────────────────────────────────────────────────

@test "resolve_thread: calls gh api graphql" {
  mock_command "gh" 0
  run resolve_thread "PRRT_test123"
  [ "$status" -eq 0 ]
  assert_mock_called "gh" "graphql"
  assert_mock_called "gh" "PRRT_test123"
}

# ── reply_to_thread ──────────────────────────────────────────────────────────

@test "reply_to_thread: calls gh api graphql with body" {
  mock_command "gh" 0
  run reply_to_thread "PRRT_abc" "Fixed the issue"
  [ "$status" -eq 0 ]
  assert_mock_called "gh" "graphql"
  assert_mock_called "gh" "PRRT_abc"
}

# ── request_copilot_review ───────────────────────────────────────────────────

@test "request_copilot_review: calls gh pr edit" {
  mock_command "gh" 0
  request_copilot_review 42
  assert_mock_called "gh" "pr edit"
  assert_mock_called "gh" "copilot-pull-request-reviewer"
}
