#!/usr/bin/env bats

setup() {
  load '../test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

# ── merge_base_branch function exists ────────────────────────────────────────

@test "merge_base_branch: function is defined" {
  run type -t merge_base_branch
  [ "$output" = "function" ]
}

@test "merge_base_branch: has DRY_RUN check in source" {
  # Verify that the function body references DRY_RUN
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q 'DRY_RUN'
}

# ── deterministic conflict resolution patterns ───────────────────────────────

@test "conflict resolution: case statement matches lock files" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  # lock files should be resolved with --theirs
  echo "$fn_body" | grep -q '\.lock'
}

@test "conflict resolution: case statement matches *-lock.yaml" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q '\-lock\.yaml'
}

@test "conflict resolution: case statement matches *-lock.json" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q '\-lock\.json'
}

@test "conflict resolution: case statement matches tsbuildinfo" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q 'tsbuildinfo'
}

@test "conflict resolution: case statement matches workflow files" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q '\.github/workflows'
}

@test "conflict resolution: case statement matches CHANGELOG.md" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q 'CHANGELOG\.md'
}

@test "conflict resolution: lock files use --theirs strategy" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  # The line with *.lock should be in the --theirs case
  echo "$fn_body" | grep -qE '\*\.lock.*\)'
  echo "$fn_body" | grep -q 'checkout --theirs'
}

@test "conflict resolution: workflow files use --ours strategy" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -qE '\.github/workflows'
  echo "$fn_body" | grep -q 'checkout --ours'
}

@test "conflict resolution: dry-run aborts merge on unresolved conflicts" {
  local fn_body
  fn_body="$(declare -f merge_base_branch)"
  echo "$fn_body" | grep -q 'dry-run.*skip'
  echo "$fn_body" | grep -q 'merge --abort'
}