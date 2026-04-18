package autoresolve

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// LockfileKind identifies the package manager a lockfile belongs to. Used to
// pick the right `<pm> install` invocation when regenerating.
type LockfileKind int

const (
	NotALockfile LockfileKind = iota
	Bun
	Pnpm
	Yarn
	Npm
	Cargo
	Poetry
	Pipenv
	Uv
	Bundler
	GoMod
)

// DetectLockfile returns the LockfileKind for a path. Matching is on the
// basename — `apps/foo/bun.lock` still resolves to Bun.
func DetectLockfile(p string) LockfileKind {
	switch filepath.Base(p) {
	case "bun.lock", "bun.lockb":
		return Bun
	case "pnpm-lock.yaml":
		return Pnpm
	case "yarn.lock":
		return Yarn
	case "package-lock.json":
		return Npm
	case "Cargo.lock":
		return Cargo
	case "poetry.lock":
		return Poetry
	case "Pipfile.lock":
		return Pipenv
	case "uv.lock":
		return Uv
	case "Gemfile.lock":
		return Bundler
	case "go.sum":
		return GoMod
	}
	return NotALockfile
}

// InstallCommand returns the argv the package manager needs to regenerate
// the lockfile from its manifest. Zero-arg entries use `<bin> install`; the
// go-module case regenerates via `go mod tidy`.
func (k LockfileKind) InstallCommand() (bin string, args []string, ok bool) {
	switch k {
	case Bun:
		return "bun", []string{"install"}, true
	case Pnpm:
		return "pnpm", []string{"install"}, true
	case Yarn:
		return "yarn", []string{"install"}, true
	case Npm:
		return "npm", []string{"install"}, true
	case Cargo:
		return "cargo", []string{"update", "--workspace"}, true
	case Poetry:
		return "poetry", []string{"lock", "--no-update"}, true
	case Pipenv:
		return "pipenv", []string{"lock"}, true
	case Uv:
		return "uv", []string{"lock"}, true
	case Bundler:
		return "bundle", []string{"lock", "--update"}, true
	case GoMod:
		return "go", []string{"mod", "tidy"}, true
	}
	return "", nil, false
}

// String gives a stable human label for logs.
func (k LockfileKind) String() string {
	switch k {
	case Bun:
		return "bun"
	case Pnpm:
		return "pnpm"
	case Yarn:
		return "yarn"
	case Npm:
		return "npm"
	case Cargo:
		return "cargo"
	case Poetry:
		return "poetry"
	case Pipenv:
		return "pipenv"
	case Uv:
		return "uv"
	case Bundler:
		return "bundle"
	case GoMod:
		return "go"
	}
	return "none"
}

// LockfileRegenerator regenerates lockfiles deterministically so review
// threads pointed at them can be resolved without calling the fix-model.
// Tests inject fakes via the exec/list/lookPath hooks; NewLockfileRegenerator
// wires the real shell-outs.
type LockfileRegenerator struct {
	WtPath string

	// Hooks for tests; zero values pick the production impls in Run().
	Exec     func(ctx context.Context, wtPath, bin string, args ...string) error
	LookPath func(bin string) (string, error)
}

// NewLockfileRegenerator builds a regenerator bound to wtPath with real
// exec hooks.
func NewLockfileRegenerator(wtPath string) *LockfileRegenerator {
	return &LockfileRegenerator{
		WtPath:   wtPath,
		Exec:     runCmdIn,
		LookPath: exec.LookPath,
	}
}

// ErrPMMissing is returned when the lockfile's package manager isn't on PATH.
// The caller should fall through to the LLM for that thread.
var ErrPMMissing = errors.New("package manager binary not found on PATH")

// Regenerate runs the appropriate `<pm> install` for kind inside WtPath.
// Caller is responsible for `git add`, `git commit`, `git push` after — the
// function intentionally stops at regenerating the working tree so the
// workflow owns staging/commit semantics.
//
// Returns ErrPMMissing if the package-manager binary is unavailable; the
// workflow routes those threads to the LLM as a fallback.
func (r *LockfileRegenerator) Regenerate(ctx context.Context, kind LockfileKind) error {
	bin, args, ok := kind.InstallCommand()
	if !ok {
		return fmt.Errorf("no install command for lockfile kind %s", kind)
	}
	if r.LookPath != nil {
		if _, err := r.LookPath(bin); err != nil {
			return fmt.Errorf("%w: %s", ErrPMMissing, bin)
		}
	}
	exec := r.Exec
	if exec == nil {
		exec = runCmdIn
	}
	if err := exec(ctx, r.WtPath, bin, args...); err != nil {
		return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return nil
}

// runCmdIn is the default exec hook — shells out with ctx cancellation.
func runCmdIn(ctx context.Context, wtPath, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = wtPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Trim loud CombinedOutput to keep logs readable; caller wraps it.
		trimmed := strings.TrimSpace(string(out))
		if len(trimmed) > 800 {
			trimmed = trimmed[:800] + "\n...(truncated)"
		}
		return fmt.Errorf("%w\n%s", err, trimmed)
	}
	return nil
}
