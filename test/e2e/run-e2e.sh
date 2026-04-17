#!/usr/bin/env bash
# E2E test for gh-crfix
#
# Creates a real PR with intentional bugs + review comments + a merge conflict,
# runs gh crfix, then verifies the fixes were applied, threads resolved, and
# conflict markers are gone.
#
# Requires: gh (authenticated with E2E_GH_TOKEN), claude CLI, ANTHROPIC_API_KEY
#
# Usage (local):
#   FIXTURE_DIR=/path/to/gh-crfix-e2e-fixtures bash test/e2e/run-e2e.sh
# Usage (CI): invoked by .github/workflows/e2e.yml — all env vars set there.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# GHCRFIX can be either the legacy bash script or the Go binary.
# Default: bash script at repo root; set GHCRFIX=/path/to/bin/gh-crfix for Go.
GHCRFIX="${GHCRFIX:-$SCRIPT_DIR/../../gh-crfix}"
FIXTURE_REPO="${FIXTURE_REPO:-maszynka/gh-crfix-e2e-fixtures}"
FIXTURE_DIR="${FIXTURE_DIR:-$SCRIPT_DIR/../../fixture-repo}"
E2E_BRANCH="e2e-test-$(date +%s)-$$"
PR_NUMBER=""
MAIN_SHA_BEFORE=""  # saved so cleanup can restore main

echo "=== gh-crfix E2E Test ==="
echo "Script : $GHCRFIX"
echo "Fixture: $FIXTURE_REPO  ($FIXTURE_DIR)"
echo "Branch : $E2E_BRANCH"
echo ""

# ── Cleanup (always runs) ────────────────────────────────────────────────────

cleanup() {
  local exit_code=$?
  echo ""
  echo "=== Cleanup ==="

  # Close PR and delete test branch
  if [ -n "${PR_NUMBER:-}" ]; then
    echo "Closing PR #$PR_NUMBER..."
    gh pr close "$PR_NUMBER" --repo "$FIXTURE_REPO" 2>/dev/null || true
  fi
  git -C "$FIXTURE_DIR" push origin --delete "$E2E_BRANCH" 2>/dev/null || true

  # Restore main to pre-test state (undo the conflict commit we pushed)
  if [ -n "${MAIN_SHA_BEFORE:-}" ]; then
    echo "Restoring main to $MAIN_SHA_BEFORE..."
    git -C "$FIXTURE_DIR" fetch origin main 2>/dev/null || true
    git -C "$FIXTURE_DIR" push origin "${MAIN_SHA_BEFORE}:refs/heads/main" --force 2>/dev/null \
      || echo "  (could not restore main — may need manual cleanup)"
  fi

  # Remove any stale gh-crfix worktrees inside the fixture repo
  git -C "$FIXTURE_DIR" worktree prune 2>/dev/null || true
  rm -rf "$FIXTURE_DIR/.gh-crfix" 2>/dev/null || true

  echo "Cleanup done."
  exit $exit_code
}
trap cleanup EXIT

# ── Preflight ────────────────────────────────────────────────────────────────

echo "=== Preflight ==="
command -v gh     >/dev/null || { echo "FAIL: gh CLI not found";     exit 1; }
command -v claude >/dev/null || { echo "FAIL: claude CLI not found"; exit 1; }
command -v jq     >/dev/null || { echo "FAIL: jq not found";         exit 1; }
[ -x "$GHCRFIX" ] || { echo "FAIL: $GHCRFIX not executable"; exit 1; }
[ -d "$FIXTURE_DIR/.git" ] || { echo "FAIL: $FIXTURE_DIR is not a git repo"; exit 1; }
echo "All checks passed."
echo ""

# ── Step 1: Create PR branch with intentional bugs ──────────────────────────

echo "=== Step 1: Creating buggy branch ==="
cd "$FIXTURE_DIR"
git fetch origin
git checkout main
git reset --hard origin/main
MAIN_SHA_BEFORE="$(git rev-parse HEAD)"
echo "Main is at $MAIN_SHA_BEFORE"

