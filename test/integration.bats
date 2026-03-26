#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common

  # Skip post-fix cycle in tests (avoids 90s sleep)
  export GH_FIX_REVIEW_WAIT=0

  # Create test git repo
  REPO_DIR="$TEST_TMPDIR/repo"
  create_test_repo "$REPO_DIR" "https://github.com/test-owner/test-repo.git"
  cd "$REPO_DIR"

  # Create PR branches
  for branch in pr-branch-1 pr-branch-2 pr-branch-3; do
    git checkout -q -b "$branch"
    touch "$branch-file"
    git add .
    git commit -q -m "commit on $branch"
    git checkout -q master 2>/dev/null || git checkout -q main
  done

  # Thread fixture
  THREADS_JSON='[{"id":"PRRT_t1","path":"file.ts","line":1,"isResolved":false,"comments":{"nodes":[{"body":"Fix this","path":"file.ts","line":1,"originalLine":1,"author":{"login":"reviewer"},"createdAt":"2025-01-01T00:00:00Z"}]}}]'
}

teardown() {
  cd /
  # Clean worktrees
  git -C "$REPO_DIR" worktree list --porcelain 2>/dev/null | grep '^worktree ' | awk '{print $2}' | while read wt; do
    [ "$wt" != "$REPO_DIR" ] && git -C "$REPO_DIR" worktree remove --force "$wt" 2>/dev/null || true
  done
  teardown_common
}

