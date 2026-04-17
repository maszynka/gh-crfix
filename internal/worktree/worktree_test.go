package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runGit runs git in dir; t.Fatals with combined output on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// runGitOut runs git and returns trimmed stdout, or fails.
func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// makeUpstreamAndClone creates a bare "upstream" repo seeded with one commit on
// `main` and returns (bareDir, cloneDir). The clone is a real on-disk clone of
// the bare repo (so `origin` is configured and `fetch` works offline).
func makeUpstreamAndClone(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()

	// Seed repo: non-bare, will push to bare below.
	seed := t.TempDir()
	runGit(t, seed, "init", "--quiet", "--initial-branch=main")
	runGit(t, seed, "config", "user.email", "test@example.com")
	runGit(t, seed, "config", "user.name", "Test")
	runGit(t, seed, "commit", "--allow-empty", "-m", "init", "--quiet")

	// Bare upstream.
	bareDir = t.TempDir()
	runGit(t, bareDir, "init", "--bare", "--quiet", "--initial-branch=main")

	// Push seed -> bare.
	runGit(t, seed, "remote", "add", "origin", bareDir)
	runGit(t, seed, "push", "--quiet", "origin", "main")

	// Clone the bare into cloneDir (this is our "local checkout").
	parent := t.TempDir()
	cloneDir = filepath.Join(parent, "clone")
	cmd := exec.Command("git", "clone", "--quiet", bareDir, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	runGit(t, cloneDir, "config", "user.email", "test@example.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	return bareDir, cloneDir
}

// pushBranchToBare creates branch `name` in bareDir pointing at current main,
// by pushing from a throwaway worktree.
func pushBranchToBare(t *testing.T, bareDir, branch string) {
	t.Helper()
	// Clone to a scratch dir, create the branch, push back.
	scratchParent := t.TempDir()
	scratch := filepath.Join(scratchParent, "scratch")
	cmd := exec.Command("git", "clone", "--quiet", bareDir, scratch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone scratch: %v\n%s", err, out)
	}
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "config", "user.name", "Test")
	runGit(t, scratch, "checkout", "-b", branch, "--quiet")
	runGit(t, scratch, "commit", "--allow-empty", "-m", "branch "+branch, "--quiet")
	runGit(t, scratch, "push", "--quiet", "origin", branch)
}

func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell / git worktree semantics required")
	}
}

// ---------- RepoRoot ----------

func TestRepoRoot_ValidRepo(t *testing.T) {
	skipIfWindows(t)
	_, clone := makeUpstreamAndClone(t)

	got, err := RepoRoot(clone)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	// Resolve symlinks (macOS /var -> /private/var).
	wantResolved, _ := filepath.EvalSymlinks(clone)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Fatalf("RepoRoot = %q (resolved %q), want %q (resolved %q)",
			got, gotResolved, clone, wantResolved)
	}
}

func TestRepoRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := RepoRoot(dir); err == nil {
		t.Fatalf("expected error for non-git dir %s", dir)
	}
}

// ---------- PathFor ----------

func TestPathFor_Shape(t *testing.T) {
	got := PathFor("/tmp/repo", 42)
	want := filepath.Join("/tmp/repo", ".gh-crfix", "worktrees", "pr-42")
	if got != want {
		t.Fatalf("PathFor = %q, want %q", got, want)
	}
}

func TestPathFor_DifferentPRs(t *testing.T) {
	a := PathFor("/r", 1)
	b := PathFor("/r", 2)
	if a == b {
		t.Fatalf("expected different paths for pr-1 and pr-2, got %q", a)
	}
	if !strings.HasSuffix(a, "pr-1") {
		t.Fatalf("expected suffix pr-1, got %q", a)
	}
	if !strings.HasSuffix(b, "pr-2") {
		t.Fatalf("expected suffix pr-2, got %q", b)
	}
}

// ---------- Setup ----------

func TestSetup_CreatesTrackingBranchFromOrigin(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "feature-x")

	// Local does NOT yet have `feature-x` (clone only fetched HEAD by default).
	wt, err := Setup(clone, "feature-x", 101)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if wt != PathFor(clone, 101) {
		t.Fatalf("Setup returned %q, want %q", wt, PathFor(clone, 101))
	}

	// Worktree should exist and be on feature-x.
	branch := runGitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "feature-x" {
		t.Fatalf("worktree branch = %q, want feature-x", branch)
	}
}

func TestSetup_UsesExistingLocalBranch(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "feature-y")

	// Fetch + create a local branch first so the "local exists" path fires.
	runGit(t, clone, "fetch", "--quiet", "origin", "feature-y")
	runGit(t, clone, "branch", "feature-y", "origin/feature-y")

	wt, err := Setup(clone, "feature-y", 102)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	branch := runGitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "feature-y" {
		t.Fatalf("branch = %q, want feature-y", branch)
	}
}

