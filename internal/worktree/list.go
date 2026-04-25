package worktree

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeInfo describes a single entry in `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path     string
	Branch   string // short name (e.g. "feature-x"); empty if detached/bare
	HEAD     string
	Detached bool
	Bare     bool
}

// ListWorktrees parses `git worktree list --porcelain` from repoRoot.
// Returns nil + error if the git invocation fails.
func ListWorktrees(repoRoot string) ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var (
		all []WorktreeInfo
		cur WorktreeInfo
		in  bool
	)
	flush := func() {
		if in {
			all = append(all, cur)
		}
		cur = WorktreeInfo{}
		in = false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
			in = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "HEAD "):
			cur.HEAD = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		}
	}
	flush()
	return all, nil
}

// findBranchWorktrees returns (wtAtOurPath, otherForBranch):
//   - wtAtOurPath: path of the worktree at ourPath, if any (== ourPath when present).
//   - otherForBranch: path of any worktree (other than ourPath) that has
//     branch checked out, if any.
//
// Path matching evaluates symlinks (macOS surfaces /private/var via
// `git worktree list` while callers usually pass /var/...).
//
// Both can be empty.
func findBranchWorktrees(wts []WorktreeInfo, ourPath, branch string) (string, string) {
	wtAtOurPath, other := "", ""
	canonOur := canonicalize(ourPath)
	for _, w := range wts {
		if w.Bare {
			continue
		}
		canonW := canonicalize(w.Path)
		if canonW == canonOur {
			wtAtOurPath = w.Path
			continue
		}
		if w.Branch == branch && other == "" {
			other = w.Path
		}
	}
	return wtAtOurPath, other
}

// canonicalize resolves symlinks so two paths that point at the same
// directory (e.g. /var/foo vs /private/var/foo on macOS) compare equal. If
// the path can't be resolved (doesn't exist yet, permission error), the
// original is returned — comparison falls back to a literal match.
func canonicalize(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
