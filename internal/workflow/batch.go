package workflow

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/progress"
)

// BatchOptions drives multi-PR processing.
type BatchOptions struct {
	PRNums      []int
	Concurrency int
	Base        Options // one prototype; PRNum is overridden per iteration
	Out         io.Writer
}

// ProcessBatch processes every PR in opts.PRNums. It runs a setup phase up to
// SetupMaxConcurrency in parallel to prepare worktrees and classify PRs as
// ready/skipped/failed, then runs the process phase (ProcessPR) for the
// "ready" PRs up to opts.Concurrency in parallel.
//
// Returns one Result per PR, in the order given.
func ProcessBatch(ctx context.Context, opts BatchOptions) []Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Out == nil {
		opts.Out = discardWriter{}
	}

	// ── 1. Create logs.Run (reuse one from Base if the caller passed it). ──
	run := opts.Base.Run
	ownRun := false
	if run == nil {
		r, err := logs.NewRun()
		if err == nil {
			run = r
			ownRun = true
		}
	}
	if ownRun && run != nil {
		defer run.Close()
	}

	// ── 2. Init progress.Tracker at run.Dir()/progress (or reuse). ─────────
	tracker := opts.Base.Tracker
	if tracker == nil && run != nil {
		tracker = progress.NewTracker(filepath.Join(run.Dir(), "progress"))
		for _, prNum := range opts.PRNums {
			_ = tracker.Init(prNum)
		}
	}

	// Wire run/tracker into the per-PR options passed to ProcessPR.
	opts.Base.Run = run
	opts.Base.Tracker = tracker

	// ── 3. Setup phase ─────────────────────────────────────────────────────
	setupConc := opts.Base.SetupMaxConc
	if setupConc <= 0 {
		setupConc = SetupMaxConcurrency
	}
	prepared := SetupPhase(ctx, opts, run, tracker, setupConc)

	// ── 4. One-line summary of ready/skipped/failed per PR ─────────────────
	printSetupSummary(opts.Out, prepared)
	if run != nil {
		readyN, skippedN, failedN := countByStatus(prepared)
		run.Mlog("[setup-all] done -- %d ready, %d skipped, %d failed",
			readyN, skippedN, failedN)
	}

	// ── 5. Process phase: only "ready" PRs get ProcessPR'd. ────────────────
	procConc := opts.Concurrency
	if procConc <= 0 {
		procConc = 1
	}
	results := make([]Result, len(prepared))

	// Indices of PRs that are ready and should flow through ProcessPR.
	var readyIdx []int
	for i, p := range prepared {
		if p.Status == "ready" {
			readyIdx = append(readyIdx, i)
		} else {
			// Skipped/failed PRs become Result directly — no ProcessPR run.
			// Mark every remaining step terminal so the dashboard and summary
			// don't leave them stuck at "queued".
			if tracker != nil {
				terminal := progress.Skipped
				if p.Status == "failed" {
					terminal = progress.Failed
				}
				_ = tracker.MarkRemaining(p.PRNum, terminal, p.Reason)
			}
			results[i] = resultFromPrepared(p)
		}
	}

	if len(readyIdx) == 0 {
		return results
	}

	effectiveConc := procConc
	if effectiveConc > len(readyIdx) {
		effectiveConc = len(readyIdx)
	}

	if effectiveConc <= 1 {
		for _, i := range readyIdx {
			p := prepared[i]
			fmt.Fprintf(opts.Out, "── PR #%d ──────────────────────────────────────────────\n", p.PRNum)
			o := opts.Base
			o.PRNum = p.PRNum
			results[i] = ProcessPR(ctx, o)
			fmt.Fprintln(opts.Out)
		}
		return results
	}

	// Stream output through a shared locked sink, line-buffered per PR. This
	// keeps memory bounded (~1 line per goroutine) so verbose validation
	// runners can't OOM the process the way the previous bytes.Buffer per-PR
	// approach did. Lines from different PRs may interleave on the terminal,
	// but ProcessPR's `[PR #N]` prefix on every log line keeps it readable —
	// and the master log file is still the source of truth via opts.Run.
	sink := &lockedWriter{w: opts.Out}
	sem := make(chan struct{}, effectiveConc)
	var wg sync.WaitGroup
	for _, i := range readyIdx {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			p := prepared[i]
			o := opts.Base
			o.PRNum = p.PRNum
			// Per-goroutine line buffers so partial writes from validation
			// streams (e.g. byte-level output from a child process pipe) don't
			// interleave at sub-line granularity across PRs.
			outBuf := newLineBufferedWriter(sink)
			progBuf := newLineBufferedWriter(sink)
			o.Out = outBuf
			o.ProgressOut = progBuf
			// Header: emitted as a single Write so it lands contiguously
			// against the shared sink lock.
			fmt.Fprintf(sink, "── PR #%d ──────────────────────────────────────────────\n", p.PRNum)
			results[i] = ProcessPR(ctx, o)
			outBuf.Flush()
			progBuf.Flush()
			fmt.Fprintln(sink)
		}(i)
	}
	wg.Wait()
	return results
}

