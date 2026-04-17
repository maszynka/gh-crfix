package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ghapi "github.com/maszynka/gh-crfix/internal/github"
)

// noopLog is a log function that drops all output so we don't pollute test
// output when driving the post-fix review cycle directly.
func noopLog(string, ...interface{}) {}

// --- 1. postFixReviewCycle: no new threads -----------------------------------

func TestPostFixReviewCycle_NoNewThreads(t *testing.T) {
	installSeams(t)

	// Record what the cycle does.
	var (
		sleepMu      sync.Mutex
		sleepCalls   []time.Duration
		commentsMu   sync.Mutex
		commentBody  []string
		copilotCount int32
		mergeCount   int32
	)

	sleepFn = func(d time.Duration) {
		sleepMu.Lock()
		sleepCalls = append(sleepCalls, d)
		sleepMu.Unlock()
	}
	fetchThreadsFn = func(context.Context, string, int, int) ([]ghapi.Thread, error) { return nil, nil }
	postCommentFn = func(_ context.Context, _ string, _ int, body string) error {
		commentsMu.Lock()
		commentBody = append(commentBody, body)
		commentsMu.Unlock()
		return nil
	}
	fetchPRFn = func(context.Context, string, int) (ghapi.PRInfo, error) {
		return ghapi.PRInfo{State: "OPEN", HeadRefName: "feature", BaseRefName: "main"}, nil
	}
	mergeBaseFn = func(context.Context, string, string) error {
		atomic.AddInt32(&mergeCount, 1)
		return nil
	}
	requestCopilotReviewFn = func(context.Context, string, int) error {
		atomic.AddInt32(&copilotCount, 1)
		return nil
	}

	opts := branchBaseOpts(t)
	opts.ReviewWaitSecs = 7
	wtPath := t.TempDir()

	postFixReviewCycle(context.Background(), opts, wtPath, 4, noopLog)

	sleepMu.Lock()
	defer sleepMu.Unlock()
	if len(sleepCalls) != 1 {
		t.Fatalf("sleepFn calls=%d want 1", len(sleepCalls))
	}
	if sleepCalls[0] != 7*time.Second {
		t.Fatalf("sleepFn duration=%v want 7s", sleepCalls[0])
	}

	commentsMu.Lock()
	defer commentsMu.Unlock()
	if len(commentBody) == 0 {
		t.Fatalf("expected at least one comment posted")
	}
	found := false
	for _, b := range commentBody {
		if strings.Contains(b, "All 4 comments addressed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want 'All 4 comments addressed' summary comment; got %v", commentBody)
	}
	if atomic.LoadInt32(&copilotCount) != 1 {
		t.Fatalf("copilot re-review should have been requested once; got %d", copilotCount)
	}
	if atomic.LoadInt32(&mergeCount) != 1 {
		t.Fatalf("merge base should have been called once; got %d", mergeCount)
	}
}

// --- 2. postFixReviewCycle: new threads show up ------------------------------

func TestPostFixReviewCycle_NewThreadsFound(t *testing.T) {
	installSeams(t)

	var (
		commentsMu   sync.Mutex
		commentBody  []string
		copilotCount int32
		mergeCount   int32
	)

	sleepFn = func(time.Duration) {}
	fetchThreadsFn = func(context.Context, string, int, int) ([]ghapi.Thread, error) {
		return []ghapi.Thread{{ID: "t1"}, {ID: "t2"}}, nil
	}
	postCommentFn = func(_ context.Context, _ string, _ int, body string) error {
		commentsMu.Lock()
		commentBody = append(commentBody, body)
		commentsMu.Unlock()
		return nil
	}
	mergeBaseFn = func(context.Context, string, string) error {
		atomic.AddInt32(&mergeCount, 1)
		return nil
	}
	requestCopilotReviewFn = func(context.Context, string, int) error {
		atomic.AddInt32(&copilotCount, 1)
		return nil
	}

	opts := branchBaseOpts(t)
	opts.ReviewWaitSecs = 1
	wtPath := t.TempDir()

	postFixReviewCycle(context.Background(), opts, wtPath, 5, noopLog)

	commentsMu.Lock()
	defer commentsMu.Unlock()
	if len(commentBody) != 1 {
		t.Fatalf("want 1 comment; got %d (%v)", len(commentBody), commentBody)
	}
	if !strings.Contains(commentBody[0], "Fixed 5 comments, but 2 new issue(s) raised") {
		t.Fatalf("unexpected comment body: %q", commentBody[0])
	}

	if atomic.LoadInt32(&copilotCount) != 0 {
		t.Fatalf("copilot re-review must NOT be requested when new threads exist; got %d", copilotCount)
	}
	if atomic.LoadInt32(&mergeCount) != 0 {
		t.Fatalf("mergeBase must NOT be called on new-threads branch; got %d", mergeCount)
	}
}

// --- 3. postFixReviewCycle: fetch threads errors out -------------------------

func TestPostFixReviewCycle_FetchError(t *testing.T) {
	installSeams(t)

	var (
		sleepCount   int32
		commentCount int32
	)

	sleepFn = func(time.Duration) { atomic.AddInt32(&sleepCount, 1) }
	fetchThreadsFn = func(context.Context, string, int, int) ([]ghapi.Thread, error) {
		return nil, errors.New("boom")
	}
	postCommentFn = func(context.Context, string, int, string) error {
		atomic.AddInt32(&commentCount, 1)
		return nil
	}

	opts := branchBaseOpts(t)
	opts.ReviewWaitSecs = 1
	wtPath := t.TempDir()

	postFixReviewCycle(context.Background(), opts, wtPath, 1, noopLog)

	if atomic.LoadInt32(&sleepCount) != 1 {
		t.Fatalf("sleepFn should still be invoked once; got %d", sleepCount)
	}
	if atomic.LoadInt32(&commentCount) != 0 {
		t.Fatalf("no comment should be posted on fetch error; got %d", commentCount)
	}
}

// --- 4. postFixReviewCycle: default wait when ReviewWaitSecs=0 --------------

func TestPostFixReviewCycle_DefaultWait(t *testing.T) {
	installSeams(t)

	var (
		sleepMu    sync.Mutex
		sleepCalls []time.Duration
	)

	sleepFn = func(d time.Duration) {
		sleepMu.Lock()
		sleepCalls = append(sleepCalls, d)
		sleepMu.Unlock()
	}
	// Return empty threads so the cycle completes through the "no new
	// threads" path, which still sleeps first.
	fetchThreadsFn = func(context.Context, string, int, int) ([]ghapi.Thread, error) { return nil, nil }
	postCommentFn = func(context.Context, string, int, string) error { return nil }
	requestCopilotReviewFn = func(context.Context, string, int) error { return nil }

	opts := branchBaseOpts(t)
	// ReviewWaitSecs == 0 → should default to 180.
	opts.ReviewWaitSecs = 0

	postFixReviewCycle(context.Background(), opts, t.TempDir(), 0, noopLog)

	sleepMu.Lock()
	defer sleepMu.Unlock()
	if len(sleepCalls) != 1 {
		t.Fatalf("sleepFn called %d times; want 1", len(sleepCalls))
	}
	if sleepCalls[0] != 180*time.Second {
		t.Fatalf("sleepFn duration=%v want 180s (default)", sleepCalls[0])
	}
}
