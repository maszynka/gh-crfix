package ai

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeScript writes a shell script to dir/name with executable perms.
// Skips on windows because we rely on shebangs.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell scripts require POSIX shebang support")
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

// exclusivePATH points $PATH at a single directory so only fake scripts are
// visible. Useful for Detect() which only needs exec.LookPath — no shell
// built-ins are invoked.
func exclusivePATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir
}

// isolatePATH prepends a fresh temp dir to $PATH so fake scripts shadow any
// real claude/codex on the developer's machine, while still exposing standard
// shell utilities (cat, printf, echo) needed by the fake scripts themselves.
// It returns the temp dir (where fake scripts should be written).
func isolatePATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Preserve a minimal standard PATH for shell built-ins invoked by scripts.
	// /usr/bin and /bin exist on both linux and macOS.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return dir
}

func TestParseBackend(t *testing.T) {
	cases := []struct {
		in   string
		want Backend
	}{
		{"claude", BackendClaude},
		{"anthropic", BackendClaude},
		{"Claude", BackendClaude},
		{"CLAUDE", BackendClaude},
		{"codex", BackendCodex},
		{"openai", BackendCodex},
		{"auto", BackendAuto},
		{"", BackendAuto},
		{"unknown", BackendAuto},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseBackend(tc.in)
			if got != tc.want {
				t.Fatalf("ParseBackend(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDetect_ClaudeWins(t *testing.T) {
	dir := exclusivePATH(t)
	writeScript(t, dir, "claude", "#!/bin/sh\nexit 0\n")
	writeScript(t, dir, "codex", "#!/bin/sh\nexit 0\n")
	if got := Detect(); got != BackendClaude {
		t.Fatalf("Detect() = %v, want BackendClaude", got)
	}
}

func TestDetect_CodexOnly(t *testing.T) {
	dir := exclusivePATH(t)
	writeScript(t, dir, "codex", "#!/bin/sh\nexit 0\n")
	if got := Detect(); got != BackendCodex {
		t.Fatalf("Detect() = %v, want BackendCodex", got)
	}
}

func TestDetect_Neither(t *testing.T) {
	exclusivePATH(t)
	if got := Detect(); got != BackendAuto {
		t.Fatalf("Detect() = %v, want BackendAuto", got)
	}
}

func TestResolveBackend(t *testing.T) {
	// BackendAuto dispatches to Detect.
	dir := exclusivePATH(t)
	writeScript(t, dir, "codex", "#!/bin/sh\nexit 0\n")
	if got := resolveBackend(BackendAuto); got != BackendCodex {
		t.Fatalf("resolveBackend(Auto) = %v, want BackendCodex", got)
	}
	// Explicit backends pass through unchanged (no PATH lookup).
	if got := resolveBackend(BackendClaude); got != BackendClaude {
		t.Fatalf("resolveBackend(Claude) = %v, want BackendClaude", got)
	}
	if got := resolveBackend(BackendCodex); got != BackendCodex {
		t.Fatalf("resolveBackend(Codex) = %v, want BackendCodex", got)
	}
}

func TestRunGate_Claude_Happy(t *testing.T) {
	dir := isolatePATH(t)
	// Emit a direct GateOutput on stdout.
	writeScript(t, dir, "claude", `#!/bin/sh
cat <<'JSON'
{"needs_advanced_model": true, "reason": "complex refactor", "threads_to_fix": ["T1","T2"]}
JSON
`)
	out, err := RunGate(context.Background(), BackendClaude, "sonnet", "prompt body", map[string]interface{}{"type": "object"})
	if err != nil {
		t.Fatalf("RunGate: %v", err)
	}
	if !out.NeedsAdvancedModel || out.Reason != "complex refactor" ||
		len(out.ThreadsToFix) != 2 || out.ThreadsToFix[0] != "T1" {
		t.Fatalf("unexpected GateOutput: %+v", out)
	}
}

func TestRunGate_Claude_Wrapped(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
cat <<'JSON'
{"structured_output": {"needs_advanced_model": false, "reason": "simple", "threads_to_fix": []}}
JSON
`)
	out, err := RunGate(context.Background(), BackendClaude, "sonnet", "p", map[string]interface{}{})
	if err != nil {
		t.Fatalf("RunGate: %v", err)
	}
	if out.NeedsAdvancedModel || out.Reason != "simple" || len(out.ThreadsToFix) != 0 {
		t.Fatalf("unexpected GateOutput: %+v", out)
	}
}

func TestRunGate_Claude_Malformed(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
printf 'not valid json at all'
`)
	_, err := RunGate(context.Background(), BackendClaude, "sonnet", "p", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "not valid json at all") {
		t.Fatalf("error should include raw output, got: %v", err)
	}
}

func TestRunGate_Claude_NonZeroExit(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
echo "boom on stderr" 1>&2
exit 3
`)
	_, err := RunGate(context.Background(), BackendClaude, "sonnet", "p", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom on stderr") {
		t.Fatalf("error should include stderr, got: %v", err)
	}
}

func TestRunGate_Codex_WritesOutputFile(t *testing.T) {
	dir := isolatePATH(t)
	// codex exec receives --output-last-message <PATH> among its args.
	// Walk argv, find the flag, and write the JSON to that file.
	writeScript(t, dir, "codex", `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [ -n "$out" ]; then
  cat > "$out" <<'JSON'
{"needs_advanced_model": true, "reason": "codex path", "threads_to_fix": ["X"]}
JSON
fi
exit 0
`)
	out, err := RunGate(context.Background(), BackendCodex, "gpt", "p", map[string]interface{}{})
	if err != nil {
		t.Fatalf("RunGate codex: %v", err)
	}
	if !out.NeedsAdvancedModel || out.Reason != "codex path" ||
		len(out.ThreadsToFix) != 1 || out.ThreadsToFix[0] != "X" {
		t.Fatalf("unexpected GateOutput: %+v", out)
	}
}

func TestRunGate_NoBackendAvailable(t *testing.T) {
	// Exclusive PATH with no claude/codex and use Auto -> Detect returns Auto -> error.
	exclusivePATH(t)
	_, err := RunGate(context.Background(), BackendAuto, "m", "p", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected no-backend error")
	}
	if !strings.Contains(err.Error(), "no AI backend available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunFix_Claude_Happy(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
exit 0
`)
	if err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", t.TempDir()); err != nil {
		t.Fatalf("RunFix: %v", err)
	}
}

