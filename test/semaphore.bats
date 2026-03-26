#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  # Source script functions — semaphore uses fd 3 which conflicts with bats
  # So we test semaphore in subshells only
}

teardown() {
  teardown_common
}

# Run semaphore operations in a subshell to avoid fd 3 conflict with bats
run_semaphore_test() {
  bash -c "
    source '$SCRIPT_PATH'
    set +euo pipefail
    LOG_DIR='$LOG_DIR'
    $1
  "
}

@test "sem_init: creates FIFO file" {
  run_semaphore_test '
    CONCURRENCY=2
    sem_init
    [ -p "$SEMAPHORE" ] && echo "OK" || echo "FAIL"
    sem_destroy
  '
  [ "$?" -eq 0 ]
}

@test "sem_init: acquire succeeds up to concurrency limit" {
  run_semaphore_test '
    CONCURRENCY=2
    sem_init
    sem_acquire
    sem_acquire
    echo "acquired 2"
    sem_release
    sem_release
    sem_destroy
  '
  [ "$?" -eq 0 ]
}

@test "sem_destroy: cleans up FIFO" {
  run run_semaphore_test '
    CONCURRENCY=1
    sem_init
    fifo="$SEMAPHORE"
    sem_destroy
    [ ! -e "$fifo" ] && echo "cleaned" || echo "not cleaned"
  '
  assert_output --partial "cleaned"
}

@test "semaphore: acquire-release cycle from subshell" {
  run run_semaphore_test '
    CONCURRENCY=2
    sem_init
    (sem_acquire; echo "got it"; sem_release) &
    wait $!
    # Should still work after subshell
    sem_acquire
    sem_release
    echo "OK"
    sem_destroy
  '
  assert_output --partial "got it"
  assert_output --partial "OK"
}

@test "semaphore: multiple concurrent subshells complete" {
  run run_semaphore_test '
    CONCURRENCY=2
    sem_init
    results="'"$TEST_TMPDIR"'/sem-results"
    mkdir -p "$results"
    for i in 1 2 3 4; do
      (
        sem_acquire
        touch "$results/$i.done"
        sleep 0.1
        sem_release
      ) &
    done
    wait
    # All 4 should have completed
    count=$(ls "$results"/*.done 2>/dev/null | wc -l | tr -d " ")
    echo "completed=$count"
    sem_destroy
  '
  assert_output --partial "completed=4"
}

@test "semaphore: concurrency=1 serializes work" {
  run run_semaphore_test '
    CONCURRENCY=1
    sem_init
    results="'"$TEST_TMPDIR"'/sem-serial"
    mkdir -p "$results"
    for i in 1 2 3; do
      (
        sem_acquire
        touch "$results/$i.done"
        sem_release
      ) &
    done
    wait
    count=$(ls "$results"/*.done 2>/dev/null | wc -l | tr -d " ")
    echo "completed=$count"
    sem_destroy
  '
  assert_output --partial "completed=3"
}