//go:build e2e

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestE2E_DryRunHappyPath builds the binary, stubs gh + claude on PATH, and
// invokes gh-crfix against a fake PR. The test is the primary integration
// harness for the Go port — it exercises:
//   - flag parsing (incl. --no-tui, --no-notify, --dry-run)
//   - config load & defaults
//   - logs.Run + progress.Tracker wiring via ProcessBatch
//   - the setup phase (fetching PR metadata, worktree, fetching threads)
//   - the process phase up through gate/fix in dry-run mode
//   - the final PrintResults summary
//
// We use --dry-run so no GitHub writes happen and the AI "fix" step is
// suppressed (meaning the fake claude is only invoked for the gate; if the
// wiring is wrong, the test will fail with a non-zero exit or missing output).
func TestE2E_DryRunHappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}

	// --- Build the gh-crfix binary -------------------------------------------
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "gh-crfix")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./")
	buildCmd.Dir = filepath.Join(repoRoot(t))
	buildCmd.Env = os.Environ()
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// --- Stub gh + claude on PATH --------------------------------------------
	stubDir := t.TempDir()
	writeStubGh(t, stubDir)
	writeStubClaude(t, stubDir)

	// --- Fresh git repo as the worktree target ------------------------------
	repoDir := t.TempDir()
	runOrFail(t, repoDir, "git", "init", "-q", "-b", "main")
	runOrFail(t, repoDir, "git", "config", "user.email", "e2e@test.local")
	runOrFail(t, repoDir, "git", "config", "user.name", "E2E Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runOrFail(t, repoDir, "git", "add", "README.md")
	runOrFail(t, repoDir, "git", "commit", "-q", "-m", "init")
	// Create the PR branch so worktree.Setup can check it out.
	runOrFail(t, repoDir, "git", "branch", "feat-test")

	// --- Isolated home for config / notifications ----------------------------
	home := t.TempDir()

	// --- Invoke the binary ---------------------------------------------------
	cmd := exec.Command(binPath,
		"https://github.com/acme/proj/pull/101",
		"--dry-run",
		"--no-tui",
		"--no-notify",
		"--no-post-fix",
	)
	cmd.Env = append([]string{},
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		"GH_CRFIX_DIR="+repoDir,
		"GH_CRFIX_NO_NOTIFY=1",
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
	)
	out, err := cmd.CombinedOutput()
	// Always surface the output so failures are diagnosable.
	t.Logf("--- gh-crfix output ---\n%s\n-----------------------", out)
	if err != nil {
		t.Fatalf("gh-crfix exit err: %v", err)
	}

	s := string(out)
	for _, want := range []string{
		"PR #",    // per-PR banner or summary row
		"Setup",   // setup phase summary banner
		"Done",    // final PrintResults banner
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// Belt-and-braces sanity: no nil panic markers.
	for _, bad := range []string{"panic:", "runtime error:", "nil pointer"} {
		if strings.Contains(s, bad) {
			t.Errorf("output contains unexpected %q", bad)
		}
	}
}

// repoRoot walks up from this test file's directory to find the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// We are in cmd/gh-crfix when running go test; the root is two levels up.
	return wd
}

func runOrFail(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// writeStubGh drops a POSIX shell script on $stubDir that impersonates `gh`
// well enough to exercise the full pipeline in dry-run mode. The script
// branches on the subcommand and returns fixed JSON the real gh CLI would
// produce.
func writeStubGh(t *testing.T, stubDir string) {
	t.Helper()
	script := `#!/bin/sh
# Fake gh CLI for gh-crfix e2e.
set -e

# Detect subcommands by scanning all args (gh uses positional subcommands).
mode=""
for arg in "$@"; do
  case "$arg" in
    pr)         mode="pr" ;;
    api)        mode="api" ;;
    run)        mode="run" ;;
    repo)       mode="repo" ;;
  esac
done

case "$mode" in
  pr)
    # pr view -> metadata JSON; pr comment -> success.
    # Discover the verb: first arg after "pr".
    verb=""
    seen_pr=0
    for arg in "$@"; do
      if [ "$seen_pr" = "1" ] && [ -z "$verb" ]; then
        verb="$arg"
        break
      fi
      if [ "$arg" = "pr" ]; then seen_pr=1; fi
    done
    case "$verb" in
      view)
        cat <<JSON
{"headRefName":"feat-test","baseRefName":"main","title":"E2E test PR","state":"OPEN","isDraft":false,"headRefOid":"deadbeefcafebabe"}
JSON
        ;;
      comment)
        echo "ok"
        ;;
      *)
        echo "ok"
        ;;
    esac
    ;;
  api)
    # Distinguish the different api calls by inspecting all args.
    all="$*"
    case "$all" in
      *graphql*reviewThreads*|*reviewThreads*)
        cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
  {"id":"PRRT_test","isResolved":false,"isOutdated":false,"line":10,"path":"README.md",
   "comments":{"nodes":[
     {"id":"IC_1","body":"please explain this line","path":"README.md","line":10,"originalLine":10,
      "author":{"login":"reviewer"},"createdAt":"2026-01-01T00:00:00Z"}
   ]}}
]}}}}}
JSON
        ;;
      *requested_reviewers*)
        echo "{}"
        ;;
      *check-runs*|*checks*|*statuses*|*commits*|*runs*)
        echo "{}"
        ;;
      *)
        echo "{}"
        ;;
    esac
    ;;
  run)
    # gh run view --log-failed -> empty string (no failing CI)
    echo ""
    ;;
  repo)
    echo "acme/proj"
    ;;
  *)
    # Unknown invocation: non-fatal, just emit empty JSON so callers see
    # parse errors rather than exit status failures.
    echo "{}"
    ;;
esac
`
	path := filepath.Join(stubDir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
}

// writeStubClaude drops a POSIX shell script on $stubDir that impersonates the
// claude CLI for both gate (-p --json-schema) and fix (plain -p) invocations.
func writeStubClaude(t *testing.T, stubDir string) {
	t.Helper()
	script := `#!/bin/sh
# Fake claude CLI for gh-crfix e2e.
# Gate path: arguments include --json-schema; emit a valid gate JSON.
# Fix path: no --json-schema; write thread-responses.json and exit 0.
set -e
for arg in "$@"; do
  if [ "$arg" = "--json-schema" ]; then
    printf '{"structured_output":{"needs_advanced_model":true,"reason":"e2e-fake","threads_to_fix":["PRRT_test"]}}\n'
    exit 0
  fi
done
# Fix path: the caller cwd is the worktree; drop a thread-responses.json.
cat > thread-responses.json <<'JSON'
[
  {"thread_id":"PRRT_test","action":"fixed","comment":"e2e fake fix"}
]
JSON
exit 0
`
	path := filepath.Join(stubDir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub claude: %v", err)
	}
	// Provide an empty codex stub too, so ai.Detect doesn't accidentally pick
	// something unexpected.
	codex := `#!/bin/sh
exit 1
`
	cp := filepath.Join(stubDir, "codex")
	if err := os.WriteFile(cp, []byte(codex), 0o755); err != nil {
		t.Fatalf("write stub codex: %v", err)
	}
}

// Silence unused-import complaints across Go versions without locking us into
// one stdlib.
var _ = fmt.Sprintf
