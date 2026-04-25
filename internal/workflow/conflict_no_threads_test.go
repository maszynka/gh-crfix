package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	ghapi "github.com/maszynka/gh-crfix/internal/github"
)

// --- Comment 1 (gemini): DetectMarkers via worktreeSetup interface, errors visible

// TestSetupOnePR_DetectMarkersErrorTreatedAsNoMarkers asserts that a hard
// error from DetectMarkers (e.g. git lookup failure) is treated as "no
// markers found" — the PR is still skipped on the no-threads-no-markers
// path. Regression guard for the gemini comment that wanted the error
// surfaced rather than silently equated with "no conflicts."
func TestSetupOnePR_DetectMarkersErrorTreatedAsNoMarkers(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main",
			Title: "X", MergeableState: "MERGEABLE"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt", markersErr: errors.New("git boom")}
	tf := &fakeThreadFetcher{} // 0 threads

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "skipped" {
		t.Fatalf("want skipped (no threads + DetectMarkers err treated as none); got %+v", got)
	}
	if got.Reason != "no unresolved threads" {
		t.Fatalf("reason = %q, want 'no unresolved threads'", got.Reason)
	}
}

// TestSetupOnePR_DetectMarkersFlagsConflictPath asserts the no-threads path
// flips to "ready" when the worktree has actual conflict markers — so the
// process phase will run conflict resolution.
func TestSetupOnePR_DetectMarkersFlagsConflictPath(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main",
			Title: "X", MergeableState: "CONFLICTING"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt", markers: []string{"file.go"}}
	tf := &fakeThreadFetcher{}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "ready" {
		t.Fatalf("want ready (no threads but markers present); got %+v", got)
	}
	if !got.HasMergeConflicts {
		t.Fatalf("HasMergeConflicts should be true; got %+v", got)
	}
}

// --- Comment 2 (codex): dry-run must not report 'ok / resolved merge conflicts'

// TestProcessPR_DryRunWithConflicts_NotReportedAsOk asserts that when conflict
// markers are detected, threads are zero, and DryRun is on, ProcessPR returns
// "skipped" — not "ok / resolved merge conflicts" — because nothing was
// actually committed or pushed.
func TestProcessPR_DryRunWithConflicts_NotReportedAsOk(t *testing.T) {
	installSeams(t)

	// Arrange: PR is OPEN, 0 threads, conflict markers detected pre-fix.
	fetchPRFn = func(_ context.Context, _ string, _ int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{
			State: "OPEN", HeadRefName: "feature", BaseRefName: "main",
			Title: "T", MergeableState: "CONFLICTING",
		}, nil
	}
	fetchThreadsFn = func(context.Context, string, int, int) ([]ghapi.Thread, error) {
		return nil, nil
	}
	detectMarkersFn = func(string) ([]string, error) {
		return []string{"file.go"}, nil
	}

	opts := branchBaseOpts(t)
	opts.DryRun = true

	res := ProcessPR(ctxBG(), opts)
	if res.Status == "ok" {
		t.Fatalf("dry-run with conflicts must not report ok; got %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Reason), "dry-run") {
		t.Fatalf("reason should mention dry-run; got %q", res.Reason)
	}
}

// --- Comment 3 (codex): concurrent output must stream, not buffer unbounded

// TestLineBufferedWriter_FlushesPartialOnFlush guards the streaming
// guarantee: any tail bytes that haven't seen a newline are still emitted
// when the goroutine finishes. Together with the lockedWriter sink this
// keeps memory bounded to ~1 line per PR.
func TestLineBufferedWriter_FlushesPartialOnFlush(t *testing.T) {
	var sink writerCapture
	w := newLineBufferedWriter(&sink)
	if _, err := w.Write([]byte("partial without newline")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := sink.String(); got != "" {
		t.Fatalf("partial line should NOT be flushed before Flush(); got %q", got)
	}
	w.Flush()
	got := sink.String()
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("Flush should append newline so next PR's header isn't joined; got %q", got)
	}
	if !strings.Contains(got, "partial without newline") {
		t.Fatalf("flushed output should contain partial; got %q", got)
	}
}

// TestLineBufferedWriter_LineGranularity asserts that completed lines flush
// promptly through the sink (not held until the writer is closed).
func TestLineBufferedWriter_LineGranularity(t *testing.T) {
	var sink writerCapture
	w := newLineBufferedWriter(&sink)
	w.Write([]byte("first\n"))
	if got := sink.String(); got != "first\n" {
		t.Fatalf("first line should flush immediately; got %q", got)
	}
	w.Write([]byte("second"))
	if got := sink.String(); got != "first\n" {
		t.Fatalf("partial second line shouldn't flush yet; got %q", got)
	}
	w.Write([]byte(" half\nthird\n"))
	want := "first\nsecond half\nthird\n"
	if got := sink.String(); got != want {
		t.Fatalf("after newlines flush; got %q, want %q", got, want)
	}
}

// writerCapture is a tiny io.Writer that accumulates bytes for assertions.
type writerCapture struct{ b []byte }

func (w *writerCapture) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *writerCapture) String() string { return string(w.b) }
