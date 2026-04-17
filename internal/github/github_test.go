package github

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

// isolatePATH prepends a fresh temp dir to $PATH so fake scripts shadow any
// real gh on the developer's machine, while still exposing standard shell
// utilities (cat, printf, echo) needed by the fake scripts themselves.
// Returns the temp dir (where the fake `gh` and helper files should live).
func isolatePATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return dir
}

// ---------- FetchPR ----------

func TestFetchPR_Happy(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
cat <<'JSON'
{"headRefName":"feat/x","baseRefName":"main","title":"add thing","state":"OPEN","isDraft":false,"headRefOid":"deadbeef"}
JSON
`)
	info, err := FetchPR(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("FetchPR: %v", err)
	}
	if info.HeadRefName != "feat/x" || info.BaseRefName != "main" ||
		info.Title != "add thing" || info.State != "OPEN" ||
		info.IsDraft != false || info.HeadSHA != "deadbeef" {
		t.Fatalf("unexpected PRInfo: %+v", info)
	}
}

func TestFetchPR_MissingFields(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
printf '{}'
`)
	info, err := FetchPR(context.Background(), "owner/repo", 1)
	if err != nil {
		t.Fatalf("FetchPR: %v", err)
	}
	if info.HeadRefName != "" || info.Title != "" || info.HeadSHA != "" || info.IsDraft {
		t.Fatalf("expected zero-valued PRInfo, got: %+v", info)
	}
}

func TestFetchPR_MalformedJSON(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
printf 'not-json'
`)
	_, err := FetchPR(context.Background(), "owner/repo", 1)
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse pr") {
		t.Fatalf("error should mention parse pr, got: %v", err)
	}
}

func TestFetchPR_NonZeroExit(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
echo "gh boom" 1>&2
exit 1
`)
	_, err := FetchPR(context.Background(), "owner/repo", 1)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "gh boom") {
		t.Fatalf("error should contain stderr, got: %v", err)
	}
}

// ---------- FetchThreads ----------

func TestFetchThreads_Mixed(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
  {"id":"T1","isResolved":true,"isOutdated":false,"line":10,"path":"a.go",
    "comments":{"nodes":[{"id":"C1","body":"done","path":"a.go","line":10,"originalLine":9,"author":{"login":"alice"},"createdAt":"2024-01-01"}]}},
  {"id":"T2","isResolved":false,"isOutdated":true,"line":20,"path":"b.go",
    "comments":{"nodes":[
      {"id":"C2","body":"fix this","path":"b.go","line":20,"originalLine":18,"author":{"login":"bob"},"createdAt":"2024-01-02"},
      {"id":"C3","body":"please","path":"b.go","line":21,"originalLine":19,"author":{"login":"carol"},"createdAt":"2024-01-03"}
    ]}}
]}}}}}
JSON
`)
	threads, err := FetchThreads(context.Background(), "owner/repo", 7, 50)
	if err != nil {
		t.Fatalf("FetchThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 unresolved thread, got %d", len(threads))
	}
	th := threads[0]
	if th.ID != "T2" || th.IsResolved || !th.IsOutdated || th.Path != "b.go" || th.Line != 20 {
		t.Fatalf("unexpected thread: %+v", th)
	}
	if len(th.Comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(th.Comments))
	}
	c0 := th.Comments[0]
	if c0.ID != "C2" || c0.Body != "fix this" || c0.Path != "b.go" ||
		c0.Line != 20 || c0.OriginalLine != 18 ||
		c0.Author != "bob" || c0.CreatedAt != "2024-01-02" {
		t.Fatalf("unexpected comment[0]: %+v", c0)
	}
	if th.Comments[1].Author != "carol" {
		t.Fatalf("unexpected comment[1]: %+v", th.Comments[1])
	}
}

func TestFetchThreads_EmptyNodes(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}
JSON
`)
	threads, err := FetchThreads(context.Background(), "owner/repo", 1, 10)
	if err != nil {
		t.Fatalf("FetchThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("expected empty slice, got %d", len(threads))
	}
}

