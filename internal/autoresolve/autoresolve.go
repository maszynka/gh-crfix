// Package autoresolve handles merge conflicts in files whose resolution is
// deterministic — lockfiles, generated manifests, changelogs, CI config —
// so the fix-model isn't burned on them. This is the Go port of the bash
// `merge_base_branch` auto-resolve block (see gh-crfix around lines 1975-
// 2000).
package autoresolve

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
)

// Side picks which version wins for a deterministic auto-resolve.
type Side string

const (
	// OurSide keeps the PR branch's version (HEAD). Used for files owned by
	// the PR author: CI configs, changelogs, generated artifacts we don't
	// want overwritten by the base branch.
	OurSide Side = "ours"
	// TheirSide keeps the base branch's version. Used for lockfiles — the
	// base's lock is considered authoritative so downstream `<pm> install`
	// can regenerate consistently against the PR's package.json / pyproject
	// / Cargo.toml / etc.
	TheirSide Side = "theirs"
)

// Result summarizes a single auto-resolve pass.
type Result struct {
	// Resolved maps path → side used. Keyed so callers can log details.
	Resolved map[string]Side
	// Remaining lists conflicted paths that didn't match any deterministic
	// pattern — the caller should hand these to the fix-model.
	Remaining []string
}

// Classify picks a resolution side for a file path, or returns ("", false)
// when no deterministic answer exists.
//
// Patterns mirror the bash script:
//
//	*.lock | *-lock.yaml | *-lock.json | *.tsbuildinfo → theirs
//	CHANGELOG.md (any dir, any case)                   → ours
//	.github/workflows/*                                 → ours
//	.github/docs/*                                      → ours
//	.github/.auto-fix-iterations                        → ours
//	thread-responses.json                               → ours
func Classify(p string) (Side, bool) {
	base := path.Base(p)

	// Lockfiles — always prefer base branch.
	if strings.HasSuffix(base, ".lock") ||
		strings.HasSuffix(base, "-lock.yaml") ||
		strings.HasSuffix(base, "-lock.json") ||
		strings.HasSuffix(base, ".tsbuildinfo") ||
		// Common specific names that aren't covered by the suffix rules.
		base == "bun.lockb" ||
		base == "Cargo.lock" ||
		base == "go.sum" ||
		base == "Pipfile.lock" ||
		base == "poetry.lock" ||
		base == "uv.lock" ||
		base == "Gemfile.lock" {
		return TheirSide, true
	}

	// Changelog files — PR author's version wins (they curated the entries).
	lowerBase := strings.ToLower(base)
	if lowerBase == "changelog.md" || lowerBase == "changelog" {
		return OurSide, true
	}

	// Artifacts the PR owns.
	if base == "thread-responses.json" {
		return OurSide, true
	}

	// GitHub configuration the PR owns.
	if strings.HasPrefix(p, ".github/workflows/") ||
		strings.HasPrefix(p, ".github/docs/") ||
		p == ".github/.auto-fix-iterations" {
		return OurSide, true
	}

	return "", false
}

// Runner carries the ctx and working directory for the git helpers. A small
// indirection so tests can fake the shell-out without touching exec.
type Runner struct {
	Ctx     context.Context
	WtPath  string
	git     func(ctx context.Context, wtPath string, args ...string) error
	listFn  func(ctx context.Context, wtPath string) ([]string, error)
}

// NewRunner returns a Runner wired to real git invocations.
func NewRunner(ctx context.Context, wtPath string) *Runner {
	return &Runner{
		Ctx:    ctx,
		WtPath: wtPath,
		git:    gitIn,
		listFn: listConflictedFiles,
	}
}

// Apply runs one deterministic pass over the conflicted files in r.WtPath.
// For each classified file it does `git checkout --<side>` + `git add`; the
// rest is returned in Remaining.
//
// The caller owns the final `git commit` and `git push` — this keeps the
// LLM fallback path from double-committing when Remaining is non-empty.
func (r *Runner) Apply() (Result, error) {
	files, err := r.listFn(r.Ctx, r.WtPath)
	if err != nil {
		return Result{}, fmt.Errorf("list conflicted files: %w", err)
	}
	res := Result{Resolved: map[string]Side{}}
	for _, f := range files {
		side, ok := Classify(f)
		if !ok {
			res.Remaining = append(res.Remaining, f)
			continue
		}
		flag := "--" + string(side)
		if err := r.git(r.Ctx, r.WtPath, "checkout", flag, f); err != nil {
			// Leave it to the LLM rather than aborting the whole PR.
			res.Remaining = append(res.Remaining, f)
			continue
		}
		if err := r.git(r.Ctx, r.WtPath, "add", f); err != nil {
			res.Remaining = append(res.Remaining, f)
			continue
		}
		res.Resolved[f] = side
	}
	return res, nil
}

// CommitAndPush finalizes an auto-resolve-only merge: commit the in-progress
// merge and push. Safe to call only when Apply left Remaining empty.
func (r *Runner) CommitAndPush() error {
	if err := r.git(r.Ctx, r.WtPath, "commit", "--no-edit"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if err := r.git(r.Ctx, r.WtPath, "push", "--quiet"); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// listConflictedFiles returns files in a conflicted (U) state inside wtPath.
func listConflictedFiles(ctx context.Context, wtPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", wtPath,
		"diff", "--name-only", "--diff-filter=U")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		files = append(files, line)
	}
	return files, nil
}

func gitIn(ctx context.Context, wtPath string, args ...string) error {
	full := append([]string{"-C", wtPath}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}
