package workflow

import (
	"fmt"
	"io"
	"sync"
)

// BatchOptions drives multi-PR processing.
type BatchOptions struct {
	PRNums      []int
	Concurrency int
	Base        Options // one prototype; PRNum is overridden per iteration
	Out         io.Writer
}

// ProcessBatch processes every PR in opts.PRNums, running up to opts.Concurrency
// workers in parallel. Returns one Result per PR, in the order given.
func ProcessBatch(opts BatchOptions) []Result {
	if opts.Out == nil {
		opts.Out = discardWriter{}
	}
	n := opts.Concurrency
	if n <= 0 {
		n = 1
	}
	if n > len(opts.PRNums) {
		n = len(opts.PRNums)
	}
	if n <= 1 {
		return processSerial(opts)
	}

	results := make([]Result, len(opts.PRNums))
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i, prNum := range opts.PRNums {
		wg.Add(1)
		sem <- struct{}{}
		go func(i, prNum int) {
			defer wg.Done()
			defer func() { <-sem }()
			o := opts.Base
			o.PRNum = prNum
			results[i] = ProcessPR(o)
		}(i, prNum)
	}
	wg.Wait()
	return results
}

func processSerial(opts BatchOptions) []Result {
	results := make([]Result, len(opts.PRNums))
	for i, prNum := range opts.PRNums {
		fmt.Fprintf(opts.Out, "── PR #%d ──────────────────────────────────────────────\n", prNum)
		o := opts.Base
		o.PRNum = prNum
		results[i] = ProcessPR(o)
		fmt.Fprintln(opts.Out)
	}
	return results
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
