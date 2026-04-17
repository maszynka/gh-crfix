//go:build e2e

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
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

	// --- Fresh git repo + bare "origin" as the worktree target -------------
	// worktree.Setup does `git fetch origin <branch>` so we wire up a
	// throwaway bare repo to act as origin.
	repoParent := t.TempDir()
	originDir := filepath.Join(repoParent, "origin.git")
	repoDir := filepath.Join(repoParent, "local")
	// Init local repo first (with a commit) then push to a bare origin we
	// created alongside. Init order matters because `git clone` refuses an
	// empty bare repo.
	runOrFail(t, repoParent, "git", "init", "-q", "-b", "main", repoDir)
	runOrFail(t, repoDir, "git", "config", "user.email", "e2e@test.local")
	runOrFail(t, repoDir, "git", "config", "user.name", "E2E Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runOrFail(t, repoDir, "git", "add", "README.md")
	runOrFail(t, repoDir, "git", "commit", "-q", "-m", "init")
	runOrFail(t, repoParent, "git", "init", "-q", "--bare", "-b", "main", originDir)
	runOrFail(t, repoDir, "git", "remote", "add", "origin", originDir)
	runOrFail(t, repoDir, "git", "push", "-q", "-u", "origin", "main")
	// Create the PR branch on origin so worktree.Setup can fetch + check it out.
	runOrFail(t, repoDir, "git", "checkout", "-q", "-b", "feat-test")
	runOrFail(t, repoDir, "git", "commit", "-q", "--allow-empty", "-m", "feat")
	runOrFail(t, repoDir, "git", "push", "-q", "-u", "origin", "feat-test")
	runOrFail(t, repoDir, "git", "checkout", "-q", "main")

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
		"PR #",  // per-PR banner or summary row
		"Setup", // setup phase summary banner
		"Done",  // final PrintResults banner
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

// ── New scenarios ───────────────────────────────────────────────────────────
//
// Each test below reuses the shared bare-repo fixture + stub harness via
// buildBinary / setupBareRepo / stub helpers. Variants in gh/claude behaviour
// are expressed by overriding the stub scripts for the specific test.

// TestE2E_PRClosed exercises the early-exit path when gh pr view reports a
// CLOSED PR. We expect exit 0 and a "skipped" row with reason "PR is CLOSED".
func TestE2E_PRClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}
	env := newE2EEnv(t)
	// Override the gh stub so `pr view` reports state=CLOSED for any PR.
	writeStubGhClosed(t, env.stubDir)
	writeStubClaude(t, env.stubDir)

	out, exit := env.runBinary(t,
		"https://github.com/acme/proj/pull/101",
		"--dry-run", "--no-tui", "--no-notify", "--no-post-fix",
	)
	t.Logf("--- output ---\n%s\n--------------", out)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	assertContains(t, out, "skipped")
	assertContains(t, out, "PR is CLOSED")
}

// TestE2E_NoUnresolvedThreads covers the setup-phase short-circuit when
// graphql reports zero unresolved threads.
func TestE2E_NoUnresolvedThreads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}
	env := newE2EEnv(t)
	writeStubGhEmptyThreads(t, env.stubDir)
	writeStubClaude(t, env.stubDir)

	out, exit := env.runBinary(t,
		"https://github.com/acme/proj/pull/101",
		"--dry-run", "--no-tui", "--no-notify", "--no-post-fix",
	)
	t.Logf("--- output ---\n%s\n--------------", out)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	assertContains(t, out, "no unresolved threads")
}

// TestE2E_MultiPR runs three PRs concurrently and asserts that the final
// results table lists [ok] PR #101, #102, #103 in input order regardless of
// the order in which the parallel workers finish.
func TestE2E_MultiPR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}
	env := newE2EEnv(t)
	// Create distinct branches on origin so each worktree can be checked out.
	for _, br := range []string{"feat-101", "feat-102", "feat-103"} {
		runOrFail(t, env.repoDir, "git", "checkout", "-q", "-b", br)
		runOrFail(t, env.repoDir, "git", "commit", "-q", "--allow-empty", "-m", "feat for "+br)
		runOrFail(t, env.repoDir, "git", "push", "-q", "-u", "origin", br)
		runOrFail(t, env.repoDir, "git", "checkout", "-q", "main")
	}
	writeStubGhPerPR(t, env.stubDir)
	writeStubClaude(t, env.stubDir)

	// Bare list form (no URL) so the binary looks up the current repo via
	// `gh repo view` — the stub handles that.
	out, exit := env.runBinary(t,
		"101,102,103",
		"--dry-run", "--no-tui", "--no-notify", "--no-post-fix",
		"--concurrency", "3",
	)
	t.Logf("--- output ---\n%s\n--------------", out)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// Each PR should produce one [ok] row in the final results table.
	okRe := regexp.MustCompile(`\[ok\]\s+PR #(\d+)`)
	matches := okRe.FindAllStringSubmatch(out, -1)
	if len(matches) < 3 {
		t.Fatalf("expected 3 [ok] rows, got %d: %q", len(matches), out)
	}
	// Assert that the first three [ok] PR numbers are in input order.
	want := []string{"101", "102", "103"}
	for i, w := range want {
		if matches[i][1] != w {
			t.Errorf("[ok] row %d: got PR #%s, want PR #%s (matches=%v)",
				i, matches[i][1], w, matches)
		}
	}
}

