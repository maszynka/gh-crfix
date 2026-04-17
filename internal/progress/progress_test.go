package progress

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// canonicalSteps mirrors the 14 steps the tracker must expose, in order.
var canonicalSteps = []Step{
	StepSetup,
	StepNormalizeCase,
	StepMergeBase,
	StepResolveConflicts,
	StepFetchThreads,
	StepFilterThreads,
	StepAutofix,
	StepValidate,
	StepGate,
	StepFix,
	StepReply,
	StepCleanup,
	StepRereview,
	StepPostfix,
}

var canonicalLabels = map[Step]string{
	StepSetup:            "Setup worktree",
	StepNormalizeCase:    "Normalize case",
	StepMergeBase:        "Merge base branch",
	StepResolveConflicts: "Resolve conflict markers",
	StepFetchThreads:     "Fetch review threads",
	StepFilterThreads:    "Triage threads",
	StepAutofix:          "Autofix hook",
	StepValidate:         "Validation",
	StepGate:             "Gate model",
	StepFix:              "Fix model",
	StepReply:            "Reply & resolve",
	StepCleanup:          "Cleanup artifact",
	StepRereview:         "Request re-review",
	StepPostfix:          "Post-fix review cycle",
}

func TestAllStepsCanonicalOrder(t *testing.T) {
	got := AllSteps()
	if len(got) != 14 {
		t.Fatalf("AllSteps() length = %d, want 14", len(got))
	}
	if !reflect.DeepEqual(got, canonicalSteps) {
		t.Fatalf("AllSteps() mismatch\ngot:  %v\nwant: %v", got, canonicalSteps)
	}
}

func TestAllStepsReturnsCopy(t *testing.T) {
	a := AllSteps()
	if len(a) == 0 {
		t.Fatal("AllSteps returned empty slice")
	}
	original := a[0]
	a[0] = Step("mutated")
	b := AllSteps()
	if b[0] != original {
		t.Fatalf("AllSteps should return an independent copy; got %q after mutation", b[0])
	}
}

