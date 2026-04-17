// Package triage classifies review threads deterministically.
package triage

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Comment is a single comment in a review thread.
type Comment struct {
	ID           string
	Body         string
	Path         string
	Line         int
	OriginalLine int
	Author       string
	CreatedAt    string
}

// Thread is a review thread on a PR.
type Thread struct {
	ID         string
	IsResolved bool
	IsOutdated bool
	Path       string
	Line       int
	Comments   []Comment
}

// Classification is the result of classifying a Thread.
type Classification struct {
	ThreadID           string
	Path               string
	Line               int
	Decision           string // "skip", "auto", "already_likely_fixed", "needs_llm"
	Reason             string
	Body               string
	ResolveWhenSkipped bool
}

// ReasonPRLevelComment is the triage reason for threads that have no file path
// (i.e. PR-level comments). Exported so that downstream packages (e.g. gate)
// can match on it without hard-coding the string.
const ReasonPRLevelComment = "PR-level comment (no file path)"

var (
	questionKeywordsRe = regexp.MustCompile(`(?i)(could you|can you|why|what about|do we need|is this intentional|question|clarify)`)
	actionableRe       = regexp.MustCompile(`(?i)(rename|change|fix|remove|add|use|should|must|please|nit:|typo|format|lint)`)
	mechanicalRe       = regexp.MustCompile(`(?i)(nit:|typo|spelling|grammar|format|formatting|prettier|eslint|lint|import order|unused import|sort imports|whitespace|newline|changelog|snapshot|generated file|docs?)`)
	nonActionableRe    = regexp.MustCompile(`(?i)(^lgtm$|^looks good|^thanks!?$|^nice!?$|great catch|^resolved\??$)`)
)

// IsQuestionOnly reports whether text is a question-only comment with no actionable keywords.
func IsQuestionOnly(text string) bool {
	compact := strings.Join(strings.Fields(text), " ")
	if !questionKeywordsRe.MatchString(compact) {
		return false
	}
	if !strings.HasSuffix(strings.TrimSpace(compact), "?") {
		return false
	}
	if actionableRe.MatchString(compact) {
		return false
	}
	return true
}

// IsSimpleMechanical reports whether text describes a simple mechanical fix.
func IsSimpleMechanical(text string) bool {
	return mechanicalRe.MatchString(text)
}

// IsNonActionable reports whether text is a non-actionable comment (e.g. "lgtm").
func IsNonActionable(text string) bool {
	words := strings.Fields(text)
	if len(words) > 8 {
		return false
	}
	return nonActionableRe.MatchString(strings.TrimSpace(text))
}

// ClassifyThread classifies a review thread given the worktree path and
// whether outdated threads should be included.
func ClassifyThread(worktreePath string, t Thread, includeOutdated bool) Classification {
	// Collect body from all comments
	var bodies []string
	for _, c := range t.Comments {
		bodies = append(bodies, c.Body)
	}
	body := strings.Join(bodies, "\n---\n")

	// Determine path
	path := t.Path
	if path == "" && len(t.Comments) > 0 {
		path = t.Comments[0].Path
	}

	// Determine line
	line := t.Line
	if line == 0 && len(t.Comments) > 0 {
		if t.Comments[0].Line != 0 {
			line = t.Comments[0].Line
		} else {
			line = t.Comments[0].OriginalLine
		}
	}

	base := Classification{
		ThreadID: t.ID,
		Path:     path,
		Line:     line,
		Body:     body,
	}

	// Priority order

	// 1. outdated
	if t.IsOutdated && !includeOutdated {
		base.Decision = "skip"
		base.Reason = "outdated thread"
		return base
	}

	// 2. empty path => PR-level comment
	if path == "" {
		base.Decision = "needs_llm"
		base.Reason = ReasonPRLevelComment
		return base
	}

	// 3. non-actionable
	if IsNonActionable(body) {
		base.Decision = "skip"
		base.Reason = "non-actionable comment"
		base.ResolveWhenSkipped = true
		return base
	}

	// 4. question-only
	if IsQuestionOnly(body) {
		base.Decision = "skip"
		base.Reason = "question-only thread"
		return base
	}

	// 5. file doesn't exist (or is inaccessible — skip either way to avoid
	//    misclassifying threads on permission-denied or similar errors)
	if _, err := os.Stat(filepath.Join(worktreePath, path)); err != nil {
		base.Decision = "skip"
		if os.IsNotExist(err) {
			base.Reason = "file no longer exists in worktree"
		} else {
			base.Reason = "unable to stat file in worktree"
		}
		return base
	}

	// 6. file changed after the most recent comment: mechanical bodies are
	//    most likely already addressed. Mirrors the Bash
	//    file_changed_after_comment + is_simple_mechanical check.
	if IsSimpleMechanical(body) && fileChangedAfterComment(worktreePath, path, lastCreatedAt(t)) {
		base.Decision = "already_likely_fixed"
		base.Reason = "file changed after comment and issue looks mechanical"
		return base
	}

	// 7. simple mechanical
	if IsSimpleMechanical(body) {
		base.Decision = "auto"
		base.Reason = "mechanical/simple comment"
		return base
	}

	// 8. default
	base.Decision = "needs_llm"
	base.Reason = "needs semantic review"
	return base
}

// lastCreatedAt returns the most recent non-empty CreatedAt from a thread's
// comments. Matches the Bash `thread_last_created_at` helper.
func lastCreatedAt(t Thread) string {
	for i := len(t.Comments) - 1; i >= 0; i-- {
		if ts := strings.TrimSpace(t.Comments[i].CreatedAt); ts != "" {
			return ts
		}
	}
	return ""
}

// fileChangedAfterComment reports whether git log shows any commits touching
// `file` in the worktree after timestamp `ts`. When any input is missing it
// returns false (caller falls through to the normal auto/needs_llm branches).
func fileChangedAfterComment(worktreePath, file, ts string) bool {
	if file == "" || ts == "" {
		return false
	}
	// `git log --since=<ts> --format=%H -- <file>` prints a SHA per matching
	// commit. Any non-empty output means the file changed after ts.
	cmd := exec.Command("git", "-C", worktreePath,
		"log", "--since="+ts, "--format=%H", "--", file)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
