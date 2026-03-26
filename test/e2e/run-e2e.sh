#!/usr/bin/env bash
# E2E test for gh-crfix
#
# Creates a PR with intentional bugs + review comments in a fixture repo,
# runs gh crfix to fix them, verifies the fixes were applied.
#
# Requires: gh (authenticated), claude CLI, ANTHROPIC_API_KEY
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GHCRFIX="$SCRIPT_DIR/../../gh-crfix"
FIXTURE_REPO="${FIXTURE_REPO:-maszynka/gh-crfix-e2e-fixtures}"
FIXTURE_DIR="${FIXTURE_DIR:-$SCRIPT_DIR/../../fixture-repo}"
E2E_BRANCH="e2e-test-$(date +%s)-$$"
PR_NUMBER=""

echo "=== gh-crfix E2E Test ==="
echo "Script:  $GHCRFIX"
echo "Fixture: $FIXTURE_REPO"
echo "Branch:  $E2E_BRANCH"
echo ""

# ── Cleanup (always runs) ────────────────────────────────────────────────────

cleanup() {
  local exit_code=$?
  echo ""
  echo "=== Cleanup ==="
  if [ -n "${PR_NUMBER:-}" ]; then
    echo "Closing PR #$PR_NUMBER..."
    gh pr close "$PR_NUMBER" --repo "$FIXTURE_REPO" --delete-branch 2>/dev/null || true
  fi
  if [ -n "${E2E_BRANCH:-}" ]; then
    git -C "$FIXTURE_DIR" push origin --delete "$E2E_BRANCH" 2>/dev/null || true
  fi
  echo "Cleanup done."
  exit $exit_code
}
trap cleanup EXIT

# ── Preflight checks ─────────────────────────────────────────────────────────

echo "=== Preflight ==="
command -v gh >/dev/null || { echo "FAIL: gh CLI not found"; exit 1; }
command -v claude >/dev/null || { echo "FAIL: claude CLI not found"; exit 1; }
command -v jq >/dev/null || { echo "FAIL: jq not found"; exit 1; }
[ -x "$GHCRFIX" ] || { echo "FAIL: $GHCRFIX not found or not executable"; exit 1; }
[ -d "$FIXTURE_DIR/.git" ] || { echo "FAIL: $FIXTURE_DIR is not a git repo"; exit 1; }
echo "All checks passed."
echo ""

# ── Step 1: Create branch with bugs ──────────────────────────────────────────

echo "=== Step 1: Creating buggy branch ==="
cd "$FIXTURE_DIR"
git checkout main
git pull --rebase origin main
git checkout -b "$E2E_BRANCH"

# Bug 1: typo in variable name (Python)
sed -i 's/first_name/frist_name/g' src/utils.py

# Bug 2: unused import (Python)
sed -i '3a import sys' src/utils.py

# Bug 3: wrong comparison operator (JS)
sed -i 's/value > 0/value >= 0/' src/validator.js

git add -A
git commit -m "chore: introduce test bugs for E2E"
git push -u origin "$E2E_BRANCH"
echo "Branch pushed."
echo ""

# ── Step 2: Open PR ──────────────────────────────────────────────────────────

echo "=== Step 2: Opening PR ==="
PR_URL=$(gh pr create \
  --repo "$FIXTURE_REPO" \
  --head "$E2E_BRANCH" \
  --base main \
  --title "E2E test $(date +%Y-%m-%d-%H%M%S)" \
  --body "Automated E2E test for gh-crfix. Will be cleaned up automatically.")

PR_NUMBER=$(echo "$PR_URL" | grep -oE '[0-9]+$')
echo "PR created: #$PR_NUMBER ($PR_URL)"
echo ""

# ── Step 3: Add review comments ──────────────────────────────────────────────

echo "=== Step 3: Adding review comments ==="

# Wait a moment for GitHub to process the PR
sleep 3

COMMIT_OID=$(gh api "repos/$FIXTURE_REPO/pulls/$PR_NUMBER" --jq '.head.sha')
echo "Head commit: $COMMIT_OID"

