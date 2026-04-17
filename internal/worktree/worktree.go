// Package worktree manages git worktrees for PR processing.
package worktree

import (
	"context"
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
func Setup(ctx context.Context, repoRoot, branch string, prNum int) (string, error) {
	wt := PathFor(repoRoot, prNum)

	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktrees dir: %w", err)
	}

	// Check if worktree already exists.
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		// Worktree exists — reset to clean state.
		if err := cleanWorktree(ctx, wt, branch); err != nil {
			return "", err
		}
		return wt, nil
	}

	// Fetch the branch from origin.
	if err := gitIn(ctx, repoRoot, "fetch", "--quiet", "origin", branch); err != nil {
		return "", fmt.Errorf("fetch %s: %w", branch, err)
	}

	// Check if local ref exists.
	localExists := gitIn(ctx, repoRoot, "rev-parse", "--verify", branch) == nil

	var addErr error
	if localExists {
		addErr = gitIn(ctx, repoRoot, "worktree", "add", "--force", wt, branch)
	} else {
		addErr = gitIn(ctx, repoRoot, "worktree", "add", "--force", wt,
			"--track", "-b", branch, "origin/"+branch)
	}
	if addErr != nil {
		return "", fmt.Errorf("worktree add: %w", addErr)
	}

	if err := cleanWorktree(ctx, wt, branch); err != nil {
		return "", err
	}
	return wt, nil
}

// Remove removes a worktree.
func Remove(ctx context.Context, repoRoot string, prNum int) error {
	wt := PathFor(repoRoot, prNum)
	if err := gitIn(ctx, repoRoot, "worktree", "remove", "--force", wt); err != nil {
		// Fall back to manual removal.
		_ = os.RemoveAll(wt)
	}
	return nil
}

// MergeBase merges baseBranch into the worktree branch.
func MergeBase(ctx context.Context, wtPath, baseBranch string) error {
	if err := gitIn(ctx, wtPath, "fetch", "--quiet", "origin", baseBranch); err != nil {
		return fmt.Errorf("fetch base %s: %w", baseBranch, err)
	}
	if err := gitIn(ctx, wtPath, "merge", "--no-edit", "origin/"+baseBranch); err != nil {
		return fmt.Errorf("merge base: %w", err)
	}
	return nil
}

// cleanWorktree resets the worktree to a clean state on branch.
func cleanWorktree(ctx context.Context, wt, branch string) error {
	// Abort any in-progress merge/rebase.
	_ = gitIn(ctx, wt, "merge", "--abort")
	_ = gitIn(ctx, wt, "rebase", "--abort")

	// Checkout and hard reset.
	if err := gitIn(ctx, wt, "checkout", branch); err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}
	if err := gitIn(ctx, wt, "reset", "--hard", "origin/"+branch); err != nil {
		// If origin ref doesn't exist, just hard reset to HEAD.
		_ = gitIn(ctx, wt, "reset", "--hard", "HEAD")
	}
	// Clean untracked files (but keep .gh-crfix/).
	_ = gitIn(ctx, wt, "clean", "-fdx", "--exclude=.gh-crfix/")
	return nil
}

// RepoRoot returns the git root for path (can be any dir inside a git repo).
func RepoRoot(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repo at %s", path)
	}
	return strings.TrimSpace(string(out)), nil
}

// MatchesRepo returns true when the origin remote URL of the git repo at
// `root` refers to owner/repo. On mismatch, the second return value is a
// human-readable message that includes both the actual origin URL and the
// target owner/repo so callers can surface it to users.
//
// The check is forgiving: local filesystem origins (commonly used in tests)
// and non-GitHub hosts are treated as matching so we don't surface false
// positives. Only URLs that look like a real GitHub URL but reference a
// different owner/repo are flagged as a mismatch.
//
// Errors are returned only for "not a git repo" / "origin remote missing"
// conditions; a successful git call always produces (bool, msg, nil).
func MatchesRepo(root, owner, repo string) (bool, string, error) {
	cmd := exec.Command("git", "-C", root, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return false, "", fmt.Errorf("git remote: %w", err)
	}
	url := strings.TrimSpace(string(out))
	if !looksLikeGitHubRemote(url) {
		// Can't tell — let the rest of the pipeline proceed. Local test
		// fixtures and non-GitHub mirrors land here.
		return true, "", nil
	}
	if remoteMatchesRepo(url, owner, repo) {
		return true, "", nil
	}
	target := owner + "/" + repo
	msg := fmt.Sprintf(
		"this directory's git remote (%s) doesn't match target PR's repo (%s) — cd into the right clone or set GH_CRFIX_DIR",
		url, target)
	return false, msg, nil
}

// looksLikeGitHubRemote reports whether the URL is clearly a GitHub remote
// — i.e. an https://github.com/ or git@github.com: form. Anything else
// (local file paths, GitLab mirrors, SSH aliases, etc.) is treated as
// unknown and intentionally skipped by MatchesRepo.
func looksLikeGitHubRemote(url string) bool {
	return strings.Contains(url, "github.com/") || strings.Contains(url, "github.com:")
}

// remoteMatchesRepo returns true if the given URL references owner/repo.
// Accepts https://, git@, and bare owner/repo.git forms.
func remoteMatchesRepo(url, owner, repo string) bool {
	url = strings.TrimSuffix(url, ".git")
	url = strings.TrimSuffix(url, "/")
	suffix := owner + "/" + repo
	return strings.HasSuffix(url, "/"+suffix) || strings.HasSuffix(url, ":"+suffix) || url == suffix
}

func gitIn(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
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
