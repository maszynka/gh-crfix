#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

# ── is_question_only ─────────────────────────────────────────────────────────

@test "is_question_only: returns true for 'Could you explain why?'" {
  run is_question_only "Could you explain why?"
  [ "$status" -eq 0 ]
}

@test "is_question_only: returns false for 'Please rename this'" {
  run is_question_only "Please rename this"
  [ "$status" -ne 0 ]
}

@test "is_question_only: returns false for question with actionable keyword" {
  run is_question_only "Can you rename this variable?"
  [ "$status" -ne 0 ]
}

@test "is_question_only: returns true for 'What about this approach?'" {
  run is_question_only "What about this approach?"
  [ "$status" -eq 0 ]
}

# ── is_simple_mechanical ─────────────────────────────────────────────────────

@test "is_simple_mechanical: returns true for 'nit: fix typo'" {
  run is_simple_mechanical "nit: fix typo"
  [ "$status" -eq 0 ]
}

@test "is_simple_mechanical: returns false for 'refactor the auth flow'" {
  run is_simple_mechanical "refactor the auth flow"
  [ "$status" -ne 0 ]
}

@test "is_simple_mechanical: returns true for 'unused import on line 5'" {
  run is_simple_mechanical "unused import on line 5"
  [ "$status" -eq 0 ]
}

@test "is_simple_mechanical: returns true for 'eslint error here'" {
  run is_simple_mechanical "eslint error here"
  [ "$status" -eq 0 ]
}

@test "is_simple_mechanical: returns true for 'update changelog'" {
  run is_simple_mechanical "update changelog"
  [ "$status" -eq 0 ]
}

# ── is_non_actionable ────────────────────────────────────────────────────────

@test "is_non_actionable: returns true for 'lgtm'" {
  run is_non_actionable "lgtm"
  [ "$status" -eq 0 ]
}

@test "is_non_actionable: returns false for 'please fix this'" {
  run is_non_actionable "please fix this"
  [ "$status" -ne 0 ]
}

@test "is_non_actionable: returns true for 'looks good to me'" {
  run is_non_actionable "looks good to me"
  [ "$status" -eq 0 ]
}

@test "is_non_actionable: returns true for 'thanks!'" {
  run is_non_actionable "thanks!"
  [ "$status" -eq 0 ]
}

# ── classify_one_thread ──────────────────────────────────────────────────────

@test "classify_one_thread: returns skip for question-only thread" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/index.ts"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_q1",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/index.ts",
  "line": 10,
  "comments": {
    "nodes": [
      {
        "id": "C1",
        "body": "Could you explain why?",
        "path": "src/index.ts",
        "line": 10,
        "originalLine": 10,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  decision="$(echo "$result" | jq -r '.decision')"
  reason="$(echo "$result" | jq -r '.reason')"

  [ "$decision" = "skip" ]
  [ "$reason" = "question-only thread" ]
}

@test "classify_one_thread: returns needs_llm for complex review comment" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/app.ts"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_c1",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/app.ts",
  "line": 20,
  "comments": {
    "nodes": [
      {
        "id": "C2",
        "body": "This logic should handle edge cases for negative values and empty arrays",
        "path": "src/app.ts",
        "line": 20,
        "originalLine": 20,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  decision="$(echo "$result" | jq -r '.decision')"

  [ "$decision" = "needs_llm" ]
}

@test "classify_one_thread: returns skip for non-actionable lgtm" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/lib.ts"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_na1",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/lib.ts",
  "line": 5,
  "comments": {
    "nodes": [
      {
        "id": "C3",
        "body": "lgtm",
        "path": "src/lib.ts",
        "line": 5,
        "originalLine": 5,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  decision="$(echo "$result" | jq -r '.decision')"
  reason="$(echo "$result" | jq -r '.reason')"

  [ "$decision" = "skip" ]
  [ "$reason" = "non-actionable comment" ]
  [ "$(echo "$result" | jq -r '.resolve_when_skipped')" = "true" ]
}

@test "classify_one_thread: returns auto for mechanical nit" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/util.ts"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_m1",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/util.ts",
  "line": 3,
  "comments": {
    "nodes": [
      {
        "id": "C4",
        "body": "nit: fix typo in variable name",
        "path": "src/util.ts",
        "line": 3,
        "originalLine": 3,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  decision="$(echo "$result" | jq -r '.decision')"

  [ "$decision" = "auto" ]
}

# ── E2E seed comment regression tests ────────────────────────────────────────
# These comments are the exact wording used in the E2E test to seed review
# threads. They must classify as needs_llm — if is_simple_mechanical() ever
# starts matching them, the E2E test will silently skip the LLM fix step.

@test "classify_one_thread: e2e typo comment classifies as needs_llm" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/utils.py"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_e2e1",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/utils.py",
  "line": 15,
  "comments": {
    "nodes": [
      {
        "id": "C_e2e1",
        "body": "The parameter is named `frist_name` but should be `first_name` — this breaks the f-string interpolation and any callers using the function. Please rename it everywhere in the function signature and body.",
        "path": "src/utils.py",
        "line": 15,
        "originalLine": 15,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  [ "$(echo "$result" | jq -r '.decision')" = "needs_llm" ]
}

@test "classify_one_thread: e2e unused-import comment classifies as needs_llm" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/utils.py"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_e2e2",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/utils.py",
  "line": 4,
  "comments": {
    "nodes": [
      {
        "id": "C_e2e2",
        "body": "The `sys` module is imported on line 4 but never referenced anywhere in this file. Please remove it to keep the module imports clean.",
        "path": "src/utils.py",
        "line": 4,
        "originalLine": 4,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  [ "$(echo "$result" | jq -r '.decision')" = "needs_llm" ]
}

@test "classify_one_thread: e2e comparison-operator comment classifies as needs_llm" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/validator.js"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_e2e3",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/validator.js",
  "line": 11,
  "comments": {
    "nodes": [
      {
        "id": "C_e2e3",
        "body": "`isPositiveNumber(0)` now incorrectly returns true. Zero is not positive — use strict `>` instead of `>=`.",
        "path": "src/validator.js",
        "line": 11,
        "originalLine": 11,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  [ "$(echo "$result" | jq -r '.decision')" = "needs_llm" ]
}

@test "classify_one_thread: e2e config-value comment classifies as needs_llm" {
  mkdir -p "$TEST_TMPDIR/worktree/src"
  touch "$TEST_TMPDIR/worktree/src/config.py"

  local thread_json
  thread_json=$(cat <<'EOF'
{
  "id": "PRRT_e2e4",
  "isResolved": false,
  "isOutdated": false,
  "path": "src/config.py",
  "line": 2,
  "comments": {
    "nodes": [
      {
        "id": "C_e2e4",
        "body": "DEFAULT_TIMEOUT should be 60 seconds to match the production default, not 30.",
        "path": "src/config.py",
        "line": 2,
        "originalLine": 2,
        "author": {"login": "reviewer"},
        "createdAt": "2025-01-01T00:00:00Z"
      }
    ]
  }
}
EOF
  )

  result="$(classify_one_thread "$TEST_TMPDIR/worktree" "$thread_json")"
  [ "$(echo "$result" | jq -r '.decision')" = "needs_llm" ]
}
