package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":         ModeTemp,
		"temp":     ModeTemp,
		"TEMP":     ModeTemp,
		"  reuse ": ModeReuse,
		"stash":    ModeStash,
		"garbage":  ModeTemp,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSetup_TempMode_RemovesOnCleanup asserts that ModeTemp removes the
// worktree when Cleanup runs. This is the new default and the property
// users care about most (no disk accumulation).
func TestSetup_TempMode_RemovesOnCleanup(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "tmp-feature")

	SetMode(ModeTemp)
	t.Cleanup(func() { SetMode(ModeTemp) })

	wt, err := Setup(context.Background(), clone, "tmp-feature", 700)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree should exist after Setup: %v", err)
	}

	if err := Cleanup(context.Background(), clone, 700); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree removed by Cleanup; stat err=%v", err)
	}
}

// TestSetup_ReuseMode_FailsOnDirty asserts that ModeReuse refuses to touch
// a dirty worktree.
func TestSetup_ReuseMode_FailsOnDirty(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "reuse-feature")

	SetMode(ModeTemp)
	wt, err := Setup(context.Background(), clone, "reuse-feature", 800)
	if err != nil {
		t.Fatalf("Setup #1: %v", err)
	}
	// Register a fresh state so Cleanup of the stale temp-state from #1
	// doesn't fire when we switch modes.
	_ = Cleanup(context.Background(), clone, 800)
	// Recreate clean worktree under reuse mode.
	SetMode(ModeReuse)
	t.Cleanup(func() { SetMode(ModeTemp) })
	if _, err := Setup(context.Background(), clone, "reuse-feature", 800); err != nil {
		t.Fatalf("Setup reuse #1: %v", err)
	}

	// Drop a dirty file and try again.
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write dirty: %v", err)
	}
	_, err = Setup(context.Background(), clone, "reuse-feature", 800)
	if err == nil {
		t.Fatalf("expected error from reuse-mode Setup on dirty worktree, got nil")
	}
}

// TestSetup_StashMode_PopsAfterCleanup asserts that ModeStash hides dirty
// content during processing and restores it on Cleanup.
func TestSetup_StashMode_PopsAfterCleanup(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "stash-feature")

	SetMode(ModeTemp)
	wt, err := Setup(context.Background(), clone, "stash-feature", 900)
	if err != nil {
		t.Fatalf("Setup #1: %v", err)
	}
	_ = Cleanup(context.Background(), clone, 900)

	// Re-create under temp mode so the dir exists, then pivot to stash mode.
	SetMode(ModeStash)
	t.Cleanup(func() { SetMode(ModeTemp) })
	if _, err := Setup(context.Background(), clone, "stash-feature", 900); err != nil {
		t.Fatalf("Setup #2 (stash, clean): %v", err)
	}

	// Dirty the worktree; next Setup should stash the change.
	dirty := filepath.Join(wt, "wip.txt")
	if err := os.WriteFile(dirty, []byte("wip"), 0o644); err != nil {
		t.Fatalf("write wip: %v", err)
	}
	if _, err := Setup(context.Background(), clone, "stash-feature", 900); err != nil {
		t.Fatalf("Setup #3 (stash, dirty): %v", err)
	}
	// After stash + reset the wip file must be gone.
	if _, err := os.Stat(dirty); !os.IsNotExist(err) {
		t.Fatalf("expected wip.txt stashed away; stat err=%v", err)
	}

	// Cleanup should pop the stash and restore the file.
	if err := Cleanup(context.Background(), clone, 900); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("expected wip.txt restored after stash pop; stat err=%v", err)
	}
}

// TestCleanup_NoState_IsNoop guards the contract that Cleanup is safe to
// call for any (repoRoot, prNum) pair, including ones Setup never ran for.
// Batch-level cleanup loops call this on skipped/failed PRs without worrying
// whether their worktree was actually created.
func TestCleanup_NoState_IsNoop(t *testing.T) {
	if err := Cleanup(context.Background(), "/nonexistent-repo", 9999); err != nil {
		t.Fatalf("Cleanup with no state should be no-op, got: %v", err)
	}
}

