#!/bin/sh
# POSIX-sh end-to-end scenarios for the Go port of gh-crfix.
#
# Companion to test/e2e/go-port.sh (which covers the dry-run happy path).
# This file covers the lighter-weight scenarios that don't need Go's
# concurrency primitives: CLOSED PR, zero unresolved threads, --setup-only,
# and the deterministic gate-skip path where no claude call should occur.
#
# Each scenario runs in its own tmpdir so nothing leaks between them. The
# script prints `PASS:` / `FAIL:` per assertion and returns non-zero if any
# scenario failed.
#
# Usage:
#   sh test/e2e/go-port-scenarios.sh
#
# Environment:
#   GOPORT_BIN  optional path to a pre-built binary; skip `go build`
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

# --- 1. Build (unless GOPORT_BIN points at an existing binary) ----------------
if [ -z "${GOPORT_BIN:-}" ]; then
  mkdir -p "$REPO_ROOT/bin"
  GOPORT_BIN="$REPO_ROOT/bin/gh-crfix-go"
  echo "[go-port-scenarios] building $GOPORT_BIN"
  (cd "$REPO_ROOT" && go build -o "$GOPORT_BIN" ./cmd/gh-crfix)
fi
[ -x "$GOPORT_BIN" ] || { echo "FAIL: $GOPORT_BIN not executable"; exit 1; }

TOTAL_FAIL=0

# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------

# setup_fixture <sandbox>
#
# Creates the canonical bare-origin + local repo pair inside $sandbox. The
# caller is expected to drop `gh` + `claude` stubs in $sandbox/stubs BEFORE
# invoking the binary.
setup_fixture() {
  sandbox="$1"
  mkdir -p "$sandbox/stubs" "$sandbox/home/.config" "$sandbox/home/.cache"
  REPO_DIR="$sandbox/repo"
  ORIGIN_DIR="$sandbox/origin.git"
  git init -q -b main "$REPO_DIR"
  git -C "$REPO_DIR" config user.email "e2e@test.local"
  git -C "$REPO_DIR" config user.name "E2E Test"
  echo "# e2e" > "$REPO_DIR/README.md"
  git -C "$REPO_DIR" add README.md
  git -C "$REPO_DIR" commit -q -m "init"
  git init -q --bare -b main "$ORIGIN_DIR"
  git -C "$REPO_DIR" remote add origin "$ORIGIN_DIR"
  git -C "$REPO_DIR" push -q -u origin main
  git -C "$REPO_DIR" checkout -q -b feat-test
  git -C "$REPO_DIR" commit -q --allow-empty -m "feat"
  git -C "$REPO_DIR" push -q -u origin feat-test
  git -C "$REPO_DIR" checkout -q main
}

# write_codex_stub <stubdir>
#
# Drops an always-failing codex stub so ai.Detect picks claude first.
write_codex_stub() {
  cat > "$1/codex" <<'STUB'
#!/bin/sh
exit 1
STUB
  chmod +x "$1/codex"
}

# write_claude_stub <stubdir>
#
# Default claude stub: gate -> JSON, fix -> write thread-responses.json.
write_claude_stub() {
  cat > "$1/claude" <<'STUB'
#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--json-schema" ]; then
    printf '{"structured_output":{"needs_advanced_model":true,"reason":"e2e-fake","threads_to_fix":["PRRT_test"]}}\n'
    exit 0
  fi
done
cat > thread-responses.json <<'JSON'
[
  {"thread_id":"PRRT_test","action":"fixed","comment":"e2e fake fix"}
]
JSON
exit 0
STUB
  chmod +x "$1/claude"
}

# write_claude_recording_stub <stubdir> <log-path>
#
# claude variant that appends its args to <log-path>. Used by the gate-skip
# scenario to assert claude was never called.
write_claude_recording_stub() {
  log="$2"
  # shellcheck disable=SC2016
  cat > "$1/claude" <<STUB
#!/bin/sh
printf '%s\n' "\$*" >> "$log"
for arg in "\$@"; do
  if [ "\$arg" = "--json-schema" ]; then
    printf '{"structured_output":{"needs_advanced_model":false,"reason":"e2e-fake","threads_to_fix":[]}}\n'
    exit 0
  fi
done
echo "[]" > thread-responses.json
exit 0
STUB
  chmod +x "$1/claude"
}

