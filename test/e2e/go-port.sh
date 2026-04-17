#!/bin/sh
# POSIX-sh end-to-end test for the Go port of gh-crfix.
#
# Builds the Go binary, stubs gh + claude on PATH, and runs a --dry-run
# invocation against a local throwaway git repo. This mirrors the Go e2e
# test in cmd/gh-crfix/main_e2e_test.go so both surfaces stay in sync.
#
# Usage:
#   sh test/e2e/go-port.sh
#
# Environment:
#   GOPORT_BIN  optional path to a pre-built binary; skip `go build`
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

# --- 1. Build (unless the caller passed GOPORT_BIN) --------------------------
if [ -z "${GOPORT_BIN:-}" ]; then
  mkdir -p "$REPO_ROOT/bin"
  GOPORT_BIN="$REPO_ROOT/bin/gh-crfix-go"
  echo "[go-port-e2e] building $GOPORT_BIN"
  (cd "$REPO_ROOT" && go build -o "$GOPORT_BIN" ./cmd/gh-crfix)
fi
[ -x "$GOPORT_BIN" ] || { echo "FAIL: $GOPORT_BIN not executable"; exit 1; }

# --- 2. Temp sandbox ---------------------------------------------------------
SANDBOX=$(mktemp -d -t gh-crfix-goport-XXXXXX)
trap 'rm -rf "$SANDBOX"' EXIT

STUB_DIR="$SANDBOX/stubs"
REPO_DIR="$SANDBOX/repo"
HOME_DIR="$SANDBOX/home"
mkdir -p "$STUB_DIR" "$REPO_DIR" "$HOME_DIR/.config" "$HOME_DIR/.cache"

# --- 3. Stub gh --------------------------------------------------------------
cat > "$STUB_DIR/gh" <<'STUB'
#!/bin/sh
mode=""
for arg in "$@"; do
  case "$arg" in
    pr)   mode="pr" ;;
    api)  mode="api" ;;
    run)  mode="run" ;;
    repo) mode="repo" ;;
  esac
done
case "$mode" in
  pr)
    verb=""
    seen_pr=0
    for arg in "$@"; do
      if [ "$seen_pr" = "1" ] && [ -z "$verb" ]; then
        verb="$arg"
        break
      fi
      if [ "$arg" = "pr" ]; then seen_pr=1; fi
    done
    if [ "$verb" = "view" ]; then
      cat <<'JSON'
{"headRefName":"feat-test","baseRefName":"main","title":"E2E test PR","state":"OPEN","isDraft":false,"headRefOid":"deadbeefcafebabe"}
JSON
    else
      echo "ok"
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
chmod +x "$STUB_DIR/gh"

# --- 4. Stub claude ----------------------------------------------------------
cat > "$STUB_DIR/claude" <<'STUB'
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
chmod +x "$STUB_DIR/claude"

# --- 5. Stub codex (always fail, keeps ai.Detect predictable) ---------------
cat > "$STUB_DIR/codex" <<'STUB'
#!/bin/sh
exit 1
STUB
chmod +x "$STUB_DIR/codex"

# --- 6. Local git repo -------------------------------------------------------
(
  cd "$REPO_DIR"
  git init -q -b main
  git config user.email "e2e@test.local"
  git config user.name "E2E Test"
  echo "# e2e" > README.md
  git add README.md
  git commit -q -m "init"
  git branch feat-test
)

# --- 7. Invoke the binary ----------------------------------------------------
OUTPUT_FILE="$SANDBOX/output.txt"
PATH="$STUB_DIR:$PATH" \
  HOME="$HOME_DIR" \
  GH_CRFIX_DIR="$REPO_DIR" \
  GH_CRFIX_NO_NOTIFY=1 \
  XDG_CONFIG_HOME="$HOME_DIR/.config" \
  XDG_CACHE_HOME="$HOME_DIR/.cache" \
  "$GOPORT_BIN" \
    "https://github.com/acme/proj/pull/101" \
    --dry-run --no-tui --no-notify --no-post-fix \
  > "$OUTPUT_FILE" 2>&1
EXIT=$?

echo "--- gh-crfix output ---"
cat "$OUTPUT_FILE"
echo "----------------------- (exit $EXIT)"

# --- 8. Assertions -----------------------------------------------------------
FAIL=0
check() {
  # $1: description, $2: grep-pattern expected in output
  if grep -q -- "$2" "$OUTPUT_FILE"; then
    echo "PASS: $1"
  else
    echo "FAIL: $1 (expected pattern: $2)"
    FAIL=$((FAIL + 1))
  fi
}

[ "$EXIT" = "0" ] && echo "PASS: exit 0" || { echo "FAIL: exit was $EXIT"; FAIL=$((FAIL + 1)); }
check "PR header line" "PR #"
check "Setup banner"  "Setup"
check "Done banner"   "Done"

# No panics.
if grep -q -E '(panic:|runtime error:|nil pointer)' "$OUTPUT_FILE"; then
  echo "FAIL: panic markers present"
  FAIL=$((FAIL + 1))
fi

if [ "$FAIL" -ne 0 ]; then
  echo "go-port e2e: $FAIL check(s) failed"
  exit 1
fi
echo "go-port e2e: all checks passed"
