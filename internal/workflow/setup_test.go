package workflow

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ghapi "github.com/maszynka/gh-crfix/internal/github"
)

// fakePRFetcher / fakeWorktreeSetup / fakeThreadFetcher implement the
// interfaces used by setupOnePR. They let us drive the pure setup logic
// without shelling out to git or gh.

type fakePRFetcher struct {
	prs map[int]ghapi.PRInfo
	err map[int]error
}

func (f *fakePRFetcher) FetchPR(repo string, prNum int) (ghapi.PRInfo, error) {
	if e, ok := f.err[prNum]; ok {
		return ghapi.PRInfo{}, e
	}
	p, ok := f.prs[prNum]
	if !ok {
		return ghapi.PRInfo{}, fmt.Errorf("not found")
	}
	return p, nil
}

type fakeWorktreeSetup struct {
	path       string
	err        error
	dirty      string
	collisions [][]string
}

func (w *fakeWorktreeSetup) Setup(repoRoot, branch string, prNum int) (string, error) {
	return w.path, w.err
}
func (w *fakeWorktreeSetup) DirtyStatus(path string) (string, error)          { return w.dirty, nil }
func (w *fakeWorktreeSetup) DetectCaseCollisions(path string) ([][]string, error) {
	return w.collisions, nil
}
func (w *fakeWorktreeSetup) RepoRoot(path string) (string, error) { return "/repo", nil }

type fakeThreadFetcher struct {
	threads []ghapi.Thread
	err     error
	delay   time.Duration
}

func (f *fakeThreadFetcher) FetchThreads(repo string, prNum, maxThreads int) ([]ghapi.Thread, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.threads, f.err
}

func baseOpts() Options {
	return Options{
		Repo:       "owner/repo",
		PRNum:      1,
		RepoRoot:   "/repo",
		MaxThreads: 100,
	}
}

func TestSetupOnePR_ClosedPRSkipped(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "CLOSED", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "skipped" {
		t.Fatalf("want skipped, got %q (%s)", got.Status, got.Reason)
	}
	if got.Reason != "PR is CLOSED" {
		t.Fatalf("want reason %q, got %q", "PR is CLOSED", got.Reason)
	}
}

func TestSetupOnePR_NotFoundSkipped(t *testing.T) {
	prf := &fakePRFetcher{err: map[int]error{1: errors.New("gh: not found")}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "skipped" {
		t.Fatalf("want skipped, got %q (%s)", got.Status, got.Reason)
	}
	if got.Reason != "not found" {
		t.Fatalf("want reason %q, got %q", "not found", got.Reason)
	}
}

func TestSetupOnePR_OpenZeroThreadsSkipped(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{threads: nil}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "skipped" || got.Reason != "no unresolved threads" {
		t.Fatalf("want skipped/no unresolved threads; got %+v", got)
	}
}

func TestSetupOnePR_OpenWithThreadsReady(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X", HeadSHA: "abc"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{{ID: "t1"}, {ID: "t2"}}}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "ready" {
		t.Fatalf("want ready; got %+v", got)
	}
	if got.Threads != 2 {
		t.Fatalf("want 2 threads; got %d", got.Threads)
	}
	if got.Worktree != "/wt" {
		t.Fatalf("want worktree=/wt; got %q", got.Worktree)
	}
	if got.HeadSHA != "abc" {
		t.Fatalf("want HeadSHA=abc; got %q", got.HeadSHA)
	}
}

func TestSetupOnePR_WorktreeErrorFails(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetup{err: errors.New("boom")}
	tf := &fakeThreadFetcher{}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "failed" {
		t.Fatalf("want failed; got %+v", got)
	}
	if got.Reason != "worktree setup failed" {
		t.Fatalf("want reason=worktree setup failed; got %q", got.Reason)
	}
}

