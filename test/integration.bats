#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common

  # Skip post-fix cycle in tests (avoids 90s sleep)
  export GH_CRFIX_REVIEW_WAIT=0

  # Create test git repo
  REPO_DIR="$TEST_TMPDIR/repo"
  create_test_repo "$REPO_DIR" "https://github.com/test-owner/test-repo.git"
  REMOTE_DIR="$TEST_TMPDIR/remote.git"
  git init --bare -q "$REMOTE_DIR"
  git -C "$REPO_DIR" remote remove origin
  git -C "$REPO_DIR" remote add origin "$REMOTE_DIR"
  export GH_CRFIX_DIR="$REPO_DIR"
  cd "$REPO_DIR"

  # Create PR branches
  for branch in pr-branch-1 pr-branch-2 pr-branch-3; do
    git checkout -q -b "$branch"
    touch "$branch-file"
    touch "file.ts"
    git add .
    git commit -q -m "commit on $branch"
    git checkout -q master 2>/dev/null || git checkout -q main
  done

  DEFAULT_BRANCH="$(git -C "$REPO_DIR" symbolic-ref --short HEAD)"
  for branch in "$DEFAULT_BRANCH" pr-branch-1 pr-branch-2 pr-branch-3; do
    git -C "$REPO_DIR" push -q -u origin "$branch"
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
  local pr_state="${1:-OPEN}" thread_count="${2:-1}" model_exit="${3:-0}" backend="${4:-claude}"

  # Mock gh with routing logic (including --jq support)
  mock_command_script "gh" "
    # Extract --jq argument if present
    jq_expr=''
    args=()
    while [ \$# -gt 0 ]; do
      if [ \"\$1\" = '--jq' ]; then shift; jq_expr=\"\$1\"
      elif [ \"\$1\" = '-q' ]; then shift; jq_expr=\"\$1\"
      else args+=(\"\$1\"); fi
      shift
    done
    apply_jq() { if [ -n \"\$jq_expr\" ]; then echo \"\$1\" | jq -r \"\$jq_expr\"; else echo \"\$1\"; fi; }

    all=\"\${args[*]}\"
    if echo \"\$all\" | grep -q 'auth status'; then
      true
    elif echo \"\$all\" | grep -q 'repo view'; then
      apply_jq '{\"nameWithOwner\":\"test-owner/test-repo\"}'
    elif echo \"\$all\" | grep -q 'pr view'; then
      if [ -n \"\$jq_expr\" ]; then
        echo '$DEFAULT_BRANCH'
      else
        pr_num=\$(echo \"\$all\" | grep -oE '[0-9]+' | head -1)
        echo \"{\\\"headRefName\\\":\\\"pr-branch-1\\\",\\\"title\\\":\\\"Test PR #\${pr_num}\\\",\\\"state\\\":\\\"$pr_state\\\",\\\"isDraft\\\":false}\"
      fi
    elif echo \"\$all\" | grep -q 'api graphql'; then
      if echo \"\$all\" | grep -q 'reviewThreads'; then
        if [ $thread_count -eq 0 ]; then
          json='{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}'
        else
          json='{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_t1\",\"isResolved\":false,\"isOutdated\":false,\"path\":\"file.ts\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"Fix\",\"path\":\"file.ts\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"rev\"},\"createdAt\":\"2025-01-01T00:00:00Z\"}]}}]}}}}}'
        fi
        apply_jq \"\$json\"
      elif echo \"\$all\" | grep -q 'resolveReviewThread\|addPullRequestReviewThreadReply'; then
        echo '{}'
      fi
    elif echo \"\$all\" | grep -q 'pr comment'; then
      true
    elif echo \"\$all\" | grep -q 'pr edit'; then
      true
    elif echo \"\$all\" | grep -q 'requested_reviewers'; then
      true
    fi
  "

  if [ "$backend" = "claude" ]; then
    mock_command_script "claude" "
      cat > thread-responses.json <<'CREOF'
[{\"thread_id\":\"PRRT_t1\",\"action\":\"fixed\",\"comment\":\"Fixed as requested\"}]
CREOF
      exit $model_exit
    "
  else
    mock_command_script "codex" "
      cat > thread-responses.json <<'CREOF'
[{\"thread_id\":\"PRRT_t1\",\"action\":\"fixed\",\"comment\":\"Fixed as requested\"}]
CREOF
      exit $model_exit
    "
  fi

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
}

@test "integration: PR with no unresolved threads skipped" {
  # Mock gh with jq passthrough for graphql calls
  mock_command_script "gh" '
    jq_expr=""
    args=()
    while [ $# -gt 0 ]; do
      if [ "$1" = "--jq" ] || [ "$1" = "-q" ]; then shift; jq_expr="$1"
      else args+=("$1"); fi
      shift
    done
    all="${args[*]}"
    if echo "$all" | grep -q "auth status"; then
      true
    elif echo "$all" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$all" | grep -q "pr view"; then
      if [ -n "$jq_expr" ]; then
        echo "'"$DEFAULT_BRANCH"'"
      else
        echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Test PR\",\"state\":\"OPEN\",\"isDraft\":false}"
      fi
    elif echo "$all" | grep -q "api graphql"; then
      json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}"
      # Check if --jq was passed and apply it
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
  assert_output --partial "nothing to process"
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
  assert_output --partial "nothing to process"
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

@test "integration: codex backend happy path" {
  setup_mocks "OPEN" 1 0 "codex"

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui --ai-backend codex --gate-model gpt-5.4-mini --fix-model gpt-5.4 "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "AI backend : codex"
  assert_output --partial "1 fixed"
}

@test "integration: prepare phase clamps high concurrency" {
  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --no-tui --dry-run -c 20 "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "Concurrency: 20"
  assert_output --partial "Prepare    : 8"
}

@test "integration: summary counts are correct for skipped PR" {
  mock_command_script "gh" '
    jq_expr=""
    args=()
    while [ $# -gt 0 ]; do
      if [ "$1" = "--jq" ] || [ "$1" = "-q" ]; then shift; jq_expr="$1"
      else args+=("$1"); fi
      shift
    done
    all="${args[*]}"
    if echo "$all" | grep -q "auth status"; then
      true
    elif echo "$all" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$all" | grep -q "pr view"; then
      if [ -n "$jq_expr" ]; then
        echo "'"$DEFAULT_BRANCH"'"
      else
        echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Test PR\",\"state\":\"OPEN\",\"isDraft\":false}"
      fi
    elif echo "$all" | grep -q "api graphql"; then
      json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}"
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
  assert_output --partial "nothing to process"
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
    jq_expr=""
    args=()
    while [ $# -gt 0 ]; do
      if [ "$1" = "--jq" ] || [ "$1" = "-q" ]; then shift; jq_expr="$1"
      else args+=("$1"); fi
      shift
    done
    apply_jq() { if [ -n "$jq_expr" ]; then echo "$1" | jq -r "$jq_expr"; else echo "$1"; fi; }
    all="${args[*]}"

    if echo "$all" | grep -q "auth status"; then
      true
    elif echo "$all" | grep -q "repo view"; then
      apply_jq "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$all" | grep -q "pr view"; then
      if [ -n "$jq_expr" ]; then echo "'"$DEFAULT_BRANCH"'"
      else echo "{\"headRefName\":\"pr-branch-1\",\"title\":\"Draft PR\",\"state\":\"OPEN\",\"isDraft\":true}"; fi
    elif echo "$all" | grep -q "api graphql"; then
      if echo "$all" | grep -q "reviewThreads"; then
        json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_1\",\"isResolved\":false,\"isOutdated\":false,\"path\":\"x.ts\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"fix\",\"path\":\"x.ts\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"r\"},\"createdAt\":\"2025-01-01T00:00:00Z\"}]}}]}}}}}"
        apply_jq "$json"
      else echo "{}"; fi
    elif echo "$all" | grep -q "pr comment\|pr edit\|requested_reviewers"; then
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
  assert_output --partial "1 fixed"
}

@test "integration: post-fix merges base branch when no new comments" {
  # Mock that returns 0 unresolved threads on the second check (post-fix)
  local call_count_file="$TEST_TMPDIR/graphql_calls"
  echo "0" > "$call_count_file"

  mock_command_script "gh" '
    if echo "$@" | grep -q "auth status"; then
      true
    elif echo "$@" | grep -q "repo view"; then
      echo "{\"nameWithOwner\":\"test-owner/test-repo\"}"
    elif echo "$@" | grep -q "pr view"; then
      if echo "$@" | grep -q "baseRefName"; then
        # gh pr view with -q extracts the value
        echo "'"$DEFAULT_BRANCH"'"
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
          json="{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_1\",\"isResolved\":false,\"isOutdated\":false,\"path\":\"x.ts\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"fix\",\"path\":\"x.ts\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"r\"},\"createdAt\":\"2025-01-01T00:00:00Z\"}]}}]}}}}}"
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
  export GH_CRFIX_REVIEW_WAIT=0
  run bash "$SCRIPT_PATH" --seq --no-tui "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  # post_fix_review_cycle writes to log file, not stdout — check overall success
  assert_output --partial "1 fixed"
}

@test "integration: merge conflict in setup stops processing early" {
  local base_branch
  base_branch="$(git -C "$REPO_DIR" symbolic-ref --short HEAD)"

  cat > "$REPO_DIR/conflict.txt" <<'EOF'
base
EOF
  git -C "$REPO_DIR" add conflict.txt
  git -C "$REPO_DIR" commit -q -m "add conflict base"

  git -C "$REPO_DIR" checkout -q pr-branch-1
  cat > "$REPO_DIR/conflict.txt" <<'EOF'
pr branch change
EOF
  git -C "$REPO_DIR" add conflict.txt
  git -C "$REPO_DIR" commit -q -m "change on pr branch"
  git -C "$REPO_DIR" push -q origin pr-branch-1

  git -C "$REPO_DIR" checkout -q "$base_branch"
  cat > "$REPO_DIR/conflict.txt" <<'EOF'
base branch change
EOF
  git -C "$REPO_DIR" add conflict.txt
  git -C "$REPO_DIR" commit -q -m "change on base branch"
  git -C "$REPO_DIR" push -q origin "$base_branch"

  mock_command_script "gh" "
    jq_expr=''
    args=()
    while [ \$# -gt 0 ]; do
      if [ \"\$1\" = '--jq' ] || [ \"\$1\" = '-q' ]; then shift; jq_expr=\"\$1\"
      else args+=(\"\$1\"); fi
      shift
    done
    apply_jq() { if [ -n \"\$jq_expr\" ]; then echo \"\$1\" | jq -r \"\$jq_expr\"; else echo \"\$1\"; fi; }
    all=\"\${args[*]}\"

    if echo \"\$all\" | grep -q 'auth status'; then
      true
    elif echo \"\$all\" | grep -q 'repo view'; then
      apply_jq '{\"nameWithOwner\":\"test-owner/test-repo\"}'
    elif echo \"\$all\" | grep -q 'pr view'; then
      if [ -n \"\$jq_expr\" ]; then
        echo '$base_branch'
      else
        echo '{\"headRefName\":\"pr-branch-1\",\"title\":\"Conflict PR\",\"state\":\"OPEN\",\"isDraft\":false}'
      fi
    elif echo \"\$all\" | grep -q 'api graphql'; then
      json='{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[{\"id\":\"PRRT_t1\",\"isResolved\":false,\"isOutdated\":false,\"path\":\"conflict.txt\",\"line\":1,\"comments\":{\"nodes\":[{\"body\":\"Fix\",\"path\":\"conflict.txt\",\"line\":1,\"originalLine\":1,\"author\":{\"login\":\"rev\"},\"createdAt\":\"2025-01-01T00:00:00Z\"}]}}]}}}}}'
      apply_jq \"\$json\"
    fi
  "
  mock_command "osascript" 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui --dry-run "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "Could not merge base branch"
  assert_output --partial "Threads total      : 1"
  run git -C "$REPO_DIR/.gh-crfix/worktrees/pr-1" status --short
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "integration: dry-run does not push merge or cleanup commits" {
  local base_branch before_sha after_sha
  base_branch="$(git -C "$REPO_DIR" symbolic-ref --short HEAD)"

  git -C "$REPO_DIR" checkout -q pr-branch-1
  cat > "$REPO_DIR/thread-responses.json" <<'EOF'
[]
EOF
  git -C "$REPO_DIR" add thread-responses.json
  git -C "$REPO_DIR" commit -q -m "add tracked thread responses artifact"
  git -C "$REPO_DIR" push -q origin pr-branch-1

  git -C "$REPO_DIR" checkout -q "$base_branch"
  cat > "$REPO_DIR/base-change.txt" <<'EOF'
base update
EOF
  git -C "$REPO_DIR" add base-change.txt
  git -C "$REPO_DIR" commit -q -m "update base branch"
  git -C "$REPO_DIR" push -q origin "$base_branch"

  before_sha="$(git -C "$REMOTE_DIR" rev-parse refs/heads/pr-branch-1)"

  setup_mocks "OPEN" 1 0

  cd "$REPO_DIR"
  run bash "$SCRIPT_PATH" --seq --no-tui --dry-run "https://github.com/test-owner/test-repo/pull/1"
  [ "$status" -eq 0 ]
  assert_output --partial "Mode       : DRY RUN"
  assert_output --partial "dry-run"

  after_sha="$(git -C "$REMOTE_DIR" rev-parse refs/heads/pr-branch-1)"
  [ "$before_sha" = "$after_sha" ]
}
