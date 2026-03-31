#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

# ── build_gate_prompt ────────────────────────────────────────────────────────

@test "build_gate_prompt: includes repo name" {
  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [],
  "skip": [{"thread_id": "T1"}],
  "auto": [],
  "already_likely_fixed": [],
  "needs_llm": [{"thread_id": "T2", "path": "src/x.ts", "line": 1, "reason": "needs review", "body": "refactor this"}]
}
EOF

  result="$(build_gate_prompt "owner/test-repo" 42 "$TEST_TMPDIR/triage.json")"
  echo "$result" | grep -q "owner/test-repo"
}

@test "build_gate_prompt: includes PR number" {
  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [],
  "skip": [],
  "auto": [],
  "already_likely_fixed": [],
  "needs_llm": []
}
EOF

  result="$(build_gate_prompt "owner/repo" 99 "$TEST_TMPDIR/triage.json")"
  echo "$result" | grep -q "#99"
}

@test "build_gate_prompt: includes triage stats" {
  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [],
  "skip": [{"thread_id": "S1"}, {"thread_id": "S2"}, {"thread_id": "S3"}],
  "auto": [{"thread_id": "A1"}],
  "already_likely_fixed": [{"thread_id": "F1"}, {"thread_id": "F2"}],
  "needs_llm": [{"thread_id": "L1", "path": "a.ts", "line": 1, "reason": "complex", "body": "fix logic"}]
}
EOF

  result="$(build_gate_prompt "owner/repo" 10 "$TEST_TMPDIR/triage.json")"
  echo "$result" | grep -q "skipped: 3"
  echo "$result" | grep -q "auto/mechanical: 1"
  echo "$result" | grep -q "already_likely_fixed: 2"
  echo "$result" | grep -q "residual needs_llm candidates: 1"
}

@test "build_gate_prompt: includes validation and score context" {
  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [],
  "skip": [],
  "auto": [],
  "already_likely_fixed": [],
  "needs_llm": [{"thread_id": "L1", "path": "a.ts", "line": 1, "reason": "complex", "body": "fix logic"}]
}
EOF

  cat > "$TEST_TMPDIR/validation.json" <<'EOF'
{
  "tests_failed": true,
  "summary": "pnpm test failed in packages/api"
}
EOF

  cat > "$TEST_TMPDIR/gate-context.json" <<'EOF'
{
  "total_score": 1.4,
  "should_run_gate": true,
  "components": {
    "needs_llm": {"score": 1},
    "pr_comment": {"score": 0.4},
    "test_failure": {"score": 0}
  }
}
EOF

  result="$(build_gate_prompt "owner/repo" 10 "$TEST_TMPDIR/triage.json" "$TEST_TMPDIR/validation.json" "$TEST_TMPDIR/gate-context.json")"
  echo "$result" | grep -q "tests_failed: true"
  echo "$result" | grep -q "pnpm test failed in packages/api"
  echo "$result" | grep -q "total: 1.4"
  echo "$result" | grep -q "threshold met: true"
}

@test "build_gate_prompt: includes residual thread body" {
  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [],
  "skip": [],
  "auto": [],
  "already_likely_fixed": [],
  "needs_llm": [{"thread_id": "PRRT_xyz", "path": "src/auth.ts", "line": 55, "reason": "needs semantic review", "body": "This auth flow has a race condition"}]
}
EOF

  result="$(build_gate_prompt "owner/repo" 7 "$TEST_TMPDIR/triage.json")"
  echo "$result" | grep -q "PRRT_xyz"
  echo "$result" | grep -q "src/auth.ts"
  echo "$result" | grep -q "race condition"
}

@test "build_gate_context: sums residual comments and test failures" {
  SCORE_NEEDS_LLM=".2"
  SCORE_PR_COMMENT="0.4"
  SCORE_TEST_FAILURE="1"

  cat > "$TEST_TMPDIR/triage.json" <<'EOF'
{
  "all": [],
  "skip": [],
  "auto": [],
  "already_likely_fixed": [],
  "needs_llm": [
    {"thread_id": "L1", "reason": "needs semantic review"},
    {"thread_id": "L2", "reason": "PR-level comment (no file path)"}
  ]
}
EOF

  cat > "$TEST_TMPDIR/validation.json" <<'EOF'
{
  "tests_failed": true,
  "summary": "tests failed"
}
EOF

  run build_gate_context "$TEST_TMPDIR/triage.json" "$TEST_TMPDIR/validation.json" "$TEST_TMPDIR/out.json"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.total_score' < "$TEST_TMPDIR/out.json")" = "1.6" ]
  [ "$(jq -r '.should_run_gate' < "$TEST_TMPDIR/out.json")" = "true" ]
  [ "$(jq -r '.components.needs_llm.score' < "$TEST_TMPDIR/out.json")" = "0.2" ]
  [ "$(jq -r '.components.pr_comment.score' < "$TEST_TMPDIR/out.json")" = "0.4" ]
  [ "$(jq -r '.components.test_failure.score' < "$TEST_TMPDIR/out.json")" = "1" ]
}

# ── gate_schema ──────────────────────────────────────────────────────────────

@test "gate_schema: is valid JSON" {
  result="$(gate_schema)"
  echo "$result" | jq -e '.' >/dev/null 2>&1
}

@test "gate_schema: has required fields" {
  result="$(gate_schema)"
  echo "$result" | jq -e '.required | index("needs_advanced_model")' >/dev/null
  echo "$result" | jq -e '.required | index("reason")' >/dev/null
  echo "$result" | jq -e '.required | index("threads_to_fix")' >/dev/null
}

@test "gate_schema: needs_advanced_model is boolean type" {
  result="$(gate_schema)"
  type="$(echo "$result" | jq -r '.properties.needs_advanced_model.type')"
  [ "$type" = "boolean" ]
}

@test "gate_schema: threads_to_fix is array of strings" {
  result="$(gate_schema)"
  type="$(echo "$result" | jq -r '.properties.threads_to_fix.type')"
  items_type="$(echo "$result" | jq -r '.properties.threads_to_fix.items.type')"
  [ "$type" = "array" ]
  [ "$items_type" = "string" ]
}
