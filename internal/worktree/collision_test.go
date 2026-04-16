package worktree

import (
	"os/exec"
	"strings"
	"testing"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "core.ignorecase", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func writeAndCommit(t *testing.T, dir, path, content string) {
	t.Helper()
	// Work with git plumbing so the test is safe on case-insensitive
	// filesystems (e.g. default macOS/Windows).
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(content)
	hash, err := cmd.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	sha := strings.TrimRight(string(hash), "\n")
	if out, err := gitCombined(dir, "update-index", "--add", "--cacheinfo", "100644,"+sha+","+path); err != nil {
		t.Fatalf("git update-index %s: %v\n%s", path, err, out)
	}
	if out, err := gitCombined(dir, "commit", "-m", "add "+path, "--quiet"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func gitCombined(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDetectCaseCollisions_Empty(t *testing.T) {
	dir := initGitRepo(t)
	writeAndCommit(t, dir, "README.md", "hello")
	writeAndCommit(t, dir, "src/main.go", "package main")

	groups, err := DetectCaseCollisions(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %v", groups)
	}
}

func TestDetectCaseCollisions_Found(t *testing.T) {
	dir := initGitRepo(t)
	writeAndCommit(t, dir, "README.md", "hello")
	writeAndCommit(t, dir, "readme.md", "hello2")
	writeAndCommit(t, dir, "src/Util.go", "package src")
	writeAndCommit(t, dir, "src/util.go", "package src2")

	groups, err := DetectCaseCollisions(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %v", len(groups), groups)
	}
	// First group should contain README.md + readme.md
	if len(groups[0]) != 2 || groups[0][0] != "README.md" || groups[0][1] != "readme.md" {
		t.Fatalf("unexpected group 0: %v", groups[0])
	}
	if len(groups[1]) != 2 || groups[1][0] != "src/Util.go" || groups[1][1] != "src/util.go" {
		t.Fatalf("unexpected group 1: %v", groups[1])
	}
}
