// Package worktree manages git worktrees for PR processing.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const subdir = ".gh-crfix/worktrees"

// Mode controls how Setup handles existing worktree state and whether the
// worktree is removed after the PR is processed.
type Mode string

const (
	// ModeTemp removes the worktree at Cleanup time. Existing dirty contents
	// are blown away by the usual reset/clean. This is the default; it keeps
	// .gh-crfix/worktrees/ from accumulating but pays a fetch+add cost on
	// every run.
	ModeTemp Mode = "temp"
	// ModeReuse keeps the worktree across runs and refuses to touch user
	// changes. If the worktree is dirty, Setup returns an error so the user
	// can decide what to do.
	ModeReuse Mode = "reuse"
	// ModeStash stashes any uncommitted changes (including untracked) before
	// resetting, then pops the stash at Cleanup time. If the pop conflicts,
	// the stash is left in place so the user can recover manually.
	ModeStash Mode = "stash"
)

// ParseMode normalizes a raw config string into a Mode. Unknown values fall
// back to ModeTemp so a typo doesn't fail the run.
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeReuse:
		return ModeReuse
	case ModeStash:
		return ModeStash
	default:
		return ModeTemp
	}
}

// Process-global mode + per-PR state. Setup writes state, Cleanup reads it.
// Callers set the mode once via SetMode (typically from main.go after config
// is loaded). Tests that drive worktree.Setup directly can also call SetMode
// to exercise mode-specific paths.
var (
	modeMu      sync.RWMutex
	globalMode  = ModeTemp
	statesMu    sync.Mutex
	setupStates = map[string]*setupState{}
)

type setupState struct {
	Mode     Mode
	Path     string
	Borrowed bool   // worktree was already checked out at Path before Setup
	StashRef string // non-empty means a stash was pushed (mode=stash, was dirty)
}

// SetMode installs the process-global Mode used by Setup/Cleanup.
func SetMode(m Mode) {
	modeMu.Lock()
	globalMode = m
	modeMu.Unlock()
}

// GetMode returns the current process-global Mode.
func GetMode() Mode {
	modeMu.RLock()
	defer modeMu.RUnlock()
	return globalMode
}

func stateKey(repoRoot string, prNum int) string {
	return fmt.Sprintf("%s|%d", repoRoot, prNum)
}

// PathFor returns the expected worktree path for a PR inside repoRoot.
func PathFor(repoRoot string, prNum int) string {
	return filepath.Join(repoRoot, subdir, fmt.Sprintf("pr-%d", prNum))
}

// Setup ensures a clean worktree for branch at repoRoot. The current
// process-global Mode (see SetMode) controls how dirty state and the
// existing worktree are handled. Returns the worktree path.
//
// Setup is mode-aware:
//   - ModeTemp:  reset --hard origin/<branch> + clean -fdx (excluding .gh-crfix/);
//                worktree is removed by Cleanup at the end of processing.
//   - ModeReuse: don't touch worktree contents; fail if dirty so the user
//                can review their own work.
//   - ModeStash: git stash any uncommitted changes (incl. untracked) before
//                reset; Cleanup pops the stash afterwards.
//
// If the target branch is already checked out in another worktree (e.g.
// .claude/worktrees/<branch>), Setup returns that worktree as-is and marks it
// "borrowed" — Cleanup will not modify or remove a borrowed worktree.
func Setup(ctx context.Context, repoRoot, branch string, prNum int) (string, error) {
	mode := GetMode()
	wt := PathFor(repoRoot, prNum)

	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktrees dir: %w", err)
	}

	// 1. Look for the branch in any existing worktree. If our designated path
	// already hosts it, that's a normal "second-call" reuse. If something
	// else (e.g. .claude/worktrees/<branch>) hosts it, borrow that worktree.
	wts, _ := ListWorktrees(repoRoot)
	wtAtOurPath, otherForBranch := findBranchWorktrees(wts, wt, branch)

	if otherForBranch != "" && wtAtOurPath == "" {
		// Branch is checked out elsewhere. Borrow it: Cleanup leaves it alone.
		recordState(repoRoot, prNum, &setupState{
			Mode: mode, Path: otherForBranch, Borrowed: true,
		})
		return otherForBranch, nil
	}

	state := &setupState{Mode: mode, Path: wt}

	if wtAtOurPath != "" {
		// Existing worktree at our path. Refresh remote ref so a temp/stash
		// reset doesn't pin us to a stale tip.
		if mode == ModeTemp || mode == ModeStash {
			_ = gitIn(ctx, repoRoot, "fetch", "--quiet", "origin", branch)
		}
		if err := prepareExistingWorktree(ctx, wt, branch, prNum, mode, state); err != nil {
			return "", err
		}
		recordState(repoRoot, prNum, state)
		return wt, nil
	}

	// 2. No existing worktree: create one. Fetch + add. We deliberately drop
	// the old `worktree add --force` flag so we don't silently steal a branch
	// already checked out in some unexpected location — the borrow path above
	// is the correct place to handle that.
	if err := gitIn(ctx, repoRoot, "fetch", "--quiet", "origin", branch); err != nil {
		return "", fmt.Errorf("fetch %s: %w", branch, err)
	}
	localExists := gitIn(ctx, repoRoot, "rev-parse", "--verify", branch) == nil
	var addErr error
	if localExists {
		addErr = gitIn(ctx, repoRoot, "worktree", "add", wt, branch)
	} else {
		addErr = gitIn(ctx, repoRoot, "worktree", "add", wt,
			"--track", "-b", branch, "origin/"+branch)
	}
	if addErr != nil {
		return "", fmt.Errorf("worktree add: %w", addErr)
	}

	// On a freshly-added worktree there's nothing to stash and nothing to
	// reuse-fail on; only ModeTemp's clean step still runs as a safety net.
	if mode == ModeTemp {
		_ = cleanWorktree(ctx, wt, branch)
	}
	recordState(repoRoot, prNum, state)
	return wt, nil
}

