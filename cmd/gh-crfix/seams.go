package main

import (
	"context"
	"os"

	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/registry"
	"github.com/maszynka/gh-crfix/internal/tui"
	"github.com/maszynka/gh-crfix/internal/workflow"
)

// Test seams. Each variable points at the real implementation by default;
// tests swap them with fakes so main-level wiring can be exercised without
// spawning a Bubble Tea program or dispatching real work. Keep these
// unexported — they are an intentional internal test hook.

// Test seam: isTerminal used to decide launcher/dashboard triggers.
var isTerminalFn = isTerminal

// Test seam: runLauncher wrapper for the interactive form.
var runLauncherFn func(ctx context.Context, cfg config.Config, ml registry.ModelList) ([]string, bool) = runLauncher

// Test seam: workflow.ProcessBatch invocation in runBatchPlain.
var processBatchFn = workflow.ProcessBatch

// Test seam: tui.Run (the dashboard) invocation in runBatch.
var runDashboardFn = tui.Run

// Test seam: os.Stdout accessor for isTerminal checks so tests don't have to
// mutate the global.
var stdoutFileFn = func() *os.File { return os.Stdout }

// Test seam: os.Stderr accessor for isTerminal checks.
var stderrFileFn = func() *os.File { return os.Stderr }

// Test seam: currentRepo lookup (shells out to `gh repo view` by default).
var currentRepoFn = currentRepo
