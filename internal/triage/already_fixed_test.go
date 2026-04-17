package triage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// initGitRepo creates a minimal git repo at dir. The returned function runs a
// git command inside the repo and fails the test on non-zero exit.
func initGitRepo(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir}, args...)
		cmd := exec.Command("git", full...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "test")
	run("config", "commit.gpgsign", "false")
	return run
}

// commitFile stages + commits a file with a specific author/committer date so
// file_changed_after_comment() logic is deterministic.
func commitFile(t *testing.T, dir, rel, body, isoDate string, run func(args ...string)) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
	run("add", rel)
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "-m", "edit "+rel)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+isoDate,
		"GIT_COMMITTER_DATE="+isoDate,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit %s: %v\n%s", rel, err, out)
	}
}

// TestClassifyThread_AlreadyLikelyFixed covers the heuristic that matches the
// Bash `file_changed_after_comment` + `is_simple_mechanical` path in
// gh-crfix/classify_one_thread: when a file was modified after the most recent
// comment was posted AND the comment looks mechanical, the Go port should
// emit decision="already_likely_fixed" instead of "auto".
func TestClassifyThread_AlreadyLikelyFixed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available on PATH")
	}
	dir := t.TempDir()
	run := initGitRepo(t, dir)

	// Initial commit with the file as it was *before* the nit-pick.
	past := "2020-01-01T00:00:00Z"
	commitFile(t, dir, "pkg/foo.go", "package foo\n// typo: wrk\n", past, run)

	// Comment created somewhere between the two commits.
	commentTime := "2021-06-15T12:00:00Z"

	// Second commit — the file changed AFTER the comment.
	future := "2022-01-01T00:00:00Z"
	commitFile(t, dir, "pkg/foo.go", "package foo\n// fixed: work\n", future, run)

	thread := Thread{
		ID:   "t-already-fixed",
		Path: "pkg/foo.go",
		Line: 2,
		Comments: []Comment{{
			Body:      "nit: typo wrk should be work",
			CreatedAt: commentTime,
		}},
	}

	got := ClassifyThread(dir, thread, true)
	if got.Decision != "already_likely_fixed" {
		t.Fatalf("decision=%q want already_likely_fixed (reason=%q)", got.Decision, got.Reason)
	}
}

// TestClassifyThread_MechanicalStaysAutoWhenFileNotRecentlyChanged ensures the
// "already_likely_fixed" branch only fires when the file actually changed
// after the comment — otherwise mechanical comments stay `auto`.
func TestClassifyThread_MechanicalStaysAutoWhenFileNotRecentlyChanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available on PATH")
	}
	dir := t.TempDir()
	run := initGitRepo(t, dir)

	// File committed well before the comment; no further edits.
	past := "2020-01-01T00:00:00Z"
	commitFile(t, dir, "pkg/bar.go", "package bar\n", past, run)

	// Comment posted much later, but file has not been touched since.
	later := time.Now().UTC().Format(time.RFC3339)

	thread := Thread{
		ID:   "t-auto",
		Path: "pkg/bar.go",
		Line: 1,
		Comments: []Comment{{
			Body:      "nit: typo in docs",
			CreatedAt: later,
		}},
	}

	got := ClassifyThread(dir, thread, true)
	if got.Decision != "auto" {
		t.Fatalf("decision=%q want auto (reason=%q)", got.Decision, got.Reason)
	}
}