// prepareExistingWorktree applies mode-specific handling to a worktree that
// already exists at wtPath.
func prepareExistingWorktree(ctx context.Context, wtPath, branch string, prNum int, mode Mode, state *setupState) error {
	switch mode {
	case ModeReuse:
		dirty, _ := DirtyStatus(wtPath)
		if dirty != "" {
			return fmt.Errorf(
				"worktree %s is dirty (WORKTREE_MODE=reuse won't touch your changes); "+
					"commit, stash, or rerun with --worktree-mode=stash/temp",
				wtPath)
		}
		// Clean — don't reset, the user owns this checkout.
		return nil

	case ModeStash:
		dirty, _ := DirtyStatus(wtPath)
		if dirty != "" {
			ref := fmt.Sprintf("gh-crfix/pr-%d", prNum)
			if err := gitIn(ctx, wtPath, "stash", "push", "--include-untracked",
				"-m", ref); err != nil {
				return fmt.Errorf("stash push: %w", err)
			}
			state.StashRef = ref
		}
		return cleanWorktree(ctx, wtPath, branch)

	default: // ModeTemp
		return cleanWorktree(ctx, wtPath, branch)
	}
}

func recordState(repoRoot string, prNum int, s *setupState) {
	statesMu.Lock()
	setupStates[stateKey(repoRoot, prNum)] = s
	statesMu.Unlock()
}

// Cleanup finalizes a Setup call. Behaviour depends on the recorded state:
//   - StashRef set: try `git stash pop`; on conflict the stash is preserved
//     and a message is returned so the user can recover.
//   - Mode=temp + !Borrowed: remove the worktree.
//   - Otherwise (reuse, borrowed, ...): leave the worktree as-is.
//
// Cleanup is safe to call on a PR whose Setup never ran (it's a no-op).
func Cleanup(ctx context.Context, repoRoot string, prNum int) error {
	statesMu.Lock()
	s, ok := setupStates[stateKey(repoRoot, prNum)]
	if ok {
		delete(setupStates, stateKey(repoRoot, prNum))
	}
	statesMu.Unlock()
	if !ok || s == nil {
		return nil
	}

	// 1. Restore stash, if any. Do this before removing the worktree so the
	// pop has the workspace to land in.
	if s.StashRef != "" && s.Path != "" {
		if err := popNamedStash(ctx, s.Path, s.StashRef); err != nil {
			// Pop conflict / missing stash: leave it so the user can resolve.
			return fmt.Errorf("stash pop %s: %w (stash preserved at %s)", s.StashRef, err, s.Path)
		}
	}

	// 2. Borrowed worktrees are someone else's; never remove them.
	if s.Borrowed {
		return nil
	}

	if s.Mode == ModeTemp && s.Path != "" {
		return Remove(ctx, repoRoot, prNum)
	}
	return nil
}

// popNamedStash finds the stash whose message matches name and pops it.
// Returns an error when the stash isn't found or the pop conflicts.
func popNamedStash(ctx context.Context, wtPath, name string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", wtPath, "stash", "list")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("stash list: %w", err)
	}
	// stash list lines look like: "stash@{0}: On branch: gh-crfix/pr-181"
	stashIdx := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, name) {
			if i := strings.IndexByte(line, ':'); i > 0 {
				stashIdx = strings.TrimSpace(line[:i])
				break
			}
		}
	}
	if stashIdx == "" {
		// Nothing to pop — stash was lost or never created.
		return nil
	}
	return gitIn(ctx, wtPath, "stash", "pop", stashIdx)
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