func TestRunFix_Claude_NonZeroExit(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
exit 2
`)
	if err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", t.TempDir()); err == nil {
		t.Fatal("expected non-nil error on non-zero exit")
	}
}

func TestRunFix_Claude_HonorsDir(t *testing.T) {
	scriptDir := isolatePATH(t)
	// The fake claude writes a marker file into its current working directory.
	writeScript(t, scriptDir, "claude", `#!/bin/sh
echo "hello" > marker.txt
exit 0
`)
	workDir := t.TempDir()
	if err := RunFix(context.Background(), BackendClaude, "sonnet", "prompt", workDir); err != nil {
		t.Fatalf("RunFix: %v", err)
	}
	p := filepath.Join(workDir, "marker.txt")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("expected marker file at %s: %v", p, err)
	}
	if strings.TrimSpace(string(data)) != "hello" {
		t.Fatalf("unexpected marker content: %q", data)
	}
}

func TestRunFix_Codex_Happy(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "codex", `#!/bin/sh
exit 0
`)
	if err := RunFix(context.Background(), BackendCodex, "gpt", "p", t.TempDir()); err != nil {
		t.Fatalf("RunFix codex: %v", err)
	}
}

func TestRunFix_NoBackendAvailable(t *testing.T) {
	exclusivePATH(t)
	err := RunFix(context.Background(), BackendAuto, "m", "p", t.TempDir())
	if err == nil {
		t.Fatal("expected no-backend error")
	}
	if !strings.Contains(err.Error(), "no AI backend available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunPlain_DelegatesToRunFix(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "claude", `#!/bin/sh
exit 0
`)
	if err := RunPlain(context.Background(), BackendClaude, "m", "p", t.TempDir()); err != nil {
		t.Fatalf("RunPlain: %v", err)
	}
}
