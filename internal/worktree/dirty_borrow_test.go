package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sameDir compares two paths after resolving symlinks. macOS symlinks
// /var → /private/var so a literal string compare between user-provided
// paths and `git`-reported paths frequently disagrees.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ar, err := filepath.EvalSymlinks(a)
	if err != nil {
		ar = a
	}
	br, err := filepath.EvalSymlinks(b)
	if err != nil {
		br = b
	}
	return ar == br
}

// TestSetup_BorrowedDirty_AutoStashesAndContinues is the property the user
// reported missing: when the target branch is checked out in another
// worktree (e.g. .claude/worktrees/<slug>) and that worktree has
// uncommitted changes, gh-crfix should NOT bail with "worktree not clean".
// Instead it should stash the user's changes (including untracked) so
// gh-crfix can do its work, and Cleanup will pop the stash back.
func TestSetup_BorrowedDirty_AutoStashesAndContinues(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "shared-feat")

	// Plant another worktree that has the branch checked out — this is the
	// .claude/worktrees/<slug> case from the user's repo.
	other := filepath.Join(clone, ".claude", "worktrees", "shared")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, clone, "fetch", "--quiet", "origin", "shared-feat")
	runGit(t, clone, "worktree", "add", "--quiet", other, "shared-feat")

	// Dirty it: drop an untracked file (the most common case in
	// development).
	dirtyFile := filepath.Join(other, "wip.txt")
	if err := os.WriteFile(dirtyFile, []byte("user-in-progress\n"), 0o644); err != nil {
		t.Fatalf("write dirty: %v", err)
	}
	if dirty, _ := DirtyStatus(other); dirty == "" {
		t.Fatalf("test bug: expected dirty status before Setup")
	}

	// Default mode (temp) — auto-stash on borrow is the new contract.
	SetMode(ModeTemp)
	t.Cleanup(func() { SetMode(ModeTemp) })

	wt, err := Setup(context.Background(), clone, "shared-feat", 1101)
	if err != nil {
		t.Fatalf("Setup must NOT fail on borrowed-dirty workspace, got: %v", err)
	}
	// macOS canonicalizes /var → /private/var; compare via evalSymlinks.
	if !sameDir(t, wt, other) {
		t.Fatalf("Setup should return borrowed path %q, got %q", other, wt)
	}

	// After Setup the borrowed worktree must be clean (the dirty file is
	// stashed away while gh-crfix does its work).
	if dirty, _ := DirtyStatus(other); dirty != "" {
		t.Errorf("borrowed worktree should be clean after auto-stash; got dirty:\n%s", dirty)
	}
	if _, err := os.Stat(dirtyFile); !os.IsNotExist(err) {
		t.Errorf("untracked file should have been stashed; stat err=%v", err)
	}

	// State must record the stash so Cleanup can find it.
	statesMu.Lock()
	s := setupStates[stateKey(clone, 1101)]
	statesMu.Unlock()
	if s == nil {
		t.Fatalf("state not recorded")
	}
	if !s.Borrowed {
		t.Errorf("state.Borrowed = false, want true")
	}
	if s.StashRef == "" {
		t.Errorf("state.StashRef empty; want a stash reference for later pop")
	}
}