gh api "repos/$FIXTURE_REPO/pulls/$PR_NUMBER/reviews" \
  --method POST \
  --input - <<EOF
{
  "commit_id": "$COMMIT_OID",
  "event": "REQUEST_CHANGES",
  "body": "Please fix these issues.",
  "comments": [
    {
      "path": "src/utils.py",
      "line": 15,
      "body": "Typo: \`frist_name\` should be \`first_name\`"
    },
    {
      "path": "src/utils.py",
      "line": 4,
      "body": "Unused import: \`sys\` is imported but never used. Remove it."
    },
    {
      "path": "src/validator.js",
      "line": 11,
      "body": "\`isPositiveNumber(0)\` returns \`true\` but zero is not positive. Use strict \`>\` not \`>=\`."
    }
  ]
}
EOF

echo "Review comments posted."
echo ""

# ── Step 4: Record pre-fix state ─────────────────────────────────────────────

echo "=== Step 4: Recording pre-fix state ==="
PRE_FIX_SHA="$COMMIT_OID"
echo "Pre-fix SHA: $PRE_FIX_SHA"
echo ""

# ── Step 5: Run gh crfix ─────────────────────────────────────────────────────

echo "=== Step 5: Running gh crfix ==="
cd "$FIXTURE_DIR"
bash "$GHCRFIX" "https://github.com/$FIXTURE_REPO/pull/$PR_NUMBER" \
  --seq --no-tui --no-post-fix
echo ""

# ── Step 6: Verify fixes ─────────────────────────────────────────────────────

echo "=== Step 6: Verifying fixes ==="
cd "$FIXTURE_DIR"
git fetch origin "$E2E_BRANCH"
git checkout "$E2E_BRANCH"
git pull --rebase origin "$E2E_BRANCH"

FAILURES=0
TOTAL=5

# Check 1: New commit was pushed
POST_FIX_SHA=$(gh api "repos/$FIXTURE_REPO/pulls/$PR_NUMBER" --jq '.head.sha')
if [ "$PRE_FIX_SHA" != "$POST_FIX_SHA" ]; then
  echo "PASS [1/5]: New commit pushed ($PRE_FIX_SHA -> $POST_FIX_SHA)"
else
  echo "FAIL [1/5]: No new commit was pushed"
  FAILURES=$((FAILURES + 1))
fi

# Check 2: Typo fixed
if grep -q 'first_name' src/utils.py && ! grep -q 'frist_name' src/utils.py; then
  echo "PASS [2/5]: Typo fixed (frist_name -> first_name)"
else
  echo "FAIL [2/5]: Typo not fixed"
  FAILURES=$((FAILURES + 1))
fi

# Check 3: Unused import removed
if ! grep -q '^import sys' src/utils.py; then
  echo "PASS [3/5]: Unused import removed"
else
  echo "FAIL [3/5]: Unused import still present"
  FAILURES=$((FAILURES + 1))
fi

# Check 4: Comparison operator fixed
if grep -q 'value > 0' src/validator.js && ! grep -q 'value >= 0' src/validator.js; then
  echo "PASS [4/5]: Comparison operator fixed (>= -> >)"
else
  echo "FAIL [4/5]: Comparison operator not fixed"
  FAILURES=$((FAILURES + 1))
fi

# Check 5: Review threads resolved
UNRESOLVED=$(gh api graphql -f query='
  query($owner: String!, $repo: String!, $pr: Int!) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $pr) {
        reviewThreads(first: 10) {
          nodes { isResolved }
        }
      }
    }
  }
' -F owner="${FIXTURE_REPO%%/*}" -F repo="${FIXTURE_REPO##*/}" -F pr="$PR_NUMBER" \
  --jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)] | length')

if [ "$UNRESOLVED" -eq 0 ]; then
  echo "PASS [5/5]: All review threads resolved"
else
  RESOLVED=$((3 - UNRESOLVED))
  if [ "$RESOLVED" -gt 0 ]; then
    echo "PASS [5/5]: $RESOLVED/3 threads resolved ($UNRESOLVED remaining)"
  else
    echo "FAIL [5/5]: No review threads resolved ($UNRESOLVED unresolved)"
    FAILURES=$((FAILURES + 1))
  fi
fi

# ── Result ────────────────────────────────────────────────────────────────────

echo ""
echo "=== E2E Results: $((TOTAL - FAILURES))/$TOTAL checks passed ==="
if [ "$FAILURES" -gt 0 ]; then
  echo "FAILED"
  exit 1
fi
echo "ALL PASSED"
