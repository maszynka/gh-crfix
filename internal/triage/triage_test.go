package triage

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// IsQuestionOnly
// ---------------------------------------------------------------------------

func TestIsQuestionOnly(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  bool
	}{
		{
			name: "question ending with question mark",
			text: "Could you explain why?",
			want: true,
		},
		{
			name: "actionable rename request",
			text: "Please rename this",
			want: false,
		},
		{
			name: "question but has actionable keyword rename",
			text: "Can you rename this variable?",
			want: false,
		},
		{
			name: "what about question",
			text: "What about this approach?",
			want: true,
		},
		{
			name: "question with fix keyword",
			text: "Can you fix this?",
			want: false,
		},
		{
			name: "question with change keyword",
			text: "Could you change this?",
			want: false,
		},
		{
			name: "no question mark",
			text: "Could you explain why",
			want: false,
		},
		{
			name: "clarify question",
			text: "Can you clarify this?",
			want: true,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := IsQuestionOnly(tc.text)
			if got != tc.want {
				t.Errorf("IsQuestionOnly(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsSimpleMechanical
// ---------------------------------------------------------------------------

func TestIsSimpleMechanical(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  bool
	}{
		{
			name: "nit typo",
			text: "nit: fix typo",
			want: true,
		},
		{
			name: "complex refactor",
			text: "refactor the auth flow",
			want: false,
		},
		{
			name: "unused import",
			text: "unused import on line 5",
			want: true,
		},
		{
			name: "eslint error",
			text: "eslint error here",
			want: true,
		},
		{
			name: "update changelog",
			text: "update changelog",
			want: true,
		},
		{
			name: "spelling",
			text: "spelling mistake in comment",
			want: true,
		},
		{
			name: "formatting",
			text: "formatting issue",
			want: true,
		},
		{
			name: "prettier",
			text: "run prettier on this file",
			want: true,
		},
		{
			name: "whitespace",
			text: "trailing whitespace",
			want: true,
		},
		{
			name: "sort imports",
			text: "sort imports please",
			want: true,
		},
		{
			name: "non-mechanical logic change",
			text: "this function has a logic bug",
			want: false,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := IsSimpleMechanical(tc.text)
			if got != tc.want {
				t.Errorf("IsSimpleMechanical(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsNonActionable
// ---------------------------------------------------------------------------

func TestIsNonActionable(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  bool
	}{
		{
			name: "lgtm",
			text: "lgtm",
			want: true,
		},
		{
			name: "please fix this",
			text: "please fix this",
			want: false,
		},
		{
			name: "looks good to me",
			text: "looks good to me",
			want: true,
		},
		{
			name: "thanks with exclamation",
			text: "thanks!",
			want: true,
		},
		{
			name: "nice",
			text: "nice!",
			want: true,
		},
		{
			name: "long comment is actionable",
			text: "this is a longer comment that has more than eight words total",
			want: false,
		},
		{
			name: "resolved",
			text: "resolved",
			want: true,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := IsNonActionable(tc.text)
			if got != tc.want {
				t.Errorf("IsNonActionable(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ClassifyThread helpers
// ---------------------------------------------------------------------------

func makeThread(id, path string, line int, resolved, outdated bool, comments []Comment) Thread {
	return Thread{
		ID:         id,
		IsResolved: resolved,
		IsOutdated: outdated,
		Path:       path,
		Line:       line,
		Comments:   comments,
	}
}

func makeComment(body string) Comment {
	return Comment{
		ID:   "c1",
		Body: body,
	}
}

// writeFile writes content to path, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// ClassifyThread
// ---------------------------------------------------------------------------

func TestClassifyThread(t *testing.T) {
	t.Run("question-only thread is skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "pkg/foo.go"), "package foo\n")

		thread := makeThread("t1", "pkg/foo.go", 1, false, false,
			[]Comment{makeComment("Could you explain why?")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "skip" {
			t.Errorf("decision = %q, want %q", got.Decision, "skip")
		}
		if got.Reason != "question-only thread" {
			t.Errorf("reason = %q, want %q", got.Reason, "question-only thread")
		}
	})

	t.Run("complex review comment needs_llm", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "pkg/bar.go"), "package bar\n\nfunc Bar() {}\n")

		thread := makeThread("t2", "pkg/bar.go", 3, false, false,
			[]Comment{makeComment("The error handling here is incorrect and could lead to a nil pointer dereference in production.")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "needs_llm" {
			t.Errorf("decision = %q, want %q", got.Decision, "needs_llm")
		}
	})

	t.Run("non-actionable lgtm is skipped with resolve", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "pkg/baz.go"), "package baz\n")

		thread := makeThread("t3", "pkg/baz.go", 1, false, false,
			[]Comment{makeComment("lgtm")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "skip" {
			t.Errorf("decision = %q, want %q", got.Decision, "skip")
		}
		if got.Reason != "non-actionable comment" {
			t.Errorf("reason = %q, want %q", got.Reason, "non-actionable comment")
		}
		if !got.ResolveWhenSkipped {
			t.Errorf("ResolveWhenSkipped = false, want true")
		}
	})

	t.Run("mechanical nit is auto", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "pkg/nit.go"), "package nit\n")

		thread := makeThread("t4", "pkg/nit.go", 1, false, false,
			[]Comment{makeComment("nit: fix typo in variable name")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "auto" {
			t.Errorf("decision = %q, want %q", got.Decision, "auto")
		}
	})

	t.Run("PR-level comment (no file path) needs_llm", func(t *testing.T) {
		dir := t.TempDir()

		thread := makeThread("t5", "", 0, false, false,
			[]Comment{makeComment("This PR needs better documentation.")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "needs_llm" {
			t.Errorf("decision = %q, want %q", got.Decision, "needs_llm")
		}
		if got.Reason != "PR-level comment (no file path)" {
			t.Errorf("reason = %q, want %q", got.Reason, "PR-level comment (no file path)")
		}
	})

	t.Run("outdated thread skipped when includeOutdated=false", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "pkg/old.go"), "package old\n")

		thread := makeThread("t6", "pkg/old.go", 1, false, true,
			[]Comment{makeComment("This was an old comment.")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "skip" {
			t.Errorf("decision = %q, want %q", got.Decision, "skip")
		}
		if got.Reason != "outdated thread" {
			t.Errorf("reason = %q, want %q", got.Reason, "outdated thread")
		}
	})

	t.Run("outdated thread included when includeOutdated=true", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "pkg/old.go"), "package old\n")

		thread := makeThread("t6b", "pkg/old.go", 1, false, true,
			[]Comment{makeComment("nit: typo here")})

		got := ClassifyThread(dir, thread, true)
		if got.Decision == "skip" && got.Reason == "outdated thread" {
			t.Errorf("outdated thread was skipped despite includeOutdated=true")
		}
	})

	t.Run("non-existent file is skipped", func(t *testing.T) {
		dir := t.TempDir()
		// deliberately do NOT create the file

		thread := makeThread("t7", "pkg/ghost.go", 5, false, false,
			[]Comment{makeComment("This line has an issue.")})

		got := ClassifyThread(dir, thread, false)
		if got.Decision != "skip" {
			t.Errorf("decision = %q, want %q", got.Decision, "skip")
		}
		if got.Reason != "file no longer exists in worktree" {
			t.Errorf("reason = %q, want %q", got.Reason, "file no longer exists in worktree")
		}
	})

	t.Run("ThreadID and Path propagated", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

		thread := makeThread("tid-99", "main.go", 1, false, false,
			[]Comment{makeComment("nit: spacing")})

		got := ClassifyThread(dir, thread, false)
		if got.ThreadID != "tid-99" {
			t.Errorf("ThreadID = %q, want %q", got.ThreadID, "tid-99")
		}
		if got.Path != "main.go" {
			t.Errorf("Path = %q, want %q", got.Path, "main.go")
		}
	})
}
