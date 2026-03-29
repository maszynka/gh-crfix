#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

@test "detect_builtin_test_command: chooses pnpm test when package.json has test script" {
  cat > "$TEST_TMPDIR/package.json" <<'EOF'
{
  "packageManager": "pnpm@10.0.0",
  "scripts": {
    "test": "vitest run"
  }
}
EOF

  result="$(detect_builtin_test_command "$TEST_TMPDIR")"
  [ "$result" = "pnpm test" ]
}

@test "detect_validate_runner: prefers repo validate hook over builtin command" {
  mkdir -p "$TEST_TMPDIR/.gh-crfix"
  cat > "$TEST_TMPDIR/.gh-crfix/validate.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$TEST_TMPDIR/.gh-crfix/validate.sh"

  cat > "$TEST_TMPDIR/package.json" <<'EOF'
{
  "scripts": {
    "test": "npm test"
  }
}
EOF

  result="$(detect_validate_runner "$TEST_TMPDIR")"
  [ "$result" = "hook::$TEST_TMPDIR/.gh-crfix/validate.sh" ]
}

@test "run_validation: no runner writes noop result" {
  mkdir -p "$TEST_TMPDIR/worktree"

  run run_validation "$TEST_TMPDIR/worktree" "" "$TEST_TMPDIR/out.json" "$TEST_TMPDIR/log.txt"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.available' < "$TEST_TMPDIR/out.json")" = "false" ]
  [ "$(jq -r '.tests_failed' < "$TEST_TMPDIR/out.json")" = "false" ]
}

@test "run_validation: hook failure is recorded as tests_failed" {
  mkdir -p "$TEST_TMPDIR/worktree"
  cat > "$TEST_TMPDIR/validate.sh" <<'EOF'
#!/usr/bin/env bash
echo "2 tests failed"
exit 1
EOF
  chmod +x "$TEST_TMPDIR/validate.sh"

  run run_validation "$TEST_TMPDIR/worktree" "hook::$TEST_TMPDIR/validate.sh" "$TEST_TMPDIR/out.json" "$TEST_TMPDIR/log.txt"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.available' < "$TEST_TMPDIR/out.json")" = "true" ]
  [ "$(jq -r '.tests_failed' < "$TEST_TMPDIR/out.json")" = "true" ]
  [ "$(jq -r '.summary' < "$TEST_TMPDIR/out.json")" = "2 tests failed" ]
}