// resultFromPrepared converts a non-ready PreparedPR into a Result so batch
// callers see one entry per input PR.
func resultFromPrepared(p PreparedPR) Result {
	status := p.Status
	if status == "ready" {
		// Defensive: should never happen, but map to ok.
		status = "ok"
	}
	return Result{
		PRNum:    p.PRNum,
		Title:    p.Title,
		Branch:   p.HeadBranch,
		Status:   status,
		Reason:   p.Reason,
		Worktree: p.Worktree,
		Threads:  p.Threads,
	}
}

func countByStatus(ps []PreparedPR) (ready, skipped, failed int) {
	for _, p := range ps {
		switch p.Status {
		case "ready":
			ready++
		case "skipped":
			skipped++
		case "failed":
			failed++
		}
	}
	return
}

// printSetupSummary emits a compact setup-phase summary. Matches bash
// setup_all's one-line-per-PR output.
func printSetupSummary(w io.Writer, ps []PreparedPR) {
	ready, skipped, failed := countByStatus(ps)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintf(w, "  Setup — %d PR(s): %d ready, %d skipped, %d failed\n",
		len(ps), ready, skipped, failed)
	fmt.Fprintln(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	for _, p := range ps {
		icon := "?"
		switch p.Status {
		case "ready":
			icon = ">>"
		case "skipped":
			icon = "--"
		case "failed":
			icon = "!!"
		}
		title := p.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Fprintf(w, "  [%s] PR #%-5d  %s\n", icon, p.PRNum, title)
		if p.Status != "ready" && p.Reason != "" {
			fmt.Fprintf(w, "             reason: %s\n", p.Reason)
		}
	}
	fmt.Fprintln(w)
}

// PrintResults writes a summary table for a batch run to w.
func PrintResults(w io.Writer, results []Result) {
	ok, skipped, failed := 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case "ok":
			ok++
		case "skipped":
			skipped++
		default:
			failed++
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintf(w, "  Done — %d PR(s): %d fixed, %d skipped, %d failed\n",
		len(results), ok, skipped, failed)
	fmt.Fprintln(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	for _, r := range results {
		icon := "?"
		switch r.Status {
		case "ok":
			icon = "ok"
		case "skipped":
			icon = "--"
		case "failed":
			icon = "!!"
		}
		title := r.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Fprintf(w, "  [%s] PR #%-5d  %s\n", icon, r.PRNum, title)
		if r.Status != "ok" && r.Reason != "" {
			fmt.Fprintf(w, "             reason: %s\n", r.Reason)
		}
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// lockedWriter serialises Write calls so concurrent goroutines don't produce
// interleaved output lines on the terminal.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (n int, err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

// lineBufferedWriter buffers up to one line of output, then flushes complete
// lines to the wrapped sink in a single Write call. This guarantees that
// concurrent goroutines writing to the same locked sink never interleave at
// sub-line granularity, while keeping memory bounded to ~one line per
// goroutine (replaces the unbounded bytes.Buffer per-PR approach that risked
// OOMs on verbose validation runners).
type lineBufferedWriter struct {
	sink io.Writer
	buf  []byte
}

func newLineBufferedWriter(sink io.Writer) *lineBufferedWriter {
	return &lineBufferedWriter{sink: sink}
}

func (w *lineBufferedWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := w.sink.Write(w.buf[:i+1]); err != nil {
			return 0, err
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits any trailing partial line. Called once per PR after ProcessPR
// returns so that final non-newline-terminated output isn't dropped.
func (w *lineBufferedWriter) Flush() {
	if len(w.buf) == 0 {
		return
	}
	// Append a newline so the partial line is visually terminated; otherwise
	// it would butt against the next PR's header.
	w.buf = append(w.buf, '\n')
	_, _ = w.sink.Write(w.buf)
	w.buf = w.buf[:0]
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
