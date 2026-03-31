#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
  LOG_DIR="$TEST_TMPDIR/logs"
  mkdir -p "$LOG_DIR"
}

teardown() { teardown_common; }

@test "progress_snapshot: hides skipped resolve_conflicts when not needed" {
  progress_init "123"
  progress_set "123" normalize_case skipped "not needed"
  progress_set "123" merge_base done " merge not needed - branch up to date "
  progress_set "123" resolve_conflicts skipped " not needed "
  progress_set "123" fetch_threads done " 3 unresolved thread(s) "

  run progress_snapshot "123"
  [ "$status" -eq 0 ]
  [[ "$output" == *"merge not needed - branch up to date"* ]]
  [[ "$output" == *"3 unresolved thread(s)"* ]]
  [[ "$output" != *"normalizing case-colliding tracked paths"* ]]
  [[ "$output" != *"optionally resolving conflicts and pushing"* ]]
}

@test "progress_snapshot: keeps other skipped steps visible" {
  progress_init "123"
  progress_set "123" autofix skipped " no hook configured "

  run progress_snapshot "123"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'skipped\trunning deterministic autofix hook\tno hook configured'* ]]
}