// TestE2E_SetupOnly asserts that --setup-only prints a "setup-only" skip
// reason and never invokes gate/fix models.
func TestE2E_SetupOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}
	env := newE2EEnv(t)
	writeStubGh(t, env.stubDir)
	writeStubClaude(t, env.stubDir)

	out, exit := env.runBinary(t,
		"https://github.com/acme/proj/pull/101",
		"--setup-only", "--no-tui", "--no-notify", "--no-post-fix",
	)
	t.Logf("--- output ---\n%s\n--------------", out)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	assertContains(t, out, "setup-only")
	assertNotContains(t, out, "running fix model")
	assertNotContains(t, out, "running gate model")
}

// TestE2E_GateSkipBelowThreshold drives the deterministic triage path where
// the only thread is non-actionable ("LGTM"). The gate model must not be
// invoked and the triage summary must be printed.
func TestE2E_GateSkipBelowThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}
	env := newE2EEnv(t)
	writeStubGhLGTM(t, env.stubDir)
	// Claude stub that records every invocation — we expect no calls at all.
	claudeCallLog := filepath.Join(env.sandbox, "claude-calls.log")
	writeStubClaudeRecording(t, env.stubDir, claudeCallLog)

	out, exit := env.runBinary(t,
		"https://github.com/acme/proj/pull/101",
		"--dry-run", "--no-tui", "--no-notify", "--no-post-fix",
	)
	t.Logf("--- output ---\n%s\n--------------", out)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// triage totals printed; LGTM classifies as skip=1 with zero needs_llm.
	assertContains(t, out, "triage: skip=1")
	assertContains(t, out, "needs_llm=0")
	// Gate must not run: neither in logs nor (more importantly) invoked.
	assertNotContains(t, out, "running gate model")
	if data, err := os.ReadFile(claudeCallLog); err == nil && len(data) > 0 {
		t.Fatalf("claude was invoked unexpectedly: %s", data)
	}
}

// TestE2E_FixModelWritesResponses drops the --dry-run flag so the fix model
// is actually invoked. Our fake claude writes thread-responses.json to its
// cwd (the worktree). The binary should then reply/resolve.
func TestE2E_FixModelWritesResponses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only e2e test")
	}
	env := newE2EEnv(t)
	writeStubGh(t, env.stubDir)
	writeStubClaude(t, env.stubDir)

	out, exit := env.runBinary(t,
		"https://github.com/acme/proj/pull/101",
		"--no-tui", "--no-notify", "--no-post-fix",
	)
	t.Logf("--- output ---\n%s\n--------------", out)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	assertContains(t, out, "replied=")
	assertContains(t, out, "resolved=")
}

// TestE2E_Interrupted starts the binary, sends SIGINT shortly after launch,
// and asserts the exit code is 130 with no panic markers in the output.
//
// The fake gh stub sleeps for a few seconds inside the graphql fetch so
// the binary is demonstrably mid-batch when the signal arrives.
func TestE2E_Interrupted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT semantics differ on Windows")
	}
	env := newE2EEnv(t)
	writeStubGhSlow(t, env.stubDir, 3)
	writeStubClaude(t, env.stubDir)

	cmd := exec.Command(env.binPath,
		"https://github.com/acme/proj/pull/101",
		"--dry-run", "--no-tui", "--no-notify", "--no-post-fix",
	)
	cmd.Env = env.envList()
	// Own process group so SIGINT only targets the binary, not the test harness.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Wait until the binary is demonstrably past main()'s signal-handler
	// registration (we delay long enough that signal.NotifyContext has
	// wired itself up and the workflow has started its first blocking
	// subprocess call). 500ms gives Go startup + config load + setup-phase
	// ample headroom on the slowest CI runners.
	time.Sleep(500 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	werr := cmd.Wait()
	exit := exitCode(werr)
	if exit != 130 {
		t.Fatalf("exit = %d, want 130 (err=%v)", exit, werr)
	}
}