func TestFetchThreads_InvalidRepo(t *testing.T) {
	// No gh invocation expected — parsing fails first.
	isolatePATH(t)
	_, err := FetchThreads(context.Background(), "badrepo", 1, 10)
	if err == nil {
		t.Fatal("expected error for invalid repo")
	}
	if !strings.Contains(err.Error(), "invalid repo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------- ReplyToThread / ResolveThread / PostComment / RequestCopilotReview ----------

// ghRecorderScript writes all argv (one per line) to $ARGFILE and exits 0.
// The caller must set $ARGFILE in the script's env via a setenv'd env var or
// by baking the path directly into the script body.
func ghRecorderScript(argFile string) string {
	return `#!/bin/sh
: > "` + argFile + `"
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argFile + `"
done
exit 0
`
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argfile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func containsAll(args []string, subs ...string) bool {
	for _, s := range subs {
		found := false
		for _, a := range args {
			if a == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestReplyToThread_Argv(t *testing.T) {
	dir := isolatePATH(t)
	argFile := filepath.Join(dir, "args.txt")
	writeScript(t, dir, "gh", ghRecorderScript(argFile))

	if err := ReplyToThread(context.Background(), "THREAD123", "Looks good"); err != nil {
		t.Fatalf("ReplyToThread: %v", err)
	}
	args := readArgs(t, argFile)
	if !containsAll(args, "api", "graphql") {
		t.Fatalf("expected api graphql, got: %v", args)
	}
	if !containsAll(args, "threadId=THREAD123", "body=Looks good") {
		t.Fatalf("expected threadId/body args, got: %v", args)
	}
}

func TestResolveThread_Argv(t *testing.T) {
	dir := isolatePATH(t)
	argFile := filepath.Join(dir, "args.txt")
	writeScript(t, dir, "gh", ghRecorderScript(argFile))

	if err := ResolveThread(context.Background(), "THREAD999"); err != nil {
		t.Fatalf("ResolveThread: %v", err)
	}
	args := readArgs(t, argFile)
	if !containsAll(args, "api", "graphql", "threadId=THREAD999") {
		t.Fatalf("unexpected argv: %v", args)
	}
}

func TestPostComment_Argv(t *testing.T) {
	dir := isolatePATH(t)
	argFile := filepath.Join(dir, "args.txt")
	writeScript(t, dir, "gh", ghRecorderScript(argFile))

	if err := PostComment(context.Background(), "owner/repo", 123, "foo"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	args := readArgs(t, argFile)
	// Expect: pr comment 123 --repo owner/repo --body foo
	if !containsAll(args, "pr", "comment", "123", "--repo", "owner/repo", "--body", "foo") {
		t.Fatalf("unexpected argv: %v", args)
	}
}

func TestRequestCopilotReview_Argv(t *testing.T) {
	dir := isolatePATH(t)
	argFile := filepath.Join(dir, "args.txt")
	writeScript(t, dir, "gh", ghRecorderScript(argFile))

	if err := RequestCopilotReview(context.Background(), "owner/repo", 55); err != nil {
		t.Fatalf("RequestCopilotReview: %v", err)
	}
	args := readArgs(t, argFile)
	if !containsAll(args, "api", "--method", "POST",
		"/repos/owner/repo/pulls/55/requested_reviewers",
		"reviewers[]=Copilot") {
		t.Fatalf("unexpected argv: %v", args)
	}
}

// ---------- FetchFailingChecks ----------

func TestFetchFailingChecks_ParsesAndFetchesLogs(t *testing.T) {
	dir := isolatePATH(t)
	// gh handles two subcommands: `api ...` returns NDJSON, `run view N ...`
	// returns many log lines so we can verify the 80-line truncation.
	script := `#!/bin/sh
case "$1" in
  api)
    cat <<'JSON'
{"name":"build","details_url":"https://github.com/owner/repo/actions/runs/111/jobs/222"}
{"name":"test","details_url":"https://github.com/owner/repo/actions/runs/333/jobs/444"}
JSON
    ;;
  run)
    # Emit 120 lines so we can verify truncation to 80.
    i=1
    while [ $i -le 120 ]; do
      printf 'log-line-%d\n' "$i"
      i=$((i+1))
    done
    ;;
  *)
    echo "unexpected: $*" 1>&2
    exit 2
    ;;
esac
exit 0
`
	writeScript(t, dir, "gh", script)

	checks, err := FetchFailingChecks(context.Background(), "owner/repo", "deadbeef")
	if err != nil {
		t.Fatalf("FetchFailingChecks: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks, got %d: %+v", len(checks), checks)
	}
	if checks[0].Name != "build" || checks[1].Name != "test" {
		t.Fatalf("unexpected names: %+v", checks)
	}
	// LogText should contain early lines and be truncated at 80 lines.
	if !strings.Contains(checks[0].LogText, "log-line-1\n") {
		t.Fatalf("expected log-line-1 in log, got: %q", checks[0].LogText)
	}
	// Count lines: the fake emits up to 120; strings.Split includes a trailing
	// empty string because the printf output ends with "\n". Impl caps at 80.
	lines := strings.Split(checks[0].LogText, "\n")
	if len(lines) != 80 {
		t.Fatalf("expected 80 lines after truncation, got %d", len(lines))
	}
	// The 80th line (index 79) should be log-line-80 (not 81+).
	if !strings.Contains(checks[0].LogText, "log-line-80") {
		t.Fatalf("expected log-line-80 in truncated log")
	}
	if strings.Contains(checks[0].LogText, "log-line-81") {
		t.Fatalf("log should have been truncated before log-line-81")
	}
}

func TestFetchFailingChecks_APIFailureNonFatal(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
echo "api error" 1>&2
exit 1
`)
	checks, err := FetchFailingChecks(context.Background(), "owner/repo", "deadbeef")
	if err != nil {
		t.Fatalf("expected nil error on api failure (non-fatal), got: %v", err)
	}
	if checks != nil {
		t.Fatalf("expected nil checks on api failure, got: %+v", checks)
	}
}

func TestFetchFailingChecks_EmptyOutput(t *testing.T) {
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
# no output, exit success
exit 0
`)
	checks, err := FetchFailingChecks(context.Background(), "owner/repo", "deadbeef")
	if err != nil {
		t.Fatalf("FetchFailingChecks: %v", err)
	}
	if len(checks) != 0 {
		t.Fatalf("expected 0 checks, got %d", len(checks))
	}
}

func TestFetchFailingChecks_DetailsURLWithoutRunID(t *testing.T) {
	// If details_url has no /runs/N segment, fetchRunLog returns "" and we
	// still record the check.
	dir := isolatePATH(t)
	writeScript(t, dir, "gh", `#!/bin/sh
case "$1" in
  api)
    printf '{"name":"lint","details_url":"https://example.com/whatever"}\n'
    ;;
  run)
    # Should not be invoked because there is no run id.
    echo "log" ; exit 0 ;;
esac
exit 0
`)
	checks, err := FetchFailingChecks(context.Background(), "owner/repo", "deadbeef")
	if err != nil {
		t.Fatalf("FetchFailingChecks: %v", err)
	}
	if len(checks) != 1 || checks[0].Name != "lint" {
		t.Fatalf("unexpected checks: %+v", checks)
	}
	if checks[0].LogText != "" {
		t.Fatalf("expected empty LogText when no run id, got: %q", checks[0].LogText)
	}
}