// TestSetup_StashMode_CleanWorktree_NoStash asserts that stash mode does
// NOT create a stash entry when the worktree is already clean — otherwise
// every Setup call would leave a noisy `gh-crfix/pr-N` stash even when
// there's nothing to preserve.
func TestSetup_StashMode_CleanWorktree_NoStash(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "stash-clean-feature")

	SetMode(ModeTemp)
	if _, err := Setup(context.Background(), clone, "stash-clean-feature", 950); err != nil {
		t.Fatalf("Setup temp: %v", err)
	}
	_ = Cleanup(context.Background(), clone, 950)

	SetMode(ModeStash)
	t.Cleanup(func() { SetMode(ModeTemp) })
	if _, err := Setup(context.Background(), clone, "stash-clean-feature", 950); err != nil {
		t.Fatalf("Setup stash (clean): %v", err)
	}

	// State must NOT carry a StashRef when nothing was stashed — otherwise
	// Cleanup's pop path would chase a phantom stash entry.
	statesMu.Lock()
	s := setupStates[stateKey(clone, 950)]
	statesMu.Unlock()
	if s == nil {
		t.Fatalf("expected setup state recorded; got nil")
	}
	if s.StashRef != "" {
		t.Fatalf("clean worktree must not produce a stash; got StashRef=%q", s.StashRef)
	}

	// `git stash list` should also be empty (or at least not contain our marker).
	cmd := exec.Command("git", "-C", PathFor(clone, 950), "stash", "list")
	out, _ := cmd.Output()
	if strings.Contains(string(out), "gh-crfix/pr-950") {
		t.Fatalf("unexpected stash entry left behind: %s", out)
	}

	_ = Cleanup(context.Background(), clone, 950)
}

// TestSetup_ReuseMode_CleanSucceedsNoReset asserts that reuse mode happily
// proceeds on a clean worktree and does NOT issue a `reset --hard` (which
// would discard local commits the user wants to keep across runs).
func TestSetup_ReuseMode_CleanSucceedsNoReset(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "reuse-clean-feature")

	SetMode(ModeTemp)
	if _, err := Setup(context.Background(), clone, "reuse-clean-feature", 970); err != nil {
		t.Fatalf("Setup temp: %v", err)
	}
	wt := PathFor(clone, 970)
	_ = Cleanup(context.Background(), clone, 970)

	// Re-create under temp first so worktree exists, then add a local commit
	// that ONLY exists in this worktree (not on origin) — reuse mode must
	// preserve it.
	SetMode(ModeTemp)
	if _, err := Setup(context.Background(), clone, "reuse-clean-feature", 970); err != nil {
		t.Fatalf("Setup recreate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "local.txt"), []byte("local"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, wt, "add", "local.txt")
	runGit(t, wt, "commit", "-m", "local-only commit", "--quiet")

	// Switch to reuse mode.
	_ = Cleanup(context.Background(), clone, 970)
	SetMode(ModeReuse)
	t.Cleanup(func() { SetMode(ModeTemp) })

	// reuse-mode Setup on a clean worktree with a local-only commit must
	// succeed and preserve the file.
	if _, err := Setup(context.Background(), clone, "reuse-clean-feature", 970); err != nil {
		t.Fatalf("Setup reuse (clean+local commit): %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "local.txt")); err != nil {
		t.Fatalf("reuse mode discarded local commit's file; stat err=%v", err)
	}
	_ = Cleanup(context.Background(), clone, 970)
}

// TestCleanup_BorrowedWorktree_NotRemoved is a critical safety property: a
// borrowed worktree (e.g. .claude/worktrees/<branch>) must NEVER be removed
// by Cleanup — that would silently nuke the user's other tooling. We
// simulate the borrowed scenario by manually marking the state.
func TestCleanup_BorrowedWorktree_NotRemoved(t *testing.T) {
	skipIfWindows(t)
	tmp := t.TempDir()
	borrowed := filepath.Join(tmp, "borrowed-worktree")
	if err := os.MkdirAll(borrowed, 0o755); err != nil {
		t.Fatalf("mkdir borrowed: %v", err)
	}

	// Inject a borrowed state directly. Using temp mode is the most
	// dangerous case (would remove if not for the Borrowed flag).
	recordState(tmp, 980, &setupState{
		Mode: ModeTemp, Path: borrowed, Borrowed: true,
	})

	if err := Cleanup(context.Background(), tmp, 980); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	// Borrowed dir MUST still exist.
	if _, err := os.Stat(borrowed); err != nil {
		t.Fatalf("borrowed worktree must not be removed; stat err=%v", err)
	}
}

// TestFindBranchWorktrees ensures shared-branch detection picks up a
// worktree at a non-default path so the borrow path can run.
func TestFindBranchWorktrees_DetectsBorrowed(t *testing.T) {
	wts := []WorktreeInfo{
		{Path: "/repo", Branch: "main"},
		{Path: "/repo/.claude/worktrees/feat-x", Branch: "feat-x"},
	}
	ours := "/repo/.gh-crfix/worktrees/pr-42"
	atOurs, other := findBranchWorktrees(wts, ours, "feat-x")
	if atOurs != "" {
		t.Errorf("wtAtOurPath: got %q, want empty", atOurs)
	}
	if other != "/repo/.claude/worktrees/feat-x" {
		t.Errorf("otherForBranch: got %q, want /repo/.claude/worktrees/feat-x", other)
	}
}
