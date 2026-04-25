package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/maszynka/gh-crfix/internal/worktree"
)

// TestCleanupAfterPR_SetupOnlySkipsCleanup is the safety property for
// `gh crfix --setup-only`: the user explicitly asked us to prepare the
// worktree and step away, so the post-PR cleanup loop must NOT remove it.
func TestCleanupAfterPR_SetupOnlySkipsCleanup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Build a real worktree on a fake bare upstream so worktree.Cleanup has
	// something concrete to remove if the SetupOnly guard slips.
	repo := makeRepoWithBranch(t, "tmp-feature")
	worktree.SetMode(worktree.ModeTemp)
	t.Cleanup(func() { worktree.SetMode(worktree.ModeTemp) })

	prNum := 4242
	wtPath, err := worktree.Setup(context.Background(), repo, "tmp-feature", prNum)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	opts := Options{SetupOnly: true}
	p := PreparedPR{PRNum: prNum, RepoRoot: repo, Worktree: wtPath}
	cleanupAfterPR(context.Background(), opts, p)

	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("setup-only must keep worktree; stat err=%v", err)
	}
	// Manual cleanup so the temp dir teardown isn't tripped by lingering state.
	_ = worktree.Cleanup(context.Background(), repo, prNum)
}

// TestCleanupAfterPR_TempModeRemoves confirms the cleanup actually fires for
// non-SetupOnly PRs in the default temp mode — pairing with the property
// above.
func TestCleanupAfterPR_TempModeRemoves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := makeRepoWithBranch(t, "tmp-feature-2")
	worktree.SetMode(worktree.ModeTemp)
	t.Cleanup(func() { worktree.SetMode(worktree.ModeTemp) })

	prNum := 4243
	wtPath, err := worktree.Setup(context.Background(), repo, "tmp-feature-2", prNum)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	opts := Options{SetupOnly: false}
	p := PreparedPR{PRNum: prNum, RepoRoot: repo, Worktree: wtPath}
	cleanupAfterPR(context.Background(), opts, p)

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("temp mode must remove worktree; stat err=%v", err)
	}
}

// TestCleanupAfterPR_NoStateIsSafe handles the skipped/failed PR case where
// cleanupAfterPR is called for a PR whose Setup never recorded state.
func TestCleanupAfterPR_NoStateIsSafe(t *testing.T) {
	// Should not panic or error. Worktree is empty / repoRoot can be a stub.
	cleanupAfterPR(context.Background(), Options{}, PreparedPR{
		PRNum: 9999, RepoRoot: t.TempDir(),
	})
}

// makeRepoWithBranch builds a bare upstream + clone with branch and returns
// the clone path. Mirrors the helper in worktree_test.go but lives here so
// the workflow package can reuse it without importing test code.
func makeRepoWithBranch(t *testing.T, branch string) string {
	t.Helper()

	seed := t.TempDir()
	mustGit(t, seed, "init", "--quiet", "--initial-branch=main")
	mustGit(t, seed, "config", "user.email", "test@example.com")
	mustGit(t, seed, "config", "user.name", "Test")
	mustGit(t, seed, "commit", "--allow-empty", "-m", "init", "--quiet")

	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "--quiet", "--initial-branch=main")
	mustGit(t, seed, "remote", "add", "origin", bare)
	mustGit(t, seed, "push", "--quiet", "origin", "main")

	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	cmd := exec.Command("git", "clone", "--quiet", bare, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	mustGit(t, clone, "config", "user.email", "test@example.com")
	mustGit(t, clone, "config", "user.name", "Test")

	// Push the target branch into the bare upstream so worktree.Setup can
	// fetch it.
	scratch := filepath.Join(t.TempDir(), "scratch")
	if out, err := exec.Command("git", "clone", "--quiet", bare, scratch).CombinedOutput(); err != nil {
		t.Fatalf("clone scratch: %v\n%s", err, out)
	}
	mustGit(t, scratch, "config", "user.email", "test@example.com")
	mustGit(t, scratch, "config", "user.name", "Test")
	mustGit(t, scratch, "checkout", "-b", branch, "--quiet")
	mustGit(t, scratch, "commit", "--allow-empty", "-m", "branch "+branch, "--quiet")
	mustGit(t, scratch, "push", "--quiet", "origin", branch)

	return clone
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