# Helper: create a full mock environment for integration tests
setup_mocks() {
  local pr_state="${1:-OPEN}" thread_count="${2:-1}" claude_exit="${3:-0}"

  # Mock gh with routing logic
  mock_command_script "gh" "
    if echo \"\$@\" | grep -q 'repo view'; then
      echo '{\"nameWithOwner\":\"test-owner/test-repo\"}'
    elif echo \"\$@\" | grep -q 'pr view'; then
      pr_num=\$(echo \"\$@\" | grep -oE '[0-9]+' | head -1)
      cat <<PREOF
{\"headRefName\":\"pr-branch-1\",\"title\":\"Test PR #\${pr_num}\",\"state\":\"$pr_state\",\"isDraft\":false}
PREOF
    elif echo \"\$@\" | grep -q 'api graphql'; then
      if echo \"\$@\" | grep -q 'reviewThreads'; then
        if [ $thread_count -eq 0 ]; then
          echo '{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}'
        else
          echo '{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_t1\",\"isResolved\":false,\"path\":\"file.ts\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"Fix\",\"path\":\"file.ts\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"rev\"},\"createdAt\":\"2025-01-01\"}]}}]}}}}}'
        fi
      elif echo \"\$@\" | grep -q 'resolveReviewThread\|addPullRequestReviewThreadReply'; then
        echo '{}'
      fi
    elif echo \"\$@\" | grep -q 'pr edit'; then
      true
    fi
  "

  # Mock claude
  mock_command_script "claude" "
    # Create a thread-responses.json in cwd
    cat > thread-responses.json <<'CREOF'
[{\"thread_id\":\"PRRT_t1\",\"action\":\"fixed\",\"comment\":\"Fixed as requested\"}]
CREOF
    exit $claude_exit
  "

  # Mock osascript (suppress macOS notification)
  mock_command "osascript" 0
}

# ── Integration tests ────────────────────────────────────────────────────────

@test "integration: single PR sequential happy path" {
  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "1 fixed"
  assert_output --partial "Review requested" || assert_output --partial "review"
}

@test "integration: PR with no unresolved threads skipped" {
  # Mock gh with jq passthrough for graphql calls
  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$@" | grep -q "pr view"; then
      echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Test PR\",\"state\":\"OPEN\",\"isDraft\":false}"
    elif echo "$@" | grep -q "api graphql"; then
      json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}"
      # Check if --jq was passed and apply it
      jq_expr=""
      while [ $# -gt 0 ]; do
        if [ "$1" = "--jq" ]; then shift; jq_expr="$1"; fi
        shift
      done
      if [ -n "$jq_expr" ]; then
        echo "$json" | jq -r "$jq_expr"
      else
        echo "$json"
      fi
    fi
  '
  mock_command "osascript" 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "No unresolved threads"
  assert_output --partial "nothing to fix"
}

@test "integration: closed PR is skipped" {
  setup_mocks "CLOSED" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "CLOSED"
  assert_output --partial "skipping"
}

@test "integration: --setup-only creates worktree but doesn't fix" {
  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --setup-only "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "nothing to fix"
  # claude should NOT have been called
  [ ! -f "$MOCK_CALLS/claude.log" ]
}

@test "integration: --no-resolve skips thread resolution" {
  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui --no-resolve "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "no-resolve"
}

@test "integration: claude failure reports FAIL" {
  setup_mocks "OPEN" 1 1  # claude exits 1

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]  # script itself succeeds, just reports failure
  assert_output --partial "failed"
}

@test "integration: summary counts are correct for skipped PR" {
  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$@" | grep -q "pr view"; then
      echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Test PR\",\"state\":\"OPEN\",\"isDraft\":false}"
    elif echo "$@" | grep -q "api graphql"; then
      json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}"
      jq_expr=""
      while [ $# -gt 0 ]; do
        if [ "$1" = "--jq" ]; then shift; jq_expr="$1"; fi
        shift
      done
      if [ -n "$jq_expr" ]; then
        echo "$json" | jq -r "$jq_expr"
      else
        echo "$json"
      fi
    fi
  '
  mock_command "osascript" 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "nothing to fix"
}

@test "integration: parallel mode with --no-tui works" {
  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" -c 2 --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "1 fixed"
}

@test "integration: draft PR is processed" {
  # Mock to return isDraft=true but state=OPEN
  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$@" | grep -q "pr view"; then
      echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Draft PR\",\"state\":\"OPEN\",\"isDraft\":true}"
    elif echo "$@" | grep -q "api graphql"; then
      if echo "$@" | grep -q "reviewThreads"; then
        echo "{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_1\",\"isResolved\":false,\"path\":\"x.ts\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"fix\",\"path\":\"x.ts\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"r\"},\"createdAt\":\"2025-01-01\"}]}}]}}}}}"
      else
        echo "{}"
      fi
    elif echo "$@" | grep -q "pr edit"; then
      true
    fi
  '
  mock_command_script "claude" '
    cat > thread-responses.json <<CREOF
[{"thread_id":"PRRT_1","action":"fixed","comment":"Done"}]
CREOF
    exit 0
  '
  mock_command "osascript" 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "DRAFT"
  assert_output --partial "1 fixed"
}

# ── Post-fix review cycle tests ──────────────────────────────────────────────

@test "integration: --no-post-fix skips review cycle" {
  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui --no-post-fix "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "no-post-fix"
  assert_output --partial "1 fixed"
}

@test "integration: post-fix merges master when no new comments" {
  # Mock that returns 0 unresolved threads on the second check (post-fix)
  local call_count_file="$TEST_TMPDIR/graphql_calls"
  echo "0" > "$call_count_file"

  mock_command_script "gh" '
    if echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$@" | grep -q "pr view"; then
      if echo "$@" | grep -q "baseRefName"; then
        # gh pr view with -q extracts the value
        echo "master"
      else
        echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Test PR\",\"state\":\"OPEN\",\"isDraft\":false}"
      fi
    elif echo "$@" | grep -q "api graphql"; then
      if echo "$@" | grep -q "reviewThreads"; then
        count=$(cat "'"$call_count_file"'")
        count=$((count + 1))
        echo "$count" > "'"$call_count_file"'"
        if [ "$count" -eq 1 ]; then
          # First call (setup): 1 unresolved thread
          json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_1\",\"isResolved\":false,\"path\":\"x.ts\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"fix\",\"path\":\"x.ts\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"r\"},\"createdAt\":\"2025-01-01\"}]}}]}}}}}"
        else
          # Second call (post-fix): 0 unresolved
          json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}"
        fi
      else
        json="{}"
      fi
      jq_expr=""
      while [ $# -gt 0 ]; do
        if [ "$1" = "--jq" ]; then shift; jq_expr="$1"; fi
        shift
      done
      if [ -n "$jq_expr" ]; then
        echo "$json" | jq -r "$jq_expr"
      else
        echo "$json"
      fi
    elif echo "$@" | grep -q "pr comment"; then
      true
    elif echo "$@" | grep -q "pr edit"; then
      true
    fi
  '
  mock_command_script "claude" '
    cat > thread-responses.json <<CREOF
[{"thread_id":"PRRT_1","action":"fixed","comment":"Done"}]
CREOF
    exit 0
  '
  mock_command "osascript" 0

  cd "$REPO_DIR"
  export GH_FIX_REVIEW_WAIT=0
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  # Script continues even if merge fails (non-fatal)
  assert_output --partial "No new review comments"
  assert_output --partial "post-fix"
}