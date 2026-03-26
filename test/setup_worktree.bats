#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script

  # Create a real git repo for worktree tests
  REPO_DIR="$TEST_TMPDIR/repo"
  create_test_repo "$REPO_DIR" "https://github.com/test/repo.git"

  # Create a branch to use for worktrees
  git -C "$REPO_DIR" checkout -q -b test-branch
  touch "$REPO_DIR/test-file"
  git -C "$REPO_DIR" add .
  git -C "$REPO_DIR" commit -q -m "test commit"
  git -C "$REPO_DIR" checkout -q master 2>/dev/null || git -C "$REPO_DIR" checkout -q main

  WORKTREE_SUBDIR=".gh-crfix/worktrees"
}

teardown() {
  # Clean up worktrees before removing tmpdir
  git -C "$REPO_DIR" worktree list --porcelain 2>/dev/null | grep '^worktree ' | awk '{print $2}' | while read wt; do
    [ "$wt" != "$REPO_DIR" ] && git -C "$REPO_DIR" worktree remove --force "$wt" 2>/dev/null || true
  done
  teardown_common
}

@test "setup_worktree: creates new worktree for local branch" {
  result="$(setup_worktree 42 "$REPO_DIR" "test-branch" 2>/dev/null)"
  [ -d "$result" ]
  # Verify correct branch
  local branch
  branch="$(git -C "$result" branch --show-current)"
  [ "$branch" = "test-branch" ]
}

@test "setup_worktree: returns expected path" {
  result="$(setup_worktree 42 "$REPO_DIR" "test-branch" 2>/dev/null)"
  [ "$result" = "$REPO_DIR/$WORKTREE_SUBDIR/pr-42" ]
}

@test "setup_worktree: existing worktree on correct branch reuses it" {
  # Create worktree first time
  first="$(setup_worktree 42 "$REPO_DIR" "test-branch" 2>/dev/null)"
  # Call again
  second="$(setup_worktree 42 "$REPO_DIR" "test-branch" 2>/dev/null)"
  [ "$first" = "$second" ]
}

@test "setup_worktree: detects branch in another worktree" {
  # Create worktree at a custom path (simulating existing worktree)
  local custom_path="$REPO_DIR/$WORKTREE_SUBDIR/custom-name"
  mkdir -p "$(dirname "$custom_path")"
  git -C "$REPO_DIR" worktree add "$custom_path" "test-branch" 2>/dev/null

  # Now setup_worktree for pr-99 should find the branch already checked out
  result="$(setup_worktree 99 "$REPO_DIR" "test-branch" 2>/dev/null)"
  # Should reuse the custom path, not create pr-99
  [ "$result" = "$custom_path" ]
}

@test "setup_worktree: logs to stderr, path on stdout" {
  # Capture stderr separately
  local stdout stderr
  stdout="$(setup_worktree 42 "$REPO_DIR" "test-branch" 2>"$TEST_TMPDIR/stderr")"
  stderr="$(cat "$TEST_TMPDIR/stderr")"

  # stdout should be just the path (no git messages)
  [ "$stdout" = "$REPO_DIR/$WORKTREE_SUBDIR/pr-42" ]
  # stderr should have log messages
  echo "$stderr" | grep -qiE "creat|exist|branch"
}