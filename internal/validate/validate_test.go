package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeExec writes content to path with executable permissions.
func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// writeFile writes without exec permission.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX exec-bit / shell semantics required")
	}
}

// ---------- Detect ----------

func TestDetect_HookOverrideExecutable(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hookDir := t.TempDir()
	hook := filepath.Join(hookDir, "custom-hook.sh")
	writeExec(t, hook, "#!/bin/sh\nexit 0\n")

	r := Detect(wt, hook)
	if r.Kind != RunnerHook {
		t.Fatalf("expected RunnerHook, got %v", r.Kind)
	}
	if r.Command != hook {
		t.Fatalf("expected command %q, got %q", hook, r.Command)
	}
}

func TestDetect_HookOverrideNotExecutable_FallsThrough(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hookDir := t.TempDir()
	hook := filepath.Join(hookDir, "not-exec.sh")
	writeFile(t, hook, "#!/bin/sh\nexit 0\n") // 0o644, not exec

	r := Detect(wt, hook)
	if r.Kind != RunnerNone {
		t.Fatalf("expected fallthrough to RunnerNone, got %v (cmd=%q)", r.Kind, r.Command)
	}
}

func TestDetect_HookOverrideMissing_FallsThrough(t *testing.T) {
	wt := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist.sh")

	r := Detect(wt, missing)
	if r.Kind != RunnerNone {
		t.Fatalf("expected RunnerNone, got %v", r.Kind)
	}
}

func TestDetect_RepoLocalValidateSh(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	writeExec(t, hook, "#!/bin/sh\nexit 0\n")

	r := Detect(wt, "")
	if r.Kind != RunnerHook {
		t.Fatalf("expected RunnerHook, got %v", r.Kind)
	}
	if r.Command != hook {
		t.Fatalf("expected %q, got %q", hook, r.Command)
	}
}

func TestDetect_ScriptsValidateSh(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, "scripts", "gh-crfix-validate.sh")
	writeExec(t, hook, "#!/bin/sh\nexit 0\n")

	r := Detect(wt, "")
	if r.Kind != RunnerHook {
		t.Fatalf("expected RunnerHook, got %v", r.Kind)
	}
	if r.Command != hook {
		t.Fatalf("expected %q, got %q", hook, r.Command)
	}
}

func TestDetect_PackageJSONWithBunLock(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `{"scripts":{"test":"echo ok"}}`)
	writeFile(t, filepath.Join(wt, "bun.lock"), "")

	r := Detect(wt, "")
	if r.Kind != RunnerBuiltin {
		t.Fatalf("expected RunnerBuiltin, got %v", r.Kind)
	}
	if r.Command != "bun test" {
		t.Fatalf("expected 'bun test', got %q", r.Command)
	}
}

func TestDetect_PackageJSONWithPnpmLock(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `{"scripts":{"test":"echo ok"}}`)
	writeFile(t, filepath.Join(wt, "pnpm-lock.yaml"), "")

	r := Detect(wt, "")
	if r.Kind != RunnerBuiltin {
		t.Fatalf("expected RunnerBuiltin, got %v", r.Kind)
	}
	if r.Command != "pnpm test" {
		t.Fatalf("expected 'pnpm test', got %q", r.Command)
	}
}

func TestDetect_PackageJSONWithYarnLock(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `{"scripts":{"test":"echo ok"}}`)
	writeFile(t, filepath.Join(wt, "yarn.lock"), "")

	r := Detect(wt, "")
	if r.Kind != RunnerBuiltin {
		t.Fatalf("expected RunnerBuiltin, got %v", r.Kind)
	}
	if r.Command != "yarn test" {
		t.Fatalf("expected 'yarn test', got %q", r.Command)
	}
}

func TestDetect_PackageJSONWithNpmLock(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `{"scripts":{"test":"echo ok"}}`)
	writeFile(t, filepath.Join(wt, "package-lock.json"), "{}")

	r := Detect(wt, "")
	if r.Kind != RunnerBuiltin {
		t.Fatalf("expected RunnerBuiltin, got %v", r.Kind)
	}
	if r.Command != "npm test" {
		t.Fatalf("expected 'npm test', got %q", r.Command)
	}
}

func TestDetect_PackageJSONNoLockfile_DefaultsToNpm(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `{"scripts":{"test":"echo ok"}}`)

	r := Detect(wt, "")
	if r.Kind != RunnerBuiltin {
		t.Fatalf("expected RunnerBuiltin, got %v", r.Kind)
	}
	if r.Command != "npm test" {
		t.Fatalf("expected 'npm test', got %q", r.Command)
	}
}

func TestDetect_PackageJSONNoTestScript(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `{"scripts":{"build":"echo"}}`)

	r := Detect(wt, "")
	if r.Kind != RunnerNone {
		t.Fatalf("expected RunnerNone, got %v (cmd=%q)", r.Kind, r.Command)
	}
}

func TestDetect_NoHooksNoPackageJSON(t *testing.T) {
	wt := t.TempDir()
	r := Detect(wt, "")
	if r.Kind != RunnerNone {
		t.Fatalf("expected RunnerNone, got %v", r.Kind)
	}
}

func TestDetect_InvalidPackageJSON(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "package.json"), `not valid json`)
	r := Detect(wt, "")
	if r.Kind != RunnerNone {
		t.Fatalf("expected RunnerNone on invalid json, got %v", r.Kind)
	}
}

// ---------- Run ----------

