// Package progress tracks per-PR pipeline state across the 14 canonical
// steps that gh-crfix runs for each pull request. State is persisted to a
// filesystem layout compatible with the original bash tool so the Go port
// and legacy scripts can share the same LOG_DIR/progress tree if needed.
//
// On-disk layout:
//
//	<rootDir>/pr-<num>/<step>.status
//	<rootDir>/pr-<num>/<step>.note
//
// Writes are atomic (tmp file + rename) and safe for concurrent use from
// multiple goroutines.
package progress

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Status is a step's lifecycle state.
type Status string

const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
	Skipped Status = "skipped"
)

// Step is a canonical pipeline step name. Use one of the exported Step* constants.
type Step string

const (
	StepSetup            Step = "setup"
	StepNormalizeCase    Step = "normalize_case"
	StepMergeBase        Step = "merge_base"
	StepResolveConflicts Step = "resolve_conflicts"
	StepFetchThreads     Step = "fetch_threads"
	StepFilterThreads    Step = "filter_threads"
	StepAutofix          Step = "autofix"
	StepValidate         Step = "validate"
	StepGate             Step = "gate"
	StepFix              Step = "fix"
	StepReply            Step = "reply"
	StepCleanup          Step = "cleanup"
	StepRereview         Step = "rereview"
	StepPostfix          Step = "postfix"
)

// allStepsCanonical is the authoritative ordering returned by AllSteps.
var allStepsCanonical = []Step{
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

var stepLabels = map[Step]string{
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

// validStep lets us quickly reject unknown step names passed to Set.
var validStep = func() map[Step]struct{} {
	m := make(map[Step]struct{}, len(allStepsCanonical))
	for _, s := range allStepsCanonical {
		m[s] = struct{}{}
	}
	return m
}()

// AllSteps returns the canonical 14 steps in pipeline order. The returned
// slice is an independent copy; callers may mutate it freely.
func AllSteps() []Step {
	out := make([]Step, len(allStepsCanonical))
	copy(out, allStepsCanonical)
	return out
}

// Label returns a human-readable label for the given step, or a fallback
// derived from the raw step name when the step is unknown. Label never
// returns an empty string.
func Label(s Step) string {
	if lbl, ok := stepLabels[s]; ok {
		return lbl
	}
	if s == "" {
		return "(unknown step)"
	}
	return string(s)
}

// Entry is a point-in-time view of a single step.
type Entry struct {
	Status Status
	Note   string
}

// Tracker persists per-PR per-step state under a root directory.
// The zero value is not usable — use NewTracker.
type Tracker struct {
	root string
	mu   sync.Mutex // guards on-disk mutations; coarse but keeps writes atomic
}

// NewTracker returns a Tracker rooted at rootDir. The directory (and any
// parents) are created lazily on first write.
func NewTracker(rootDir string) *Tracker {
	return &Tracker{root: rootDir}
}

// Init writes Pending with an empty note for every canonical step under the
// given PR. Existing entries for that PR are overwritten.
func (t *Tracker) Init(prNum int) error {
	for _, s := range allStepsCanonical {
		if err := t.Set(prNum, s, Pending, ""); err != nil {
			return err
		}
	}
	return nil
}

// Set records the status and note for (prNum, step). Unknown step names are
// rejected with an error. Both the status and note files are rewritten
// atomically (tmp file + rename) so a concurrent reader never sees a
// partially written value.
func (t *Tracker) Set(prNum int, s Step, status Status, note string) error {
	if _, ok := validStep[s]; !ok {
		return fmt.Errorf("progress: unknown step %q", string(s))
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	dir := t.prDir(prNum)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("progress: mkdir %s: %w", dir, err)
	}
	if err := writeFileAtomic(filepath.Join(dir, string(s)+".status"), []byte(string(status))); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, string(s)+".note"), []byte(note)); err != nil {
		return err
	}
	return nil
}

// Get returns the (status, note, present) triple for (prNum, step). It
// reports present=false whenever the status file is missing, even if a
// stray note file is still on disk.
func (t *Tracker) Get(prNum int, s Step) (Status, string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.getLocked(prNum, s)
}

func (t *Tracker) getLocked(prNum int, s Step) (Status, string, bool) {
	dir := t.prDir(prNum)
	statusBytes, err := os.ReadFile(filepath.Join(dir, string(s)+".status"))
	if err != nil {
		return "", "", false
	}
	noteBytes, err := os.ReadFile(filepath.Join(dir, string(s)+".note"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Status exists but note doesn't — treat the note as empty.
			return Status(strings.TrimRight(string(statusBytes), "\n")), "", true
		}
		return "", "", false
	}
	return Status(strings.TrimRight(string(statusBytes), "\n")), string(noteBytes), true
}

// MarkRemaining rewrites every step for prNum whose current status is Pending
// or Running to (status, note). Steps that are already Done, Failed, or
// Skipped are left untouched. Steps that have no on-disk entry at all are
// also left untouched (nothing to rewrite).
func (t *Tracker) MarkRemaining(prNum int, status Status, note string) error {
	t.mu.Lock()
	var toRewrite []Step
	for _, s := range allStepsCanonical {
		cur, _, ok := t.getLocked(prNum, s)
		if !ok {
			continue
		}
		if cur == Pending || cur == Running {
			toRewrite = append(toRewrite, s)
		}
	}
	t.mu.Unlock()

	for _, s := range toRewrite {
		if err := t.Set(prNum, s, status, note); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot reads every pr-<num> directory under the root and returns a
// nested map suitable for rendering. The returned map is never nil.
func (t *Tracker) Snapshot() map[int]map[Step]Entry {
	out := make(map[int]map[Step]Entry)
	t.mu.Lock()
	defer t.mu.Unlock()

	prs := t.prsLocked()
	for _, n := range prs {
		inner := make(map[Step]Entry)
		for _, s := range allStepsCanonical {
			st, note, ok := t.getLocked(n, s)
			if !ok {
				continue
			}
			inner[s] = Entry{Status: st, Note: note}
		}
		if len(inner) > 0 {
			out[n] = inner
		}
	}
	return out
}

// PRs lists all PR numbers that have a pr-<num> directory under the root,
// sorted numerically ascending. Non-numeric or malformed directories are
// ignored. A missing root directory yields an empty slice.
func (t *Tracker) PRs() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.prsLocked()
}

func (t *Tracker) prsLocked() []int {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "pr-") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(name, "pr-"))
		if err != nil || n < 0 {
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func (t *Tracker) prDir(prNum int) string {
	return filepath.Join(t.root, fmt.Sprintf("pr-%d", prNum))
}

// writeFileAtomic writes data to path atomically: write to a sibling tmp file
// in the same directory, fsync, then rename into place. The rename is atomic
// on POSIX filesystems, which means a concurrent reader either sees the old
// contents or the new contents — never a half-written file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("progress: create tmp for %s: %w", path, err)
	}
	tmpPath := f.Name()
	// Best-effort cleanup on error.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("progress: write tmp %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("progress: sync tmp %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("progress: close tmp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("progress: rename %s -> %s: %w", tmpPath, path, err)
	}
	cleanup = false
	return nil
}