git checkout -b "$E2E_BRANCH"

# Portable in-place sed (BSD / GNU compatible).
sedi() { sed -i.bak "$@" && rm -f "${@: -1}.bak"; }

# Bug 1: typo in parameter name  (format_name in utils.py)
sedi 's/first_name/frist_name/g' src/utils.py

# Bug 2: unused import added     (utils.py)
sedi '3a\
import sys' src/utils.py

# Bug 3: wrong comparison operator  (validator.js — isPositiveNumber)
sedi 's/value > 0/value >= 0/' src/validator.js

# Bug 4: new file added on this branch — main will add a conflicting version
cat > src/config.py << 'PYEOF'
# Application configuration
DEFAULT_TIMEOUT = 30
MAX_RETRIES = 5
DEBUG = False
PYEOF

git add -A
git commit -m "chore: introduce test bugs for E2E (${E2E_BRANCH})"
git push -u origin "$E2E_BRANCH"
echo "Branch pushed: $E2E_BRANCH"
echo ""

# ── Step 2: Open PR ──────────────────────────────────────────────────────────

echo "=== Step 2: Opening PR ==="
PR_URL=$(gh pr create \
  --repo "$FIXTURE_REPO" \
  --head "$E2E_BRANCH" \
  --base main \
  --title "E2E test $(date +%Y-%m-%d-%H%M%S)" \
  --body "Automated E2E test for gh-crfix. Will be cleaned up automatically.")
PR_NUMBER="${PR_URL##*/}"
echo "PR created: #$PR_NUMBER ($PR_URL)"
echo ""

# ── Step 3: Add review comments ─────────────────────────────────────────────

echo "=== Step 3: Adding review comments ==="
sleep 3  # let GitHub process the PR

COMMIT_OID=$(gh api "repos/$FIXTURE_REPO/pulls/$PR_NUMBER" --jq '.head.sha')
echo "Head SHA: $COMMIT_OID"

gh api "repos/$FIXTURE_REPO/pulls/$PR_NUMBER/reviews" \
  --method POST \
  --input - <<EOF
{
  "commit_id": "$COMMIT_OID",
  "event": "COMMENT",
  "body": "Several issues to fix before this can merge.",
  "comments": [
    {
      "path": "src/utils.py",
      "line": 15,
      "body": "Typo: \`frist_name\` should be \`first_name\`. Fix the parameter name and the f-string."
    },
    {
      "path": "src/utils.py",
      "line": 4,
      "body": "Unused import: \`sys\` is imported but never used. Please remove it."
    },
    {
      "path": "src/validator.js",
      "line": 11,
      "body": "\`isPositiveNumber(0)\` now incorrectly returns true. Zero is not positive — use strict \`>\` instead of \`>=\`."
    },
    {
      "path": "src/config.py",
      "line": 2,
      "body": "DEFAULT_TIMEOUT should be 60 seconds to match the production default, not 30."
    }
  ]
}
EOF
echo "Review posted."
echo ""

# ── Step 4: Advance main with a conflicting change ──────────────────────────

echo "=== Step 4: Advancing main (creating merge conflict) ==="
git checkout main
git reset --hard origin/main

# main adds src/config.py with different values — conflicts with the PR branch
cat > src/config.py << 'PYEOF'
# Application configuration (production defaults)
DEFAULT_TIMEOUT = 60
MAX_RETRIES = 3
DEBUG = False
ENVIRONMENT = "production"
PYEOF

git add src/config.py
git commit -m "chore: add production config defaults (E2E conflict commit)"
git push origin main
echo "Main advanced — $(git rev-parse HEAD)"
git checkout "$E2E_BRANCH"
echo ""

# ── Step 5: Record pre-fix state ────────────────────────────────────────────

echo "=== Step 5: Recording pre-fix state ==="
PRE_FIX_SHA="$COMMIT_OID"
echo "Pre-fix SHA: $PRE_FIX_SHA"
echo ""

# ── Step 6: Run gh crfix ─────────────────────────────────────────────────────

