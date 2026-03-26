#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
  OWNER_REPO="test-owner/test-repo"
}

teardown() { teardown_common; }

# ── post_fix_review_cycle ────────────────────────────────────────────────────

@test "post_fix_review_cycle: returns immediately when NO_POST_FIX=true" {
  NO_POST_FIX=true
  DRY_RUN=false
  local log_file="$TEST_TMPDIR/test.log"
  : > "$log_file"

  run post_fix_review_cycle 42 "/tmp/fake-worktree" 3 "$log_file"
  [ "$status" -eq 0 ]
  # Should not have written anything to log (returns before the block)
  [ ! -s "$log_file" ]
}

@test "post_fix_review_cycle: returns immediately when DRY_RUN=true" {
  NO_POST_FIX=false
  DRY_RUN=true
  local log_file="$TEST_TMPDIR/test.log"
  : > "$log_file"

  run post_fix_review_cycle 42 "/tmp/fake-worktree" 3 "$log_file"
  [ "$status" -eq 0 ]
  # Should not have written anything to log (returns before the block)
  [ ! -s "$log_file" ]
}

@test "post_fix_review_cycle: returns immediately when both flags true" {
  NO_POST_FIX=true
  DRY_RUN=true
  local log_file="$TEST_TMPDIR/test.log"
  : > "$log_file"

  run post_fix_review_cycle 42 "/tmp/fake-worktree" 3 "$log_file"
  [ "$status" -eq 0 ]
  [ ! -s "$log_file" ]
}

# ── post_fix_summary ─────────────────────────────────────────────────────────

@test "post_fix_summary: respects DRY_RUN — does not post comment" {
  DRY_RUN=true

  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [{"thread_id": "T1"}, {"thread_id": "T2"}],
  "skip": [{"thread_id": "T1"}],
  "auto": [],
  "already_likely_fixed": [],
  "needs_llm": [{"thread_id": "T2"}]
}
EOF

  cat > "$TEST_TMPDIR/responses.json" <<'EOF'
[{"thread_id": "T2", "action": "fixed", "comment": "done"}]
EOF

  local log_file="$TEST_TMPDIR/summary.log"
  : > "$log_file"

  # Mock gh to detect if it gets called
  mock_command "gh" 0 ""

  run post_fix_summary 42 "$TEST_TMPDIR/triage.json" "$TEST_TMPDIR/responses.json" "$log_file"
  [ "$status" -eq 0 ]

  # gh should NOT have been called since DRY_RUN=true
  [ ! -f "$MOCK_CALLS/gh.log" ]
}

@test "post_fix_summary: function is defined" {
  run type -t post_fix_summary
  [ "$output" = "function" ]
}