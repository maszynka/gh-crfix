#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
  OWNER_REPO="test-owner/test-repo"
}

teardown() { teardown_common; }

# ── reply_and_resolve_all ────────────────────────────────────────────────────

@test "reply_and_resolve_all: with valid responses file" {
  # Create threads file
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[
  {"id": "PRRT_1", "comments": {"nodes": [{"body": "fix x"}]}},
  {"id": "PRRT_2", "comments": {"nodes": [{"body": "fix y"}]}}
]
EOF

  # Create responses file
  cat > "$TEST_TMPDIR/responses.json" <<'EOF'
[
  {"thread_id": "PRRT_1", "action": "fixed", "comment": "Fixed X"},
  {"thread_id": "PRRT_2", "action": "skipped", "comment": "Not applicable"}
]
EOF

  # Mock gh to succeed silently
  mock_command "gh" 0

  run reply_and_resolve_all 42 "$TEST_TMPDIR/threads.json" "$TEST_TMPDIR/responses.json"
  [ "$status" -eq 0 ]
  assert_output --partial "Replied & resolved 2/2"
}

@test "reply_and_resolve_all: without responses file uses fallback" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[
  {"id": "PRRT_1", "comments": {"nodes": [{"body": "fix"}]}},
  {"id": "PRRT_2", "comments": {"nodes": [{"body": "fix"}]}}
]
EOF

  mock_command "gh" 0

  run reply_and_resolve_all 42 "$TEST_TMPDIR/threads.json" "$TEST_TMPDIR/nonexistent.json"
  [ "$status" -eq 0 ]
  assert_output --partial "Replied & resolved 2/2"
}

@test "reply_and_resolve_all: malformed responses file uses fallback" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[{"id": "PRRT_1", "comments": {"nodes": [{"body": "fix"}]}}]
EOF

  echo "NOT JSON" > "$TEST_TMPDIR/bad.json"
  mock_command "gh" 0

  run reply_and_resolve_all 42 "$TEST_TMPDIR/threads.json" "$TEST_TMPDIR/bad.json"
  [ "$status" -eq 0 ]
  assert_output --partial "Replied & resolved 1/1"
}

@test "reply_and_resolve_all: empty threads" {
  echo "[]" > "$TEST_TMPDIR/threads.json"
  echo "[]" > "$TEST_TMPDIR/responses.json"

  mock_command "gh" 0

  run reply_and_resolve_all 42 "$TEST_TMPDIR/threads.json" "$TEST_TMPDIR/responses.json"
  [ "$status" -eq 0 ]
  assert_output --partial "0/0"
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