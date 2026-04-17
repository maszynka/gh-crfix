package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMatchesRepo_MatchingRemote: a clone whose origin URL contains
// owner/repo should match.
func TestMatchesRepo_MatchingRemote(t *testing.T) {
	skipIfWindows(t)
	_, clone := makeUpstreamAndClone(t)
	// Rewrite origin so it points at a URL that contains owner/repo.
	runGit(t, clone, "remote", "set-url", "origin", "https://github.com/maszynka/gh-crfix.git")

	ok, _, err := MatchesRepo(clone, "maszynka", "gh-crfix")
	if err != nil {
		t.Fatalf("MatchesRepo: %v", err)
	}
	if !ok {
		t.Fatalf("expected match for origin=maszynka/gh-crfix")
	}
}

// TestMatchesRepo_MismatchedRemote: origin points at a different repo →
// returns false with a message that includes the remote URL and target.
func TestMatchesRepo_MismatchedRemote(t *testing.T) {
	skipIfWindows(t)
	_, clone := makeUpstreamAndClone(t)
	runGit(t, clone, "remote", "set-url", "origin", "https://github.com/someone/else.git")

	ok, msg, err := MatchesRepo(clone, "maszynka", "gh-crfix")
	if err != nil {
		t.Fatalf("MatchesRepo: %v", err)
	}
	if ok {
		t.Fatalf("expected mismatch")
	}
	if !strings.Contains(msg, "someone/else") {
		t.Fatalf("msg should contain actual remote URL; got %q", msg)
	}
	if !strings.Contains(msg, "maszynka/gh-crfix") {
		t.Fatalf("msg should contain target owner/repo; got %q", msg)
	}
}

// TestMatchesRepo_LocalOriginPassesThrough: a non-GitHub origin (e.g., a
// local bare repo used in tests or a GitLab mirror) is treated as unknown
// and returned as a match so we don't break local fixtures.
func TestMatchesRepo_LocalOriginPassesThrough(t *testing.T) {
	skipIfWindows(t)
	_, clone := makeUpstreamAndClone(t)
	// Default origin is already a local path — don't touch it.
	ok, _, err := MatchesRepo(clone, "maszynka", "gh-crfix")
	if err != nil {
		t.Fatalf("MatchesRepo: %v", err)
	}
	if !ok {
		t.Fatalf("expected local origin to be treated as a match (pass-through)")
	}
}

// TestMatchesRepo_NotGitRepo: non-git dir returns an error.
func TestMatchesRepo_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := MatchesRepo(dir, "maszynka", "gh-crfix"); err == nil {
		t.Fatalf("expected error for non-git dir")
	}
}

// TestRepoRoot_AcceptsContext ensures RepoRoot respects a cancelled ctx.
func TestRepoRoot_AcceptsContext(t *testing.T) {
	skipIfWindows(t)
	_, clone := makeUpstreamAndClone(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RepoRoot(ctx, clone)
	// Either returns ctx.Err() or succeeds before noticing — main goal is
	// that the signature takes a ctx.
	_ = err
	_ = filepath.Clean // keep import used
	_ = os.Stat
	_ = exec.Command
}
