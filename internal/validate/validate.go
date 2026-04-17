// Package validate detects and runs the repo's test/validation step.
package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultValidateTimeout bounds the validation runner so a hung `bun test`,
// `pnpm test`, or custom hook doesn't stall the whole pipeline. Override via
// env `GH_CRFIX_VALIDATE_TIMEOUT` (Go duration, e.g. "30m").
const defaultValidateTimeout = 15 * time.Minute

// RunnerKind identifies what kind of runner is available.
type RunnerKind int

const (
	RunnerNone    RunnerKind = iota
	RunnerHook               // .gh-crfix/validate.sh or custom path
	RunnerBuiltin            // built-in command from package.json
)

// Runner describes how to run validation.
type Runner struct {
	Kind    RunnerKind
	Command string // path to script, or "npm test" etc.
}

// Result is the outcome of validation.
type Result struct {
	Available   bool   `json:"available"`
	Ran         bool   `json:"ran"`
	Success     bool   `json:"success"`
	TestsFailed bool   `json:"tests_failed"`
	Summary     string `json:"summary"`
}

// Detect finds the best available validation runner.
// hookOverride is an explicit path from --validate-hook (empty = auto-detect).
//
// When hookOverride is relative, it is resolved against worktreePath so that
// `--validate-hook scripts/ci.sh` finds the hook inside the PR worktree rather
// than the (unpredictable) process CWD. Absolute paths are used verbatim.
func Detect(worktreePath, hookOverride string) Runner {
	// 1. Explicit flag
	if hookOverride != "" {
		candidate := hookOverride
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(worktreePath, candidate)
		}
		if isExec(candidate) {
			return Runner{Kind: RunnerHook, Command: candidate}
		}
	}

	// 2. Repo-local hooks
	for _, rel := range []string{
		".gh-crfix/validate.sh",
		"scripts/gh-crfix-validate.sh",
	} {
		p := filepath.Join(worktreePath, rel)
		if isExec(p) {
			return Runner{Kind: RunnerHook, Command: p}
		}
	}

	// 3. Detect from package.json
	if cmd := detectPackageTestCmd(worktreePath); cmd != "" {
		return Runner{Kind: RunnerBuiltin, Command: cmd}
	}

	return Runner{Kind: RunnerNone}
}

// Run executes the validation runner inside worktreePath and returns the result.
//
// ctx is honored for cancellation/deadline. A default 15-minute budget is
// applied if ctx has no earlier deadline; override via env
// `GH_CRFIX_VALIDATE_TIMEOUT`.
//
// stream (optional, may be nil) receives the runner's combined stdout/stderr
// line by line as it's produced — this is what the caller uses to keep the
// terminal alive during long test suites. The full output is still collected
// and returned in Result.Summary (truncated at 2000 chars).
func Run(ctx context.Context, worktreePath string, r Runner, stream io.Writer) Result {
	if r.Kind == RunnerNone {
		return Result{Available: false}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Apply the default timeout if the caller didn't supply a stricter one.
	timeout := envDuration("GH_CRFIX_VALIDATE_TIMEOUT", defaultValidateTimeout)
	if timeout > 0 {
		if existing, ok := ctx.Deadline(); !ok || time.Until(existing) > timeout {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}

	var cmd *exec.Cmd
	jsonOut := filepath.Join(worktreePath, ".gh-crfix/validation.json")
	switch r.Kind {
	case RunnerHook:
		// Ensure we never read a stale JSON result from a previous run —
		// preserved worktrees would otherwise short-circuit to the old data.
		_ = os.Remove(jsonOut)
		cmd = exec.CommandContext(ctx, r.Command)
		cmd.Env = append(os.Environ(),
			"GH_CRFIX_VALIDATION_OUT="+jsonOut,
		)
	case RunnerBuiltin:
		parts := strings.Fields(r.Command)
		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	}
	cmd.Dir = worktreePath

	// Stream stdout+stderr line-by-line while collecting the full output for
	// the Summary. If stream is nil, we still collect but don't mirror.
	var collected bytes.Buffer
	var writer io.Writer = &collected
	if stream != nil {
		writer = io.MultiWriter(&collected, stream)
	}
	cmd.Stdout = writer
	cmd.Stderr = writer

	err := cmd.Run()
	success := err == nil
	summary := strings.TrimSpace(collected.String())
	if len(summary) > 2000 {
		summary = summary[:2000] + "\n...(truncated)"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		summary = fmt.Sprintf("validation timed out after %s (set GH_CRFIX_VALIDATE_TIMEOUT to override)\n%s",
			timeout, summary)
	}

	// Hook may write a JSON file with structured results.
	if r.Kind == RunnerHook {
		if res, ok := readHookJSON(jsonOut); ok {
			return res
		}
	}

	return Result{
		Available:   true,
		Ran:         true,
		Success:     success,
		TestsFailed: !success,
		Summary:     summary,
	}
}

// envDuration reads a Go duration from env, returns fallback on empty/invalid.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func readHookJSON(path string) (Result, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, false
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, false
	}
	return r, true
}

func isExec(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !fi.IsDir() && fi.Mode()&0o111 != 0
}

func detectPackageTestCmd(worktreePath string) string {
	pkgPath := filepath.Join(worktreePath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	if _, ok := pkg.Scripts["test"]; !ok {
		return ""
	}

	// Pick the right package manager.
	for _, f := range []struct{ lock, cmd string }{
		{"bun.lock", "bun test"},
		{"bun.lockb", "bun test"},
		{"pnpm-lock.yaml", "pnpm test"},
		{"yarn.lock", "yarn test"},
		{"package-lock.json", "npm test"},
	} {
		if _, err := os.Stat(filepath.Join(worktreePath, f.lock)); err == nil {
			return f.cmd
		}
	}
	return fmt.Sprintf("npm test")
}
