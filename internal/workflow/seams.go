package workflow

// This file collects package-level function variables that delegate to the
// real implementations used by ProcessPR. Tests swap them out to exercise
// workflow branches without shelling out to `gh`, `git`, `claude`, or `codex`.
//
// Test seam; see workflow_branch_test.go

import (
	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/conflict"
	ghapi "github.com/maszynka/gh-crfix/internal/github"
	"github.com/maszynka/gh-crfix/internal/worktree"
)

// GitHub API seams.
var (
	fetchPRFn             = ghapi.FetchPR
	fetchThreadsFn        = ghapi.FetchThreads
	postCommentFn         = ghapi.PostComment
	replyToThreadFn       = ghapi.ReplyToThread
	resolveThreadFn       = ghapi.ResolveThread
	fetchFailingChecksFn  = ghapi.FetchFailingChecks
	requestCopilotReviewFn = ghapi.RequestCopilotReview
)

// Worktree seams.
var (
	repoRootFn              = worktree.RepoRoot
	setupWorktreeFn         = worktree.Setup
	dirtyStatusFn           = worktree.DirtyStatus
	mergeBaseFn             = worktree.MergeBase
	detectCaseCollisionsFn  = worktree.DetectCaseCollisions
)

// Conflict detection seam.
var detectMarkersFn = conflict.DetectMarkers

// AI seams.
var (
	runGateFn  = ai.RunGate
	runFixFn   = ai.RunFix
	runPlainFn = ai.RunPlain
)