func TestLabelCoversAllSteps(t *testing.T) {
	for _, s := range canonicalSteps {
		want := canonicalLabels[s]
		got := Label(s)
		if got != want {
			t.Errorf("Label(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestLabelUnknownStep(t *testing.T) {
	got := Label(Step("no_such_step"))
	if got == "" {
		t.Fatalf("Label(unknown) should not be empty; got %q", got)
	}
}

func newTracker(t *testing.T) (*Tracker, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "progress")
	return NewTracker(root), root
}

func TestInitWritesPending(t *testing.T) {
	tr, _ := newTracker(t)
	if err := tr.Init(42); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, s := range AllSteps() {
		st, note, ok := tr.Get(42, s)
		if !ok {
			t.Errorf("step %q: Get ok=false, want true", s)
			continue
		}
		if st != Pending {
			t.Errorf("step %q: status=%q, want %q", s, st, Pending)
		}
		if note != "" {
			t.Errorf("step %q: note=%q, want empty", s, note)
		}
	}
}

func TestSetGetRoundtrip(t *testing.T) {
	tr, _ := newTracker(t)
	cases := []struct {
		step   Step
		status Status
		note   string
	}{
		{StepSetup, Pending, ""},
		{StepNormalizeCase, Running, "normalizing commit X"},
		{StepMergeBase, Done, "merged main"},
		{StepResolveConflicts, Failed, "unresolved marker in a.go"},
		{StepFetchThreads, Skipped, "no threads"},
		{StepFix, Done, "two files patched"},
	}
	for _, c := range cases {
		if err := tr.Set(7, c.step, c.status, c.note); err != nil {
			t.Fatalf("Set(%v): %v", c, err)
		}
	}
	for _, c := range cases {
		st, note, ok := tr.Get(7, c.step)
		if !ok {
			t.Errorf("Get(%v) ok=false, want true", c.step)
			continue
		}
		if st != c.status {
			t.Errorf("Get(%v) status=%q, want %q", c.step, st, c.status)
		}
		if note != c.note {
			t.Errorf("Get(%v) note=%q, want %q", c.step, note, c.note)
		}
	}
}

func TestSetUnknownStepErrors(t *testing.T) {
	tr, _ := newTracker(t)
	if err := tr.Set(1, Step("bogus"), Done, ""); err == nil {
		t.Fatalf("Set(unknown step) expected error, got nil")
	}
}

func TestGetMissingFilesReturnsFalse(t *testing.T) {
	tr, _ := newTracker(t)
	_, _, ok := tr.Get(99, StepSetup)
	if ok {
		t.Fatalf("Get on never-written pr/step should return ok=false")
	}
}

func TestGetNoteOnlyMissingStatusReturnsFalse(t *testing.T) {
	tr, root := newTracker(t)
	// Write a note but no status — Get must still say "not present".
	prDir := filepath.Join(root, "pr-5")
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prDir, string(StepSetup)+".note"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, ok := tr.Get(5, StepSetup)
	if ok {
		t.Fatalf("Get with note-only on disk should return ok=false")
	}
}

func TestMarkRemainingOverwritesPendingAndRunning(t *testing.T) {
	tr, _ := newTracker(t)
	if err := tr.Init(3); err != nil {
		t.Fatal(err)
	}
	// Terminal states that must be preserved.
	if err := tr.Set(3, StepSetup, Done, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set(3, StepMergeBase, Failed, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set(3, StepFix, Skipped, "no changes needed"); err != nil {
		t.Fatal(err)
	}
	// A running one that should be rewritten.
	if err := tr.Set(3, StepGate, Running, "thinking"); err != nil {
		t.Fatal(err)
	}

	if err := tr.MarkRemaining(3, Skipped, "early exit"); err != nil {
		t.Fatalf("MarkRemaining: %v", err)
	}

	checks := map[Step]struct {
		status Status
		note   string
	}{
		StepSetup:     {Done, "ok"},
		StepMergeBase: {Failed, "boom"},
		StepFix:       {Skipped, "no changes needed"},
		// Rewritten entries:
		StepGate:             {Skipped, "early exit"},
		StepNormalizeCase:    {Skipped, "early exit"},
		StepResolveConflicts: {Skipped, "early exit"},
		StepFetchThreads:     {Skipped, "early exit"},
		StepFilterThreads:    {Skipped, "early exit"},
		StepAutofix:          {Skipped, "early exit"},
		StepValidate:         {Skipped, "early exit"},
		StepReply:            {Skipped, "early exit"},
		StepCleanup:          {Skipped, "early exit"},
		StepRereview:         {Skipped, "early exit"},
		StepPostfix:          {Skipped, "early exit"},
	}
	for step, want := range checks {
		st, note, ok := tr.Get(3, step)
		if !ok {
			t.Errorf("Get(%q) ok=false", step)
			continue
		}
		if st != want.status {
			t.Errorf("Get(%q) status=%q, want %q", step, st, want.status)
		}
		if note != want.note {
			t.Errorf("Get(%q) note=%q, want %q", step, note, want.note)
		}
	}
}

func TestMarkRemainingOnFreshPR(t *testing.T) {
	// Before Init: no files exist. MarkRemaining should not explode and should
	// leave the tracker in a valid state (no entries written, since there is
	// nothing pending/running to rewrite).
	tr, _ := newTracker(t)
	if err := tr.MarkRemaining(11, Failed, "aborted"); err != nil {
		t.Fatalf("MarkRemaining on empty state: %v", err)
	}
	for _, s := range AllSteps() {
		if _, _, ok := tr.Get(11, s); ok {
			t.Errorf("step %q unexpectedly present after MarkRemaining on empty state", s)
		}
	}
}

func TestSnapshotAcrossPRs(t *testing.T) {
	tr, _ := newTracker(t)
	if err := tr.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := tr.Init(2); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set(1, StepGate, Done, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set(2, StepFix, Failed, "patch failed"); err != nil {
		t.Fatal(err)
	}

	snap := tr.Snapshot()
	if _, ok := snap[1]; !ok {
		t.Fatalf("snapshot missing PR 1")
	}
	if _, ok := snap[2]; !ok {
		t.Fatalf("snapshot missing PR 2")
	}
	if got := snap[1][StepGate]; got.Status != Done || got.Note != "approved" {
		t.Errorf("snap[1][gate] = %+v, want {Done approved}", got)
	}
	if got := snap[2][StepFix]; got.Status != Failed || got.Note != "patch failed" {
		t.Errorf("snap[2][fix] = %+v, want {Failed patch failed}", got)
	}
	// Every canonical step should be present for both PRs (after Init).
	for _, prNum := range []int{1, 2} {
		for _, s := range AllSteps() {
			if _, ok := snap[prNum][s]; !ok {
				t.Errorf("snap[%d] missing step %q", prNum, s)
			}
		}
	}
}

func TestSnapshotEmptyRoot(t *testing.T) {
	tr, _ := newTracker(t)
	snap := tr.Snapshot()
	if snap == nil {
		t.Fatalf("Snapshot() returned nil, want empty non-nil map")
	}
	if len(snap) != 0 {
		t.Fatalf("Snapshot() on empty root = %v, want empty", snap)
	}
}

func TestPRsNumericallySorted(t *testing.T) {
	tr, _ := newTracker(t)
	// Intentionally out-of-order and not lex-order-friendly (10 < 2 lex).
	for _, n := range []int{10, 2, 100, 7, 33} {
		if err := tr.Init(n); err != nil {
			t.Fatal(err)
		}
	}
	got := tr.PRs()
	want := []int{2, 7, 10, 33, 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PRs() = %v, want %v", got, want)
	}
}

func TestPRsIgnoresJunkDirs(t *testing.T) {
	tr, root := newTracker(t)
	if err := tr.Init(5); err != nil {
		t.Fatal(err)
	}
	// Drop garbage in the progress dir that shouldn't show up as a PR.
	if err := os.MkdirAll(filepath.Join(root, "not-a-pr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pr-notanumber"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tr.PRs()
	want := []int{5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PRs() = %v, want %v", got, want)
	}
}

func TestConcurrentSetsNoRacesNoLostWrites(t *testing.T) {
	tr, _ := newTracker(t)
	// 20 goroutines, each pinned to a distinct (pr, step) pair so the final
	// state is deterministic and we can detect lost writes.
	const N = 20
	var wg sync.WaitGroup
	steps := AllSteps()
	type key struct {
		pr   int
		step Step
	}
	plan := make([]key, N)
	for i := 0; i < N; i++ {
		plan[i] = key{pr: 1000 + i, step: steps[i%len(steps)]}
	}
	wg.Add(N)
	for i, k := range plan {
		i, k := i, k
		go func() {
			defer wg.Done()
			note := fmt.Sprintf("worker-%d", i)
			if err := tr.Set(k.pr, k.step, Done, note); err != nil {
				t.Errorf("Set(%+v): %v", k, err)
			}
		}()
	}
	wg.Wait()

	for i, k := range plan {
		st, note, ok := tr.Get(k.pr, k.step)
		if !ok {
			t.Errorf("Get(%+v) ok=false", k)
			continue
		}
		if st != Done {
			t.Errorf("Get(%+v) status=%q, want %q", k, st, Done)
		}
		want := fmt.Sprintf("worker-%d", i)
		if note != want {
			t.Errorf("Get(%+v) note=%q, want %q", k, note, want)
		}
	}
}

func TestConcurrentSetsSameKey(t *testing.T) {
	// Hammer the same (pr, step) from many goroutines — the file must always
	// be readable (never observe a half-written status) and the final value
	// must match one of the notes written.
	tr, _ := newTracker(t)
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			note := fmt.Sprintf("w-%d", i)
			if err := tr.Set(42, StepFix, Running, note); err != nil {
				t.Errorf("Set: %v", err)
			}
		}()
	}
	// Simultaneously read.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			st, _, ok := tr.Get(42, StepFix)
			if ok && st != Running {
				t.Errorf("concurrent Get observed bad status %q", st)
				return
			}
		}
	}()
	wg.Wait()
	close(done)

	st, note, ok := tr.Get(42, StepFix)
	if !ok {
		t.Fatalf("Get ok=false after concurrent writes")
	}
	if st != Running {
		t.Errorf("final status=%q, want Running", st)
	}
	valid := false
	for i := 0; i < N; i++ {
		if note == fmt.Sprintf("w-%d", i) {
			valid = true
			break
		}
	}
	if !valid {
		t.Errorf("final note=%q doesn't match any writer", note)
	}
}

func TestSetCreatesRootOnDemand(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "does", "not", "exist", "yet")
	tr := NewTracker(root)
	if err := tr.Set(1, StepSetup, Done, "ok"); err != nil {
		t.Fatalf("Set should create root on demand: %v", err)
	}
	st, note, ok := tr.Get(1, StepSetup)
	if !ok || st != Done || note != "ok" {
		t.Fatalf("Get after lazy-mkdir = (%q, %q, %v)", st, note, ok)
	}
}

func TestSetStatusRewritesNote(t *testing.T) {
	// Each Set is a full overwrite of both status and note — a new call with
	// an empty note must clear any previous note.
	tr, _ := newTracker(t)
	if err := tr.Set(1, StepFix, Running, "trying thing"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set(1, StepFix, Done, ""); err != nil {
		t.Fatal(err)
	}
	st, note, ok := tr.Get(1, StepFix)
	if !ok || st != Done || note != "" {
		t.Fatalf("expected (Done, \"\", true); got (%q, %q, %v)", st, note, ok)
	}
}

// sanity: make sure our canonicalLabels test map exhaustively mirrors AllSteps.
func TestLabelsExhaustive(t *testing.T) {
	got := AllSteps()
	seen := make(map[Step]bool, len(got))
	for _, s := range got {
		seen[s] = true
	}
	if len(seen) != len(canonicalLabels) {
		t.Fatalf("canonicalLabels has %d entries, AllSteps has %d", len(canonicalLabels), len(seen))
	}
	// Stable comparator so failures are readable.
	var missing []string
	for s := range canonicalLabels {
		if !seen[s] {
			missing = append(missing, string(s))
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("label entries not in AllSteps: %v", missing)
	}
}
