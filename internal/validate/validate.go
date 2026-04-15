// Package validate detects and runs the repo's test/validation step.
package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
func Detect(worktreePath, hookOverride string) Runner {
	// 1. Explicit flag
	if hookOverride != "" {
		if isExec(hookOverride) {
			return Runner{Kind: RunnerHook, Command: hookOverride}
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
func Run(worktreePath string, r Runner) Result {
	if r.Kind == RunnerNone {
		return Result{Available: false}
	}

	var cmd *exec.Cmd
	switch r.Kind {
	case RunnerHook:
		cmd = exec.Command(r.Command)
		cmd.Env = append(os.Environ(),
			"GH_CRFIX_VALIDATION_OUT="+filepath.Join(worktreePath, ".gh-crfix/validation.json"),
		)
	case RunnerBuiltin:
		parts := strings.Fields(r.Command)
		cmd = exec.Command(parts[0], parts[1:]...)
	}
	cmd.Dir = worktreePath

	out, err := cmd.CombinedOutput()
	success := err == nil
	summary := strings.TrimSpace(string(out))
	if len(summary) > 2000 {
		summary = summary[:2000] + "\n...(truncated)"
	}

	// Hook may write a JSON file with structured results.
	if r.Kind == RunnerHook {
		if res, ok := readHookJSON(filepath.Join(worktreePath, ".gh-crfix/validation.json")); ok {
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
