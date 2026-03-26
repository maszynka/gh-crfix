#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

@test "build_prompt: contains PR number and repo" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[
  {
    "id": "PRRT_123",
    "path": "src/index.ts",
    "line": 42,
    "comments": {
      "nodes": [
        {"body": "Fix this bug", "path": "src/index.ts", "line": 42, "originalLine": 42, "author": {"login": "reviewer1"}}
      ]
    }
  }
]
EOF
  result="$(build_prompt "owner/repo" 93 "$TEST_TMPDIR/threads.json")"
  echo "$result" | grep -q "PR #93"
  echo "$result" | grep -q "owner/repo"
}

@test "build_prompt: contains thread details" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[
  {
    "id": "PRRT_abc",
    "path": "app.ts",
    "line": 10,
    "comments": {
      "nodes": [
        {"body": "Add validation here", "path": "app.ts", "line": 10, "originalLine": 10, "author": {"login": "bob"}}
      ]
    }
  }
]
EOF
  result="$(build_prompt "o/r" 1 "$TEST_TMPDIR/threads.json")"
  echo "$result" | grep -q "PRRT_abc"
  echo "$result" | grep -q "app.ts"
  echo "$result" | grep -q "Add validation here"
  echo "$result" | grep -q "@bob"
}

@test "build_prompt: multiple threads numbered correctly" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[
  {"id": "T1", "path": "a.ts", "line": 1, "comments": {"nodes": [{"body": "fix a", "path": "a.ts", "line": 1, "originalLine": 1, "author": {"login": "x"}}]}},
  {"id": "T2", "path": "b.ts", "line": 2, "comments": {"nodes": [{"body": "fix b", "path": "b.ts", "line": 2, "originalLine": 2, "author": {"login": "y"}}]}},
  {"id": "T3", "path": "c.ts", "line": 3, "comments": {"nodes": [{"body": "fix c", "path": "c.ts", "line": 3, "originalLine": 3, "author": {"login": "z"}}]}}
]
EOF
  result="$(build_prompt "o/r" 1 "$TEST_TMPDIR/threads.json")"
  echo "$result" | grep -q "Thread 1"
  echo "$result" | grep -q "Thread 2"
  echo "$result" | grep -q "Thread 3"
  echo "$result" | grep -q "3 unresolved"
}

@test "build_prompt: thread with multiple comments" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[
  {
    "id": "T1",
    "path": "x.ts",
    "line": 5,
    "comments": {
      "nodes": [
        {"body": "First comment", "path": "x.ts", "line": 5, "originalLine": 5, "author": {"login": "alice"}},
        {"body": "Follow-up", "path": "x.ts", "line": 5, "originalLine": 5, "author": {"login": "bob"}}
      ]
    }
  }
]
EOF
  result="$(build_prompt "o/r" 1 "$TEST_TMPDIR/threads.json")"
  echo "$result" | grep -q "First comment"
  echo "$result" | grep -q "Follow-up"
  echo "$result" | grep -q "@alice"
  echo "$result" | grep -q "@bob"
}

@test "build_prompt: contains thread-responses.json instructions" {
  cat > "$TEST_TMPDIR/threads.json" <<'EOF'
[{"id": "T1", "path": "a.ts", "line": 1, "comments": {"nodes": [{"body": "fix", "path": "a.ts", "line": 1, "originalLine": 1, "author": {"login": "x"}}]}}]
EOF
  result="$(build_prompt "o/r" 1 "$TEST_TMPDIR/threads.json")"
  echo "$result" | grep -q "thread-responses.json"
  echo "$result" | grep -q '"action"'
  echo "$result" | grep -q '"fixed"'
  echo "$result" | grep -q '"skipped"'
}

@test "build_prompt: zero threads" {
  echo "[]" > "$TEST_TMPDIR/threads.json"
  result="$(build_prompt "o/r" 1 "$TEST_TMPDIR/threads.json")"
  echo "$result" | grep -q "0 unresolved"
}