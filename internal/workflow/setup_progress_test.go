package workflow

import (
	"bytes"
	"strings"
	"testing"

	ghapi "github.com/maszynka/gh-crfix/internal/github"
)

// TestSetupOnePR_ProgressOutEmitsStartAndReady verifies that a ready PR
// emits both a "fetching metadata" and a "ready" line to opts.ProgressOut.
// This is the UX fix so plain-text callers see progress during long setups.
func TestSetupOnePR_ProgressOutEmitsStartAndReady(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "t"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{
		{ID: "t1"},
		{ID: "t2"},
	}}

	var buf bytes.Buffer
	opts := baseOpts()
	opts.ProgressOut = &buf

	got := setupOnePR(opts, prf, wt, tf, nil, nil)
	if got.Status != "ready" {
		t.Fatalf("want ready, got %q (%s)", got.Status, got.Reason)
	}

	out := buf.String()
	if !strings.Contains(out, "[setup] PR #1: fetching metadata") {
		t.Errorf("missing 'fetching metadata' line; got:\n%s", out)
	}
	if !strings.Contains(out, "[setup] PR #1: ready") {
		t.Errorf("missing 'ready' line; got:\n%s", out)
	}
	if !strings.Contains(out, "2 thread") {
		t.Errorf("ready line missing thread count; got:\n%s", out)
	}
}

// TestSetupOnePR_ProgressOutEmitsFailed: a worktree setup failure should
// produce a "failed" line.
func TestSetupOnePR_ProgressOutEmitsFailed(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main"},
	}}
	wt := &fakeWorktreeSetup{err: errSetup}
	tf := &fakeThreadFetcher{}

	var buf bytes.Buffer
	opts := baseOpts()
	opts.ProgressOut = &buf

	got := setupOnePR(opts, prf, wt, tf, nil, nil)
	if got.Status != "failed" {
		t.Fatalf("want failed, got %q (%s)", got.Status, got.Reason)
	}

	out := buf.String()
	if !strings.Contains(out, "[setup] PR #1: fetching metadata") {
		t.Errorf("missing 'fetching metadata' line; got:\n%s", out)
	}
	if !strings.Contains(out, "[setup] PR #1: failed") {
		t.Errorf("missing 'failed' line; got:\n%s", out)
	}
}

// TestSetupOnePR_ProgressOutEmitsSkipped: a not-found PR should produce a
// "skipped" line.
func TestSetupOnePR_ProgressOutEmitsSkipped(t *testing.T) {
	prf := &fakePRFetcher{err: map[int]error{1: errSetup}}
	wt := &fakeWorktreeSetup{}
	tf := &fakeThreadFetcher{}

	var buf bytes.Buffer
	opts := baseOpts()
	opts.ProgressOut = &buf

	got := setupOnePR(opts, prf, wt, tf, nil, nil)
	if got.Status != "skipped" {
		t.Fatalf("want skipped, got %q (%s)", got.Status, got.Reason)
	}

	out := buf.String()
	if !strings.Contains(out, "[setup] PR #1: skipped") {
		t.Errorf("missing 'skipped' line; got:\n%s", out)
	}
}

// TestSetupOnePR_NilProgressOutNoPanic: ensure nil writer is tolerated.
func TestSetupOnePR_NilProgressOutNoPanic(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{{ID: "t1"}}}
	opts := baseOpts()
	opts.ProgressOut = nil
	got := setupOnePR(opts, prf, wt, tf, nil, nil)
	if got.Status != "ready" {
		t.Fatalf("want ready, got %q", got.Status)
	}
}

var errSetup = &setupErr{}

type setupErr struct{}

// Message shape mirrors gh's output for a missing PR so that
// looksLikeNotFound() correctly routes this to "skipped / not found".
// A generic error would (correctly) be surfaced as a failure instead.
func (e *setupErr) Error() string { return "could not resolve to a Repository with the name" }