// TestCleanup_BorrowedAutoStash_PopsAndRestoresFiles verifies the round-trip:
// the user's pre-Setup work must come back after Cleanup.
func TestCleanup_BorrowedAutoStash_PopsAndRestoresFiles(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "shared-feat-2")

	other := filepath.Join(clone, ".claude", "worktrees", "shared2")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, clone, "fetch", "--quiet", "origin", "shared-feat-2")
	runGit(t, clone, "worktree", "add", "--quiet", other, "shared-feat-2")

	dirtyFile := filepath.Join(other, "wip.txt")
	if err := os.WriteFile(dirtyFile, []byte("user-wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	SetMode(ModeTemp)
	t.Cleanup(func() { SetMode(ModeTemp) })

	if _, err := Setup(context.Background(), clone, "shared-feat-2", 1102); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Sanity: file gone (stashed).
	if _, err := os.Stat(dirtyFile); !os.IsNotExist(err) {
		t.Fatalf("test bug: setup should have stashed away wip.txt")
	}

	if err := Cleanup(context.Background(), clone, 1102); err != nil {
		t.Fatalf("Cleanup must pop the stash cleanly when there's no conflict, got: %v", err)
	}

	// File must be restored to its original content.
	got, err := os.ReadFile(dirtyFile)
	if err != nil {
		t.Fatalf("user's wip.txt was not restored after Cleanup: %v", err)
	}
	if string(got) != "user-wip\n" {
		t.Errorf("wip.txt content mismatch; got %q want %q", got, "user-wip\n")
	}
}

// TestSetup_BorrowedClean_NoStashCreated keeps the auto-stash narrow: when
// the borrowed worktree is already clean, Setup must not create a noisy
// gh-crfix stash entry.
func TestSetup_BorrowedClean_NoStashCreated(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "shared-feat-clean")

	other := filepath.Join(clone, ".claude", "worktrees", "shared-clean")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, clone, "fetch", "--quiet", "origin", "shared-feat-clean")
	runGit(t, clone, "worktree", "add", "--quiet", other, "shared-feat-clean")

	SetMode(ModeTemp)
	t.Cleanup(func() { SetMode(ModeTemp) })

	if _, err := Setup(context.Background(), clone, "shared-feat-clean", 1103); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	statesMu.Lock()
	s := setupStates[stateKey(clone, 1103)]
	statesMu.Unlock()
	if s == nil || !s.Borrowed {
		t.Fatalf("state missing or not borrowed: %+v", s)
	}
	if s.StashRef != "" {
		t.Errorf("clean borrowed worktree must not produce a stash; got StashRef=%q", s.StashRef)
	}
	out, _ := exec.Command("git", "-C", other, "stash", "list").Output()
	if strings.Contains(string(out), "gh-crfix") {
		t.Errorf("unexpected gh-crfix stash entry on clean borrow: %s", out)
	}
}

// TestParseMode_RecognizesIsolated documents the new mode keyword. Without
// this, --isolated / WORKTREE_MODE=isolated falls back to ModeTemp and the
// borrow logic would still kick in.
func TestParseMode_RecognizesIsolated(t *testing.T) {
	if got := ParseMode("isolated"); got != ModeIsolated {
		t.Errorf("ParseMode(\"isolated\") = %q, want %q", got, ModeIsolated)
	}
	if got := ParseMode("ISOLATED"); got != ModeIsolated {
		t.Errorf("ParseMode(\"ISOLATED\") = %q, want %q", got, ModeIsolated)
	}
}

// TestSetup_IsolatedMode_BypassesBorrowCreatesDetached is the strategy-(B)
// counterpart to auto-stash: the user opts out of borrowing entirely. The
// branch may be checked out anywhere; gh-crfix should still create its
// own worktree at .gh-crfix/worktrees/pr-<N> with a detached HEAD on
// origin/<branch>, untouched by what's happening elsewhere.
func TestSetup_IsolatedMode_BypassesBorrowCreatesDetached(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "iso-feat")

	// Put the branch in a borrow target with dirty content — this would
	// normally be borrowed (with auto-stash). Isolated mode must ignore it.
	other := filepath.Join(clone, ".claude", "worktrees", "iso-borrow")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, clone, "fetch", "--quiet", "origin", "iso-feat")
	runGit(t, clone, "worktree", "add", "--quiet", other, "iso-feat")
	if err := os.WriteFile(filepath.Join(other, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	SetMode(ModeIsolated)
	t.Cleanup(func() { SetMode(ModeTemp) })

	wt, err := Setup(context.Background(), clone, "iso-feat", 1104)
	if err != nil {
		t.Fatalf("Setup (isolated): %v", err)
	}
	wantPath := PathFor(clone, 1104)
	if !sameDir(t, wt, wantPath) {
		t.Errorf("isolated worktree path = %q, want %q (must NOT borrow %s)", wt, wantPath, other)
	}
	// Borrowed worktree's wip.txt must be untouched (no stash/pop happened
	// because we didn't borrow it).
	if _, err := os.Stat(filepath.Join(other, "wip.txt")); err != nil {
		t.Errorf("isolated mode must not touch borrow target; wip.txt missing: %v", err)
	}
	// New worktree must be on a detached HEAD (no branch ref) so the user's
	// other checkout retains exclusive ownership of the branch ref. With
	// detached HEAD, git symbolic-ref exits 1 — that is the expected state.
	cmd := exec.Command("git", "-C", wt, "symbolic-ref", "-q", "HEAD")
	if out, err := cmd.Output(); err == nil {
		t.Errorf("isolated worktree should have detached HEAD; got symbolic-ref=%q", strings.TrimSpace(string(out)))
	}
	statesMu.Lock()
	s := setupStates[stateKey(clone, 1104)]
	statesMu.Unlock()
	if s == nil || s.Borrowed {
		t.Errorf("isolated state must be non-borrowed; got %+v", s)
	}
}