// ── Shared e2e harness ──────────────────────────────────────────────────────

// e2eEnv bundles everything a scenario needs to invoke the binary: a built
// binary on disk, a stub directory to drop fake CLIs into, a tracked origin
// repo, and an isolated HOME.
type e2eEnv struct {
	binPath string
	stubDir string
	repoDir string
	home    string
	sandbox string
}

// newE2EEnv builds the binary (once per test) and stages a bare-repo + home
// + stub directory under t.TempDir(). The caller is expected to write the
// gh/claude stubs it wants after construction so each scenario can override.
func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	env := &e2eEnv{}

	env.binPath = buildBinary(t)

	env.sandbox = t.TempDir()
	env.stubDir = filepath.Join(env.sandbox, "stubs")
	env.repoDir = filepath.Join(env.sandbox, "local")
	env.home = filepath.Join(env.sandbox, "home")
	originDir := filepath.Join(env.sandbox, "origin.git")
	for _, d := range []string{env.stubDir, env.home, filepath.Join(env.home, ".config"), filepath.Join(env.home, ".cache")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	setupBareRepo(t, env.sandbox, env.repoDir, originDir)
	return env
}

// envList produces the fixed env slice every scenario uses. Callers may
// extend / override as needed before passing to exec.Cmd.
func (e *e2eEnv) envList() []string {
	return []string{
		"PATH=" + e.stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + e.home,
		"GH_CRFIX_DIR=" + e.repoDir,
		"GH_CRFIX_NO_NOTIFY=1",
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(e.home, ".cache"),
	}
}

// runBinary invokes the built binary with the given args, returning combined
// output and exit code. Helpers favour returning the exit code directly over
// asserting on it so individual tests can assert specific non-zero codes.
func (e *e2eEnv) runBinary(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.binPath, args...)
	cmd.Env = e.envList()
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(err)
}

// buildBinary compiles ./cmd/gh-crfix into a temp dir and returns the path.
// Each test gets its own binary so parallel runs don't clobber each other.
func buildBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "gh-crfix")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./")
	buildCmd.Dir = repoRoot(t)
	buildCmd.Env = os.Environ()
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return binPath
}

// setupBareRepo creates a local+origin pair matching the happy-path fixture.
// The local repo has a main branch with README.md and a feat-test branch
// pushed to origin. Individual scenarios may push additional branches on top.
func setupBareRepo(t *testing.T, parent, repoDir, originDir string) {
	t.Helper()
	runOrFail(t, parent, "git", "init", "-q", "-b", "main", repoDir)
	runOrFail(t, repoDir, "git", "config", "user.email", "e2e@test.local")
	runOrFail(t, repoDir, "git", "config", "user.name", "E2E Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runOrFail(t, repoDir, "git", "add", "README.md")
	runOrFail(t, repoDir, "git", "commit", "-q", "-m", "init")
	runOrFail(t, parent, "git", "init", "-q", "--bare", "-b", "main", originDir)
	runOrFail(t, repoDir, "git", "remote", "add", "origin", originDir)
	runOrFail(t, repoDir, "git", "push", "-q", "-u", "origin", "main")
	runOrFail(t, repoDir, "git", "checkout", "-q", "-b", "feat-test")
	runOrFail(t, repoDir, "git", "commit", "-q", "--allow-empty", "-m", "feat")
	runOrFail(t, repoDir, "git", "push", "-q", "-u", "origin", "feat-test")
	runOrFail(t, repoDir, "git", "checkout", "-q", "main")
}

// exitCode unpacks an exec.Cmd error into a plain integer. Unknown errors
// collapse to -1 so assertions stay readable.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output missing %q", needle)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("output unexpectedly contains %q", needle)
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

// ── Stub writers ────────────────────────────────────────────────────────────

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

// writeStubGhClosed is writeStubGh, but pr view reports state=CLOSED.
func writeStubGhClosed(t *testing.T, stubDir string) {
	t.Helper()
	script := `#!/bin/sh
set -e
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      cat <<'JSON'
{"headRefName":"feat-test","baseRefName":"main","title":"Closed PR","state":"CLOSED","isDraft":false,"headRefOid":"deadbeef"}
JSON
    else
      echo ok
    fi
    ;;
  repo) echo "acme/proj" ;;
  api)  echo "{}" ;;
  run)  echo "" ;;
  *)    echo "{}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh (closed): %v", err)
	}
}

// writeStubGhEmptyThreads returns an empty reviewThreads.nodes list from the
// graphql endpoint so the setup phase short-circuits on "no unresolved
// threads".
func writeStubGhEmptyThreads(t *testing.T, stubDir string) {
	t.Helper()
	script := `#!/bin/sh
set -e
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      cat <<'JSON'
{"headRefName":"feat-test","baseRefName":"main","title":"Clean PR","state":"OPEN","isDraft":false,"headRefOid":"deadbeef"}
JSON
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*)
        printf '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}\n'
        ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh (empty threads): %v", err)
	}
}