# run_binary <sandbox> [--output PATH] [binary args...]
#
# Invokes the Go binary inside the sandbox with the stubs on PATH and an
# isolated HOME. Redirects combined output to <sandbox>/output.txt and
# returns the binary's exit status.
run_binary() {
  sandbox="$1"
  shift
  out="$sandbox/output.txt"
  set +e
  PATH="$sandbox/stubs:$PATH" \
    HOME="$sandbox/home" \
    GH_CRFIX_DIR="$sandbox/repo" \
    GH_CRFIX_NO_NOTIFY=1 \
    XDG_CONFIG_HOME="$sandbox/home/.config" \
    XDG_CACHE_HOME="$sandbox/home/.cache" \
    "$GOPORT_BIN" "$@" >"$out" 2>&1
  rc=$?
  set -e
  return $rc
}

# check_contains <output-file> <label> <pattern>
#
# Asserts the file contains pattern; prints PASS/FAIL line and updates
# SCENARIO_FAIL on miss.
check_contains() {
  f="$1"; label="$2"; pat="$3"
  if grep -q -- "$pat" "$f"; then
    echo "PASS: $label"
  else
    echo "FAIL: $label (expected pattern: $pat)"
    SCENARIO_FAIL=$((SCENARIO_FAIL + 1))
  fi
}

# check_not_contains <output-file> <label> <pattern>
check_not_contains() {
  f="$1"; label="$2"; pat="$3"
  if grep -q -- "$pat" "$f"; then
    echo "FAIL: $label (unexpected pattern: $pat)"
    SCENARIO_FAIL=$((SCENARIO_FAIL + 1))
  else
    echo "PASS: $label"
  fi
}

# ---------------------------------------------------------------------------
# Scenario 1: PR CLOSED
# ---------------------------------------------------------------------------
scenario_pr_closed() {
  echo "━━ scenario: PR CLOSED ━━"
  SCENARIO_FAIL=0
  sandbox=$(mktemp -d -t gh-crfix-closed-XXXXXX)
  trap 'rm -rf "$sandbox"' RETURN 2>/dev/null || true
  setup_fixture "$sandbox"

  cat > "$sandbox/stubs/gh" <<'STUB'
#!/bin/sh
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      echo '{"headRefName":"feat-test","baseRefName":"main","title":"Closed PR","state":"CLOSED","isDraft":false,"headRefOid":"dead"}'
    else
      echo ok
    fi
    ;;
  repo) echo "acme/proj" ;;
  api)  echo "{}" ;;
  run)  echo "" ;;
  *)    echo "{}" ;;
esac
STUB
  chmod +x "$sandbox/stubs/gh"
  write_claude_stub "$sandbox/stubs"
  write_codex_stub "$sandbox/stubs"

  run_binary "$sandbox" \
    "https://github.com/acme/proj/pull/101" \
    --dry-run --no-tui --no-notify --no-post-fix
  rc=$?

  [ "$rc" = 0 ] && echo "PASS: exit 0" || { echo "FAIL: exit was $rc"; SCENARIO_FAIL=$((SCENARIO_FAIL + 1)); }
  check_contains "$sandbox/output.txt" "contains skipped"      "skipped"
  check_contains "$sandbox/output.txt" "contains PR is CLOSED" "PR is CLOSED"

  rm -rf "$sandbox"
  [ "$SCENARIO_FAIL" -eq 0 ] || TOTAL_FAIL=$((TOTAL_FAIL + SCENARIO_FAIL))
}

# ---------------------------------------------------------------------------
# Scenario 2: no unresolved threads
# ---------------------------------------------------------------------------
scenario_no_threads() {
  echo "━━ scenario: no unresolved threads ━━"
  SCENARIO_FAIL=0
  sandbox=$(mktemp -d -t gh-crfix-empty-XXXXXX)
  setup_fixture "$sandbox"

  cat > "$sandbox/stubs/gh" <<'STUB'
#!/bin/sh
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      echo '{"headRefName":"feat-test","baseRefName":"main","title":"Clean PR","state":"OPEN","isDraft":false,"headRefOid":"dead"}'
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*) echo '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}' ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
STUB
  chmod +x "$sandbox/stubs/gh"
  write_claude_stub "$sandbox/stubs"
  write_codex_stub "$sandbox/stubs"

  run_binary "$sandbox" \
    "https://github.com/acme/proj/pull/101" \
    --dry-run --no-tui --no-notify --no-post-fix
  rc=$?

  [ "$rc" = 0 ] && echo "PASS: exit 0" || { echo "FAIL: exit was $rc"; SCENARIO_FAIL=$((SCENARIO_FAIL + 1)); }
  check_contains "$sandbox/output.txt" "contains no unresolved threads" "no unresolved threads"

  rm -rf "$sandbox"
  [ "$SCENARIO_FAIL" -eq 0 ] || TOTAL_FAIL=$((TOTAL_FAIL + SCENARIO_FAIL))
}

