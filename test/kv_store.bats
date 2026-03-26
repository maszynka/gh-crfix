#!/usr/bin/env bats

setup() {
  load 'test_helper/common'
  setup_common
  source_script
}

teardown() { teardown_common; }

@test "kv_set and kv_get: basic string" {
  kv_set 42 title "Fix bug"
  result="$(kv_get 42 title)"
  [ "$result" = "Fix bug" ]
}

@test "kv_get: missing key returns empty" {
  result="$(kv_get 99 nonexistent)"
  [ -z "$result" ]
}

@test "kv_set: overwrite replaces value" {
  kv_set 1 name "old"
  kv_set 1 name "new"
  result="$(kv_get 1 name)"
  [ "$result" = "new" ]
}

@test "kv_set: multiple keys per id" {
  kv_set 10 title "PR title"
  kv_set 10 branch "main"
  kv_set 10 thread_count "5"
  [ "$(kv_get 10 title)" = "PR title" ]
  [ "$(kv_get 10 branch)" = "main" ]
  [ "$(kv_get 10 thread_count)" = "5" ]
}

@test "kv_set: value with spaces" {
  kv_set 1 title "Add multi-user chat with AI annotations"
  result="$(kv_get 1 title)"
  [ "$result" = "Add multi-user chat with AI annotations" ]
}

@test "kv_append and kv_list: multiple items" {
  kv_append ready 42
  kv_append ready 43
  kv_append ready 44
  result="$(kv_list ready)"
  echo "$result" | grep -q "42"
  echo "$result" | grep -q "43"
  echo "$result" | grep -q "44"
}

@test "kv_list: empty returns empty" {
  result="$(kv_list nonexistent)"
  [ -z "$result" ]
}

@test "kv_append: items are in order" {
  kv_append order "first"
  kv_append order "second"
  kv_append order "third"
  result="$(kv_list order)"
  [ "$(echo "$result" | head -1)" = "first" ]
  [ "$(echo "$result" | tail -1)" = "third" ]
}

@test "kv_set: different ids don't collide" {
  kv_set 1 title "PR one"
  kv_set 2 title "PR two"
  [ "$(kv_get 1 title)" = "PR one" ]
  [ "$(kv_get 2 title)" = "PR two" ]
}