func TestSetup_SecondCallCleansExistingDirtyFile(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "feature-z")

	wt, err := Setup(clone, "feature-z", 103)
	if err != nil {
		t.Fatalf("Setup #1: %v", err)
	}

	// Drop an untracked file in the worktree.
	dirty := filepath.Join(wt, "dirty.txt")
	if err := os.WriteFile(dirty, []byte("trash"), 0o644); err != nil {
		t.Fatalf("write dirty: %v", err)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("precondition: dirty file missing: %v", err)
	}

	// Second call should clean the worktree.
	wt2, err := Setup(clone, "feature-z", 103)
	if err != nil {
		t.Fatalf("Setup #2: %v", err)
	}
	if wt2 != wt {
		t.Fatalf("expected same worktree path, got %q vs %q", wt2, wt)
	}
	if _, err := os.Stat(dirty); !os.IsNotExist(err) {
		t.Fatalf("expected dirty file removed, stat err=%v", err)
	}
}

// ---------- MergeBase ----------

func TestMergeBase_HappyPath(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "mb-feature")

	wt, err := Setup(clone, "mb-feature", 200)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Advance main in bare so there's something to merge.
	scratchParent := t.TempDir()
	scratch := filepath.Join(scratchParent, "scratch")
	cmd := exec.Command("git", "clone", "--quiet", bare, scratch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone scratch: %v\n%s", err, out)
	}
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "config", "user.name", "Test")
	runGit(t, scratch, "checkout", "main", "--quiet")
	if err := os.WriteFile(filepath.Join(scratch, "main-only.txt"), []byte("m"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, scratch, "add", "main-only.txt")
	runGit(t, scratch, "commit", "-m", "advance main", "--quiet")
	runGit(t, scratch, "push", "--quiet", "origin", "main")

	if err := MergeBase(wt, "main"); err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	// main-only.txt should now exist in the worktree.
	if _, err := os.Stat(filepath.Join(wt, "main-only.txt")); err != nil {
		t.Fatalf("expected main-only.txt merged in, stat err=%v", err)
	}
}

func TestMergeBase_ConflictReturnsError(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "conflict-feature")

	// On main in bare, add conflict.txt with "MAIN".
	scratchParent := t.TempDir()
	scratch := filepath.Join(scratchParent, "scratch-main")
	if out, err := exec.Command("git", "clone", "--quiet", bare, scratch).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "config", "user.name", "Test")
	runGit(t, scratch, "checkout", "main", "--quiet")
	if err := os.WriteFile(filepath.Join(scratch, "conflict.txt"), []byte("MAIN\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, scratch, "add", "conflict.txt")
	runGit(t, scratch, "commit", "-m", "main adds conflict", "--quiet")
	runGit(t, scratch, "push", "--quiet", "origin", "main")

	// On conflict-feature in bare, add conflict.txt with "FEATURE".
	scratch2 := filepath.Join(scratchParent, "scratch-feature")
	if out, err := exec.Command("git", "clone", "--quiet", bare, scratch2).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	runGit(t, scratch2, "config", "user.email", "test@example.com")
	runGit(t, scratch2, "config", "user.name", "Test")
	runGit(t, scratch2, "checkout", "conflict-feature", "--quiet")
	// Reset to origin/main's parent so it doesn't already have MAIN's content.
	// conflict-feature was branched from initial empty commit before main-only
	// changes, so it's safe to just add differing content now.
	if err := os.WriteFile(filepath.Join(scratch2, "conflict.txt"), []byte("FEATURE\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, scratch2, "add", "conflict.txt")
	runGit(t, scratch2, "commit", "-m", "feature adds conflict", "--quiet")
	runGit(t, scratch2, "push", "--quiet", "origin", "conflict-feature")

	// Now Setup feature in clone and try to merge main -> conflict.
	wt, err := Setup(clone, "conflict-feature", 300)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if err := MergeBase(wt, "main"); err == nil {
		t.Fatalf("expected conflict error from MergeBase, got nil")
	}
}

// ---------- Remove ----------

func TestRemove_DeletesWorktreeDir_AndIsIdempotent(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "rm-feature")

	wt, err := Setup(clone, "rm-feature", 400)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("precondition: worktree dir missing: %v", err)
	}

	if err := Remove(clone, 400); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed, got err=%v", err)
	}

	// Second call must not blow up.
	if err := Remove(clone, 400); err != nil {
		t.Fatalf("Remove (2nd call): %v", err)
	}
}

// ---------- DirtyStatus ----------

func TestDirtyStatus_PristineIsEmpty(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "ds-feature")

	wt, err := Setup(clone, "ds-feature", 500)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	s, err := DirtyStatus(wt)
	if err != nil {
		t.Fatalf("DirtyStatus: %v", err)
	}
	if s != "" {
		t.Fatalf("expected empty DirtyStatus on pristine worktree, got %q", s)
	}
}

func TestDirtyStatus_UntrackedFileIsNonEmpty(t *testing.T) {
	skipIfWindows(t)
	bare, clone := makeUpstreamAndClone(t)
	pushBranchToBare(t, bare, "ds2-feature")

	wt, err := Setup(clone, "ds2-feature", 501)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}
	s, err := DirtyStatus(wt)
	if err != nil {
		t.Fatalf("DirtyStatus: %v", err)
	}
	if s == "" {
		t.Fatalf("expected non-empty DirtyStatus with untracked file, got empty")
	}
	if !strings.Contains(s, "stray.txt") {
		t.Fatalf("expected status to mention stray.txt, got %q", s)
	}
}