func TestRun_NoneReturnsUnavailable(t *testing.T) {
	wt := t.TempDir()
	res := Run(wt, Runner{Kind: RunnerNone})
	if res.Available {
		t.Fatalf("expected Available=false, got %+v", res)
	}
}

func TestRun_HookSuccess(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	writeExec(t, hook, "#!/bin/sh\necho all good\nexit 0\n")

	res := Run(wt, Runner{Kind: RunnerHook, Command: hook})
	if !res.Available || !res.Ran {
		t.Fatalf("expected available+ran, got %+v", res)
	}
	if !res.Success {
		t.Fatalf("expected Success=true, got %+v", res)
	}
	if res.TestsFailed {
		t.Fatalf("expected TestsFailed=false, got %+v", res)
	}
	if !strings.Contains(res.Summary, "all good") {
		t.Fatalf("expected summary to contain 'all good', got %q", res.Summary)
	}
}

func TestRun_HookFailure(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	writeExec(t, hook, "#!/bin/sh\necho tests broke\nexit 1\n")

	res := Run(wt, Runner{Kind: RunnerHook, Command: hook})
	if !res.Available || !res.Ran {
		t.Fatalf("expected available+ran, got %+v", res)
	}
	if res.Success {
		t.Fatalf("expected Success=false, got %+v", res)
	}
	if !res.TestsFailed {
		t.Fatalf("expected TestsFailed=true, got %+v", res)
	}
	if !strings.Contains(res.Summary, "tests broke") {
		t.Fatalf("expected summary to contain 'tests broke', got %q", res.Summary)
	}
}

func TestRun_Builtin_FakeNpmOnPath(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()

	// Fake 'npm' that prints args and exits 0.
	binDir := t.TempDir()
	writeExec(t, filepath.Join(binDir, "npm"),
		"#!/bin/sh\necho fake-npm \"$@\"\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := Run(wt, Runner{Kind: RunnerBuiltin, Command: "npm test"})
	if !res.Available || !res.Ran {
		t.Fatalf("expected available+ran, got %+v", res)
	}
	if !res.Success {
		t.Fatalf("expected Success=true, got %+v", res)
	}
	if !strings.Contains(res.Summary, "fake-npm test") {
		t.Fatalf("expected summary to contain 'fake-npm test', got %q", res.Summary)
	}
}

func TestRun_OutputTruncatedAt2000(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	// Produce > 2000 chars of output.
	writeExec(t, hook, "#!/bin/sh\nawk 'BEGIN{for(i=0;i<3000;i++)printf \"x\"}'\nexit 0\n")

	res := Run(wt, Runner{Kind: RunnerHook, Command: hook})
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if !strings.HasSuffix(res.Summary, "...(truncated)") {
		t.Fatalf("expected truncation suffix, got tail=%q", tail(res.Summary, 40))
	}
	// 2000 chars + newline + "...(truncated)"
	const want = 2000 + 1 + len("...(truncated)")
	if len(res.Summary) != want {
		t.Fatalf("expected summary len %d, got %d", want, len(res.Summary))
	}
}

// TestRun_HookJSONSupersedesStdout exercises the readHookJSON seam: when the
// hook writes a valid JSON Result to $GH_CRFIX_VALIDATION_OUT, Run must return
// that decoded Result regardless of stdout.
func TestRun_HookJSONSupersedesStdout(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")

	payload := Result{
		Available:   true,
		Ran:         true,
		Success:     false,
		TestsFailed: true,
		Summary:     "from-json-file",
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Hook prints to stdout and writes JSON to GH_CRFIX_VALIDATION_OUT.
	// Exit 0 so we can tell the JSON's Success=false really came from JSON.
	script := "#!/bin/sh\n" +
		"echo stdout-should-be-ignored\n" +
		"mkdir -p \"$(dirname \"$GH_CRFIX_VALIDATION_OUT\")\"\n" +
		"cat > \"$GH_CRFIX_VALIDATION_OUT\" <<'EOF'\n" +
		string(jsonBytes) + "\n" +
		"EOF\n" +
		"exit 0\n"
	writeExec(t, hook, script)

	res := Run(wt, Runner{Kind: RunnerHook, Command: hook})
	if res.Summary != "from-json-file" {
		t.Fatalf("expected JSON summary, got %q (full=%+v)", res.Summary, res)
	}
	if res.Success != false || res.TestsFailed != true {
		t.Fatalf("expected JSON-provided Success/TestsFailed, got %+v", res)
	}
}

// TestRun_HookJSONInvalid_FallsBackToStdout: a corrupt JSON file must be
// ignored and the captured-stdout Result returned.
func TestRun_HookJSONInvalid_FallsBackToStdout(t *testing.T) {
	skipIfWindows(t)
	wt := t.TempDir()
	hook := filepath.Join(wt, ".gh-crfix", "validate.sh")
	script := "#!/bin/sh\n" +
		"echo stdout-wins\n" +
		"mkdir -p \"$(dirname \"$GH_CRFIX_VALIDATION_OUT\")\"\n" +
		"echo 'not json' > \"$GH_CRFIX_VALIDATION_OUT\"\n" +
		"exit 0\n"
	writeExec(t, hook, script)

	res := Run(wt, Runner{Kind: RunnerHook, Command: hook})
	if !strings.Contains(res.Summary, "stdout-wins") {
		t.Fatalf("expected stdout fallback, got %q", res.Summary)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
}

// ---------- helpers ----------

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
