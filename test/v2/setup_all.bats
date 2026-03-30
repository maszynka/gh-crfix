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
  merge_base_branch() { echo "merge should not run" >> "$TEST_TMPDIR/merge-called"; return 0; }

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
  [ ! -f "$TEST_TMPDIR/merge-called" ]
}

@test "setup_all: case-colliding worktree triggers automatic normalization before setup-only skip" {
  echo "dirty change" > tracked.txt
  SETUP_ONLY=true
  detect_case_collisions() { echo "apps/Foo.ts | apps/foo.ts"; }
  case_collision_groups_json() { echo '[["apps/Foo.ts","apps/foo.ts"]]'; }
  handle_case_collisions() {
    git -C "$3" checkout -- tracked.txt >/dev/null 2>&1
    progress_set "$2" normalize_case done "1 group normalized"
    return 0
  }

  run setup_all "$REPO_DIR"
  [ "$status" -eq 0 ]
  assert_output --partial "cd $REPO_DIR"
  [ "$(progress_get_status "1" "normalize_case")" = "done" ]
  [ "$(progress_get_note "1" "normalize_case")" = "1 group normalized" ]
}
