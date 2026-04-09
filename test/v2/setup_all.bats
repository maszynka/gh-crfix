#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script

  REPO_DIR="$TEST_TMPDIR/repo"
  create_test_repo "$REPO_DIR" "https://github.com/test-owner/test-repo.git"
  cd "$REPO_DIR"
  echo "dirty" > tracked.txt
  git add tracked.txt
  git commit -q -m "add tracked file"
  echo "dirty change" > tracked.txt

  OWNER_REPO="test-owner/test-repo"
  PR_NUMBERS="1"
  SETUP_ONLY=false
  LOG_DIR="$TEST_TMPDIR/logs"
  mkdir -p "$LOG_DIR/kv"

  detect_autofix_hook() { :; }
  detect_validate_runner() { :; }
  setup_worktree() { echo "$REPO_DIR"; }
  get_unresolved_threads() { echo '[]'; }

  mock_command_script "gh" '
    if echo "$@" | grep -q "pr view"; then
      echo "{\"headRefName\":\"feature/test\",\"title\":\"Dirty Worktree PR\",\"state\":\"OPEN\",\"isDraft\":false}"
    fi
  '
}

teardown() {
  cd /
  teardown_common
}

@test "setup_all: skips PR when worktree is dirty before merge" {
  run setup_all "$REPO_DIR"
  [ "$status" -eq 0 ]
  assert_output --partial "Worktree is dirty before processing"
  [ -z "$(kv_list ready)" ]
  [ "$(kv_list skipped)" = "1" ]
}

@test "setup_all: case-colliding worktree is queued for worker normalization" {
  echo "dirty change" > tracked.txt
  get_unresolved_threads() { echo '[{"id":"t1"}]'; }
  detect_case_collisions() { echo "apps/Foo.ts | apps/foo.ts"; }

  run setup_all "$REPO_DIR"
  [ "$status" -eq 0 ]
  assert_output --partial "Case collisions    : detected"
  [ "$(kv_list ready)" = "1" ]
  [ "$(progress_get_status "1" "normalize_case")" = "pending" ]
  [ "$(progress_get_note "1" "normalize_case")" = "1 group(s) detected" ]
}

@test "setup_all: skips PR when there are no unresolved threads" {
  git -C "$REPO_DIR" checkout tracked.txt
  run setup_all "$REPO_DIR"
  [ "$status" -eq 0 ]
  assert_output --partial "No unresolved threads"
  [ -z "$(kv_list ready)" ]
  [ "$(kv_list skipped)" = "1" ]
}

@test "setup_phase_concurrency: clamps to configured max" {
  CONCURRENCY=20

  run setup_phase_concurrency
  [ "$status" -eq 0 ]
  [ "$output" = "8" ]
}