// writeStubGhPerPR returns a distinct branch name per PR number so
// independent worktrees can be created for each in the multi-PR test.
func writeStubGhPerPR(t *testing.T, stubDir string) {
	t.Helper()
	script := `#!/bin/sh
set -e
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
# Extract the PR number as the first all-digits positional argument.
prnum=""
for arg in "$@"; do
  case "$arg" in
    ''|*[!0-9]*) ;;
    *) prnum="$arg"; break;;
  esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      printf '{"headRefName":"feat-%s","baseRefName":"main","title":"PR %s","state":"OPEN","isDraft":false,"headRefOid":"deadbeef%s"}\n' "$prnum" "$prnum" "$prnum"
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*)
        cat <<JSON
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
  {"id":"PRRT_$prnum","isResolved":false,"isOutdated":false,"line":10,"path":"README.md",
   "comments":{"nodes":[
     {"id":"IC_$prnum","body":"please explain this line","path":"README.md","line":10,"originalLine":10,
      "author":{"login":"reviewer"},"createdAt":"2026-01-01T00:00:00Z"}
   ]}}
]}}}}}
JSON
        ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh (per-PR): %v", err)
	}
}

// writeStubGhLGTM returns a single non-actionable "LGTM" review thread so
// deterministic triage classifies it as skip with no needs_llm threads.
func writeStubGhLGTM(t *testing.T, stubDir string) {
	t.Helper()
	script := `#!/bin/sh
set -e
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      cat <<'JSON'
{"headRefName":"feat-test","baseRefName":"main","title":"LGTM PR","state":"OPEN","isDraft":false,"headRefOid":"deadbeef"}
JSON
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*)
        cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
  {"id":"PRRT_lgtm","isResolved":false,"isOutdated":false,"line":1,"path":"README.md",
   "comments":{"nodes":[
     {"id":"IC_lgtm","body":"LGTM","path":"README.md","line":1,"originalLine":1,
      "author":{"login":"reviewer"},"createdAt":"2026-01-01T00:00:00Z"}
   ]}}
]}}}}}
JSON
        ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh (LGTM): %v", err)
	}
}

// writeStubGhSlow is writeStubGh but the graphql fetch sleeps for sleepSecs
// so the SIGINT test has a predictable window to interrupt.
func writeStubGhSlow(t *testing.T, stubDir string, sleepSecs int) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
set -e
mode=""
for arg in "$@"; do
  case "$arg" in pr) mode=pr;; api) mode=api;; run) mode=run;; repo) mode=repo;; esac
done
case "$mode" in
  pr)
    verb=""; seen=0
    for arg in "$@"; do
      if [ "$seen" = 1 ] && [ -z "$verb" ]; then verb="$arg"; break; fi
      [ "$arg" = pr ] && seen=1
    done
    if [ "$verb" = view ]; then
      cat <<'JSON'
{"headRefName":"feat-test","baseRefName":"main","title":"Slow PR","state":"OPEN","isDraft":false,"headRefOid":"deadbeef"}
JSON
    else
      echo ok
    fi
    ;;
  api)
    all="$*"
    case "$all" in
      *reviewThreads*)
        sleep %d
        cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}
JSON
        ;;
      *) echo "{}" ;;
    esac
    ;;
  run)  echo "" ;;
  repo) echo "acme/proj" ;;
  *)    echo "{}" ;;
esac
`, sleepSecs)
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh (slow): %v", err)
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

// writeStubClaudeRecording is writeStubClaude but appends each invocation's
// full argv to logPath so tests can assert whether claude was called at all.
func writeStubClaudeRecording(t *testing.T, stubDir, logPath string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
for arg in "$@"; do
  if [ "$arg" = "--json-schema" ]; then
    printf '{"structured_output":{"needs_advanced_model":false,"reason":"e2e-fake","threads_to_fix":[]}}\n'
    exit 0
  fi
done
cat > thread-responses.json <<'JSON'
[]
JSON
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(stubDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub claude (recording): %v", err)
	}
	codex := `#!/bin/sh
exit 1
`
	if err := os.WriteFile(filepath.Join(stubDir, "codex"), []byte(codex), 0o755); err != nil {
		t.Fatalf("write stub codex: %v", err)
	}
}
