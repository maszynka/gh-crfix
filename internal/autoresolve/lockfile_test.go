package autoresolve

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestDetectLockfile(t *testing.T) {
	cases := []struct {
		path string
		want LockfileKind
	}{
		{"bun.lock", Bun},
		{"apps/psypapka/bun.lock", Bun},
		{"bun.lockb", Bun},
		{"pnpm-lock.yaml", Pnpm},
		{"yarn.lock", Yarn},
		{"package-lock.json", Npm},
		{"Cargo.lock", Cargo},
		{"poetry.lock", Poetry},
		{"Pipfile.lock", Pipenv},
		{"uv.lock", Uv},
		{"Gemfile.lock", Bundler},
		{"go.sum", GoMod},
		// Negatives.
		{"README.md", NotALockfile},
		{"src/main.go", NotALockfile},
		{"package.json", NotALockfile},
		{"not-a-bun.lockfile", NotALockfile},
	}
	for _, c := range cases {
		if got := DetectLockfile(c.path); got != c.want {
			t.Errorf("DetectLockfile(%q) = %v; want %v", c.path, got, c.want)
		}
	}
}

func TestInstallCommand(t *testing.T) {
	cases := []struct {
		kind     LockfileKind
		wantBin  string
		wantArgs []string
	}{
		{Bun, "bun", []string{"install"}},
		{Pnpm, "pnpm", []string{"install"}},
		{Yarn, "yarn", []string{"install"}},
		{Npm, "npm", []string{"install"}},
		{Cargo, "cargo", []string{"update", "--workspace"}},
		{Poetry, "poetry", []string{"lock", "--no-update"}},
		{GoMod, "go", []string{"mod", "tidy"}},
	}
	for _, c := range cases {
		bin, args, ok := c.kind.InstallCommand()
		if !ok {
			t.Errorf("%s: InstallCommand returned ok=false", c.kind)
			continue
		}
		if bin != c.wantBin || !reflect.DeepEqual(args, c.wantArgs) {
			t.Errorf("%s: got (%q, %v); want (%q, %v)", c.kind, bin, args, c.wantBin, c.wantArgs)
		}
	}
	if _, _, ok := NotALockfile.InstallCommand(); ok {
		t.Error("NotALockfile should return ok=false")
	}
}

// TestRegenerate_Happy: package manager is on PATH, exec succeeds.
func TestRegenerate_Happy(t *testing.T) {
	var called struct {
		bin  string
		args []string
	}
	r := &LockfileRegenerator{
		WtPath:   "/wt",
		Exec:     func(ctx context.Context, wtPath, bin string, args ...string) error { called.bin = bin; called.args = args; return nil },
		LookPath: func(bin string) (string, error) { return "/usr/bin/" + bin, nil },
	}
	if err := r.Regenerate(context.Background(), Bun); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if called.bin != "bun" || !reflect.DeepEqual(called.args, []string{"install"}) {
		t.Fatalf("exec called with (%q, %v); want (bun, [install])", called.bin, called.args)
	}
}

// TestRegenerate_MissingBinary: when bun / pnpm / etc. isn't on PATH, we
// surface ErrPMMissing so the workflow can fall back to the LLM for that
// specific thread instead of silently failing.
func TestRegenerate_MissingBinary(t *testing.T) {
	var execCalls int
	r := &LockfileRegenerator{
		WtPath: "/wt",
		Exec: func(context.Context, string, string, ...string) error {
			execCalls++
			return nil
		},
		LookPath: func(bin string) (string, error) {
			return "", errors.New("exec: \"" + bin + "\": not found")
		},
	}
	err := r.Regenerate(context.Background(), Bun)
	if err == nil || !errors.Is(err, ErrPMMissing) {
		t.Fatalf("Regenerate: %v; want ErrPMMissing", err)
	}
	if execCalls != 0 {
		t.Fatalf("exec hook must not run when LookPath fails; got %d calls", execCalls)
	}
}

// TestRegenerate_ExecFails: PM exists but install errors. The wrapped error
// carries enough context for the log line.
func TestRegenerate_ExecFails(t *testing.T) {
	r := &LockfileRegenerator{
		WtPath:   "/wt",
		Exec:     func(context.Context, string, string, ...string) error { return errors.New("exit 1") },
		LookPath: func(string) (string, error) { return "/usr/bin/bun", nil },
	}
	err := r.Regenerate(context.Background(), Bun)
	if err == nil {
		t.Fatal("Regenerate should propagate exec failure")
	}
	if errors.Is(err, ErrPMMissing) {
		t.Fatalf("wrong sentinel: %v", err)
	}
}

// TestRegenerate_UnknownKind guards the zero-value branch.
func TestRegenerate_UnknownKind(t *testing.T) {
	r := &LockfileRegenerator{WtPath: "/wt"}
	if err := r.Regenerate(context.Background(), NotALockfile); err == nil {
		t.Fatal("Regenerate(NotALockfile) should error")
	}
}