# ---------------------------------------------------------------------------
# Scenario 4: --setup-only
# ---------------------------------------------------------------------------
scenario_setup_only() {
  echo "━━ scenario: --setup-only ━━"
  SCENARIO_FAIL=0
  sandbox=$(mktemp -d -t gh-crfix-setup-XXXXXX)
  setup_fixture "$sandbox"

  # Default gh stub from go-port.sh (inline copy, single-thread).
  cat > "$sandbox/stubs/gh" <<'STUB'
#!/bin/sh
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      echo '{"headRefName":"feat-test","baseRefName":"main","title":"E2E test PR","state":"OPEN","isDraft":false,"headRefOid":"dead"}'
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*)
        cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
  {"id":"PRRT_test","isResolved":false,"isOutdated":false,"line":10,"path":"README.md",
   "comments":{"nodes":[
     {"id":"IC_1","body":"please explain this line","path":"README.md","line":10,"originalLine":10,
      "author":{"login":"reviewer"},"createdAt":"2026-01-01T00:00:00Z"}
   ]}}
]}}}}}
JSON
        ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
STUB
  chmod +x "$sandbox/stubs/gh"
  write_claude_stub "$sandbox/stubs"
  write_codex_stub "$sandbox/stubs"

  run_binary "$sandbox" \
    "https://github.com/acme/proj/pull/101" \
    --setup-only --no-tui --no-notify --no-post-fix
  rc=$?

  [ "$rc" = 0 ] && echo "PASS: exit 0" || { echo "FAIL: exit was $rc"; SCENARIO_FAIL=$((SCENARIO_FAIL + 1)); }
  check_contains     "$sandbox/output.txt" "contains setup-only"            "setup-only"
  check_not_contains "$sandbox/output.txt" "does not run fix model"         "running fix model"
  check_not_contains "$sandbox/output.txt" "does not run gate model"        "running gate model"

  rm -rf "$sandbox"
  [ "$SCENARIO_FAIL" -eq 0 ] || TOTAL_FAIL=$((TOTAL_FAIL + SCENARIO_FAIL))
}

# ---------------------------------------------------------------------------
# Scenario 5: gate skip below threshold (LGTM thread, claude never called)
# ---------------------------------------------------------------------------
scenario_gate_skip() {
  echo "━━ scenario: gate skip below threshold ━━"
  SCENARIO_FAIL=0
  sandbox=$(mktemp -d -t gh-crfix-gate-XXXXXX)
  setup_fixture "$sandbox"
  claude_log="$sandbox/claude-calls.log"

  cat > "$sandbox/stubs/gh" <<'STUB'
#!/bin/sh
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      echo '{"headRefName":"feat-test","baseRefName":"main","title":"LGTM PR","state":"OPEN","isDraft":false,"headRefOid":"dead"}'
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*)
        cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
  {"id":"PRRT_lgtm","isResolved":false,"isOutdated":false,"line":1,"path":"README.md",
   "comments":{"nodes":[
     {"id":"IC_lgtm","body":"LGTM","path":"README.md","line":1,"originalLine":1,
      "author":{"login":"reviewer"},"createdAt":"2026-01-01T00:00:00Z"}
   ]}}
]}}}}}
JSON
        ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
STUB
  chmod +x "$sandbox/stubs/gh"
  write_claude_recording_stub "$sandbox/stubs" "$claude_log"
  write_codex_stub "$sandbox/stubs"

  run_binary "$sandbox" \
    "https://github.com/acme/proj/pull/101" \
    --dry-run --no-tui --no-notify --no-post-fix
  rc=$?

  [ "$rc" = 0 ] && echo "PASS: exit 0" || { echo "FAIL: exit was $rc"; SCENARIO_FAIL=$((SCENARIO_FAIL + 1)); }
  check_contains     "$sandbox/output.txt" "triage skip=1"          "triage: skip=1"
  check_contains     "$sandbox/output.txt" "triage needs_llm=0"     "needs_llm=0"
  check_not_contains "$sandbox/output.txt" "no gate model log"      "running gate model"

  if [ -s "$claude_log" ]; then
    echo "FAIL: claude was invoked unexpectedly: $(cat "$claude_log")"
    SCENARIO_FAIL=$((SCENARIO_FAIL + 1))
  else
    echo "PASS: claude was never invoked"
  fi

  rm -rf "$sandbox"
  [ "$SCENARIO_FAIL" -eq 0 ] || TOTAL_FAIL=$((TOTAL_FAIL + SCENARIO_FAIL))
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------
scenario_pr_closed
scenario_no_threads
scenario_setup_only
scenario_gate_skip

echo ""
if [ "$TOTAL_FAIL" -ne 0 ]; then
  echo "go-port scenarios: $TOTAL_FAIL check(s) failed"
  exit 1
fi
echo "go-port scenarios: all checks passed"