func TestSetupOnePR_CaseCollisionReadyWithFlag(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetup{
		path:       "/wt",
		dirty:      " M somefile",
		collisions: [][]string{{"Foo.go", "foo.go"}},
	}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{{ID: "t1"}}}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "ready" {
		t.Fatalf("want ready (case-collision resolvable later); got %+v", got)
	}
	if !got.HasCaseCol {
		t.Fatalf("want HasCaseCol=true; got %+v", got)
	}
}

func TestSetupOnePR_DirtyNoCollisionFails(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetup{
		path:       "/wt",
		dirty:      " M somefile",
		collisions: nil,
	}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{{ID: "t1"}}}

	got := setupOnePR(baseOpts(), prf, wt, tf, nil, nil)
	if got.Status != "failed" {
		t.Fatalf("want failed; got %+v", got)
	}
	if got.Reason != "worktree not clean" {
		t.Fatalf("want reason=worktree not clean; got %q", got.Reason)
	}
}

func TestSetupOnePR_SetupOnlyMarksSkipped(t *testing.T) {
	prf := &fakePRFetcher{prs: map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "feature", BaseRefName: "main", Title: "X"},
	}}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &fakeThreadFetcher{threads: []ghapi.Thread{{ID: "t1"}}}

	opts := baseOpts()
	opts.SetupOnly = true
	got := setupOnePR(opts, prf, wt, tf, nil, nil)
	if got.Status != "skipped" || got.Reason != "setup-only" {
		t.Fatalf("want skipped/setup-only; got %+v", got)
	}
}

// --- SetupPhase concurrency test ------------------------------------------

// trackingFetcher is a thread fetcher that sleeps `delay` per call and
// counts peak concurrent invocations.
type trackingFetcher struct {
	delay   time.Duration
	inFlight int32
	peak    int32
	threads []ghapi.Thread
	mu      sync.Mutex
}

func (f *trackingFetcher) FetchThreads(repo string, prNum, maxThreads int) ([]ghapi.Thread, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	f.mu.Lock()
	if cur > f.peak {
		f.peak = cur
	}
	f.mu.Unlock()
	time.Sleep(f.delay)
	return f.threads, nil
}

func TestSetupPhase_ParallelizesAndPreservesOrder(t *testing.T) {
	prs := map[int]ghapi.PRInfo{
		1: {State: "OPEN", HeadRefName: "a", BaseRefName: "main", Title: "One"},
		2: {State: "OPEN", HeadRefName: "b", BaseRefName: "main", Title: "Two"},
		3: {State: "OPEN", HeadRefName: "c", BaseRefName: "main", Title: "Three"},
	}
	prf := &fakePRFetcher{prs: prs}
	wt := &fakeWorktreeSetup{path: "/wt"}
	tf := &trackingFetcher{
		delay:   100 * time.Millisecond,
		threads: []ghapi.Thread{{ID: "x"}},
	}

	opts := BatchOptions{
		PRNums: []int{1, 2, 3},
		Base:   baseOpts(),
	}

	start := time.Now()
	out := setupPhaseWith(opts, prf, wt, tf, nil, nil, 3)
	elapsed := time.Since(start)

	if len(out) != 3 {
		t.Fatalf("want 3 results; got %d", len(out))
	}
	// Order preserved.
	for i, pr := range []int{1, 2, 3} {
		if out[i].PRNum != pr {
			t.Fatalf("result[%d].PRNum = %d; want %d", i, out[i].PRNum, pr)
		}
	}
	// All ready.
	for _, r := range out {
		if r.Status != "ready" {
			t.Fatalf("PR %d not ready: %+v", r.PRNum, r)
		}
	}
	// Concurrency: 3 calls at 100ms each must finish well under 250ms.
	if elapsed > 250*time.Millisecond {
		t.Fatalf("setup phase took %v; expected <250ms with concurrency=3", elapsed)
	}
	if tf.peak < 2 {
		t.Fatalf("expected at least 2 concurrent fetches; peak=%d", tf.peak)
	}
}
