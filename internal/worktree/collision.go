package worktree

import (
	"os/exec"
	"sort"
	"strings"
)

// DetectCaseCollisions returns groups of HEAD-tracked paths whose lowercase
// forms collide (two or more distinct paths that differ only in case). Each
// returned group is sorted and the outer slice is sorted by first path.
func DetectCaseCollisions(wtPath string) ([][]string, error) {
	cmd := exec.Command("git", "-C", wtPath, "ls-tree", "-r", "--name-only", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	byLower := map[string]map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		lc := strings.ToLower(line)
		if byLower[lc] == nil {
			byLower[lc] = map[string]struct{}{}
		}
		byLower[lc][line] = struct{}{}
	}
	var groups [][]string
	for _, set := range byLower {
		if len(set) < 2 {
			continue
		}
		g := make([]string, 0, len(set))
		for p := range set {
			g = append(g, p)
		}
		sort.Strings(g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups, nil
}

// DirtyStatus returns the output of `git status --short` (empty means clean).
func DirtyStatus(wtPath string) (string, error) {
	cmd := exec.Command("git", "-C", wtPath, "status", "--short")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
