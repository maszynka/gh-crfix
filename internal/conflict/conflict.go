// Package conflict detects and, with AI help, fixes committed git merge
// conflict markers in a worktree.
package conflict

import (
	"fmt"
	"os/exec"
	"strings"
)

// DetectMarkers returns tracked files in wtPath that contain committed merge
// conflict markers (a line beginning with `<<<<<<< `). The result is
// deduplicated. An empty list + nil error means the tree is clean.
func DetectMarkers(wtPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", wtPath, "grep", "-Il", "^<<<<<<< ")
	out, err := cmd.Output()
	if err != nil {
		// `git grep` exits 1 when no matches — treat as clean.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("git grep: %w", err)
	}
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		files = append(files, line)
	}
	return files, nil
}

// BuildFixPrompt builds the free-form prompt sent to the fix model for
// resolving the given set of conflicted files.
func BuildFixPrompt(files []string) string {
	var sb strings.Builder
	sb.WriteString("The following files have git merge conflict markers " +
		"(<<<<<<< HEAD, =======, >>>>>>>) that were accidentally committed to " +
		"the branch. Fix each file by resolving the conflicts properly:\n")
	for _, f := range files {
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	sb.WriteString(`
For each file:
1. Read the file and find all conflict marker sections
2. Resolve each conflict — prefer the HEAD/PR-branch version (between <<<<<<< HEAD and =======) unless the other side is clearly an improvement
3. Remove all conflict marker lines (<<<<<<< HEAD, =======, >>>>>>> ...)
4. Save the file and run: git add <file>

After all files are fixed: git commit -m 'fix: resolve committed conflict markers' && git push
`)
	return sb.String()
}
