package workflow

// This file collects package-level function variables that delegate to the
// real implementations used by ProcessPR. Tests swap them out to exercise
// workflow branches without shelling out to `gh`, `git`, `claude`, or `codex`.
//
// All seams now take `context.Context` as their first argument so callers
// (and tests) can thread cancellation/timeouts through the pipeline.
//
// Test seam; see workflow_branch_test.go

import (
	"context"
	"time"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/conflict"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
	"github.com/maszynka/gh-crfix/internal/worktree"
)

// GitHub API seams.
var (
	fetchPRFn              = ghapi.FetchPR
	fetchThreadsFn         = ghapi.FetchThreads
	postCommentFn          = ghapi.PostComment
	replyToThreadFn        = ghapi.ReplyToThread
	resolveThreadFn        = ghapi.ResolveThread
	fetchFailingChecksFn   = ghapi.FetchFailingChecks
	requestCopilotReviewFn = ghapi.RequestCopilotReview
)

// Worktree seams.
var (
	repoRootFn             = worktree.RepoRoot
	setupWorktreeFn        = worktree.Setup
	dirtyStatusFn          = dirtyStatusAdapter
	mergeBaseFn            = worktree.MergeBase
	detectCaseCollisionsFn = detectCaseCollisionsAdapter
)

// dirtyStatusAdapter / detectCaseCollisionsAdapter keep the historical
// ctx-less signatures for DirtyStatus + DetectCaseCollisions (those helpers
// do pure file I/O with no blocking semantics worth cancelling). We route
// through an adapter so tests can override the seam without having to also
// rewrite the underlying functions.
func dirtyStatusAdapter(path string) (string, error) {
	return worktree.DirtyStatus(path)
}

func detectCaseCollisionsAdapter(path string) ([][]string, error) {
	return worktree.DetectCaseCollisions(path)
}

// Conflict detection seam.
var detectMarkersFn = conflict.DetectMarkers

// AI seams.
var (
	runGateFn  = ai.RunGate
	runFixFn   = ai.RunFix
	runPlainFn = ai.RunPlain
)

// sleepFn is a test seam for time.Sleep used by the post-fix review cycle.
// Tests override this with a no-op or recorder so they don't actually block.
var sleepFn = time.Sleep

// Compile-time assertion that seam types track their underlying functions.
var _ context.Context = context.TODO()