echo "=== Step 6: Running gh crfix ==="
cd "$FIXTURE_DIR"
# Invoke via shebang so both the bash script and the Go binary work.
"$GHCRFIX" "https://github.com/$FIXTURE_REPO/pull/$PR_NUMBER" \
  --seq --no-tui --no-post-fix
echo ""

# ── Step 7: Verify ──────────────────────────────────────────────────────────

echo "=== Step 7: Verifying ==="
cd "$FIXTURE_DIR"
git fetch origin "$E2E_BRANCH"
git checkout "$E2E_BRANCH"
# gh crfix made its edits inside a separate git worktree and pushed them;
# the fixture dir's checkout is still at the PRE-fix SHA with local tree
# state that diverges from the new remote tip. Hard-reset to the remote
# tip so assertions grep the files gh crfix actually produced.
git reset --hard "origin/$E2E_BRANCH"

PASS=0; FAIL=0
check() {
  local num="$1" desc="$2" result="$3"
  if [ "$result" = "pass" ]; then
    echo "  PASS [$num] $desc"
    PASS=$((PASS + 1))
  else
    echo "  FAIL [$num] $desc"
    FAIL=$((FAIL + 1))
  fi
}

# [1] New commit was pushed
POST_FIX_SHA=$(gh api "repos/$FIXTURE_REPO/pulls/$PR_NUMBER" --jq '.head.sha')
[ "$PRE_FIX_SHA" != "$POST_FIX_SHA" ] \
  && check 1 "new commit pushed ($POST_FIX_SHA)" pass \
  || check 1 "new commit pushed — SHA unchanged, no commit was made" fail

# [2] Typo fixed in utils.py
(grep -q 'first_name' src/utils.py && ! grep -q 'frist_name' src/utils.py) \
  && check 2 "typo fixed (frist_name → first_name)" pass \
  || check 2 "typo still present or first_name missing" fail

# [3] Unused import removed
! grep -qE '^import sys' src/utils.py \
  && check 3 "unused import sys removed" pass \
  || check 3 "unused 'import sys' still present" fail

# [4] Comparison operator fixed
(grep -q 'value > 0' src/validator.js && ! grep -q 'value >= 0' src/validator.js) \
  && check 4 "comparison fixed (>= → >)" pass \
  || check 4 "comparison operator not fixed" fail

# [5] No conflict markers in any file
CONFLICT_FILES="$(grep -rls '<<<<<<' src/ 2>/dev/null || true)"
[ -z "$CONFLICT_FILES" ] \
  && check 5 "no conflict markers in src/" pass \
  || check 5 "conflict markers still present in: $CONFLICT_FILES" fail

# [6] Review threads resolved
UNRESOLVED=$(gh api graphql -f query='
  query($owner: String!, $repo: String!, $pr: Int!) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $pr) {
        reviewThreads(first: 20) {
          nodes { isResolved }
        }
      }
    }
  }
' -F owner="${FIXTURE_REPO%%/*}" -F repo="${FIXTURE_REPO##*/}" -F pr="$PR_NUMBER" \
  --jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)] | length')
[ "$UNRESOLVED" -eq 0 ] \
  && check 6 "all review threads resolved" pass \
  || check 6 "$UNRESOLVED review thread(s) still unresolved" fail

# [7] Log files were created and contain useful content
LAST_RUN_LOG="$HOME/.gh-crfix/last-run/run.log"
# Accept either [process-pr] (bash) or [setup-pr] (Go port) per-PR entries.
([ -f "$LAST_RUN_LOG" ] && grep -qE '\[(process|setup)-pr\]' "$LAST_RUN_LOG") \
  && check 7 "master log written to $LAST_RUN_LOG" pass \
  || check 7 "master log missing or empty at $LAST_RUN_LOG" fail

# ── Result ───────────────────────────────────────────────────────────────────

TOTAL=$((PASS + FAIL))
echo ""
echo "=== E2E Result: $PASS/$TOTAL passed ==="
[ "$FAIL" -gt 0 ] && { echo "FAILED"; exit 1; }
echo "ALL PASSED"
