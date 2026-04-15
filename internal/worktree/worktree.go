// Package worktree manages git worktrees for PR processing.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const subdir = ".gh-crfix/worktrees"

// PathFor returns the expected worktree path for a PR inside repoRoot.
func PathFor(repoRoot string, prNum int) string {
	return filepath.Join(repoRoot, subdir, fmt.Sprintf("pr-%d", prNum))
}

// Setup ensures a clean worktree for branch at repoRoot.
// Returns the worktree path.
func Setup(repoRoot, branch string, prNum int) (string, error) {
	wt := PathFor(repoRoot, prNum)

	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktrees dir: %w", err)
	}

	// Check if worktree already exists.
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		// Worktree exists — reset to clean state.
		if err := cleanWorktree(wt, branch); err != nil {
			return "", err
		}
		return wt, nil
	}

	// Fetch the branch from origin.
	if err := gitIn(repoRoot, "fetch", "--quiet", "origin", branch); err != nil {
		return "", fmt.Errorf("fetch %s: %w", branch, err)
	}

	// Check if local ref exists.
	localExists := gitIn(repoRoot, "rev-parse", "--verify", branch) == nil

	var addErr error
	if localExists {
		addErr = gitIn(repoRoot, "worktree", "add", "--force", wt, branch)
	} else {
		addErr = gitIn(repoRoot, "worktree", "add", "--force", wt,
			"--track", "-b", branch, "origin/"+branch)
	}
	if addErr != nil {
		return "", fmt.Errorf("worktree add: %w", addErr)
	}

	if err := cleanWorktree(wt, branch); err != nil {
		return "", err
	}
	return wt, nil
}

// Remove removes a worktree.
func Remove(repoRoot string, prNum int) error {
	wt := PathFor(repoRoot, prNum)
	if err := gitIn(repoRoot, "worktree", "remove", "--force", wt); err != nil {
		// Fall back to manual removal.
		_ = os.RemoveAll(wt)
	}
	return nil
}

// MergeBase merges baseBranch into the worktree branch.
func MergeBase(wtPath, baseBranch string) error {
	if err := gitIn(wtPath, "fetch", "--quiet", "origin", baseBranch); err != nil {
		return fmt.Errorf("fetch base %s: %w", baseBranch, err)
	}
	if err := gitIn(wtPath, "merge", "--no-edit", "origin/"+baseBranch); err != nil {
		return fmt.Errorf("merge base: %w", err)
	}
	return nil
}

// cleanWorktree resets the worktree to a clean state on branch.
func cleanWorktree(wt, branch string) error {
	// Abort any in-progress merge/rebase.
	_ = gitIn(wt, "merge", "--abort")
	_ = gitIn(wt, "rebase", "--abort")

	// Checkout and hard reset.
	if err := gitIn(wt, "checkout", branch); err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}
	if err := gitIn(wt, "reset", "--hard", "origin/"+branch); err != nil {
		// If origin ref doesn't exist, just hard reset to HEAD.
		_ = gitIn(wt, "reset", "--hard", "HEAD")
	}
	// Clean untracked files (but keep .gh-crfix/).
	_ = gitIn(wt, "clean", "-fdx", "--exclude=.gh-crfix/")
	return nil
}

// RepoRoot returns the git root for path (can be any dir inside a git repo).
func RepoRoot(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repo at %s", path)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitIn(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args[:min(3, len(args))], " "), err, out)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
