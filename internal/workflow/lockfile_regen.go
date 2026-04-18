package workflow

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/maszynka/gh-crfix/internal/autoresolve"
	"github.com/maszynka/gh-crfix/internal/triage"
)

// regenerateLockfileThreads intercepts review threads pointed at lockfiles
// BEFORE the gate model sees them. The fix is always `<pm> install`, a
// deterministic operation — forwarding the thread to the fix model would
// either burn tokens re-discovering this OR (worse) tempt the model into
// hand-editing a 10k-line lockfile.
//
// For each lockfile thread we:
//  1. Identify the package-manager family by file basename.
//  2. Run `<pm> install` (or equivalent) at wtPath. Missing PM binary →
//     leave the thread for the LLM to handle.
//  3. If the install actually changed the lockfile: stage, commit, push.
//  4. Emit a `fixed` ThreadResponse so the later reply+resolve step marks
//     the thread resolved with a succinct explanation.
//  5. Remove the thread from the pools the gate will receive.
//
// Pools are pointer-modified in place. Returns the generated responses and
// the number of threads handled.
func regenerateLockfileThreads(
	ctx context.Context,
	opts Options,
	wtPath string,
	needsLLM *[]triage.Classification,
	auto *[]triage.Classification,
	log func(string, ...interface{}),
) (responses []ThreadResponse, handled int) {

	// Group threads by lockfile kind so we run each install at most once.
	type group struct {
		kind     autoresolve.LockfileKind
		threads  []triage.Classification
	}
	groups := map[autoresolve.LockfileKind]*group{}

	// Returns the pool filtered to only the threads NOT picked up for
	// deterministic handling, plus the picks.
	filter := func(pool []triage.Classification) (kept []triage.Classification, picked []triage.Classification) {
		for _, c := range pool {
			kind := autoresolve.DetectLockfile(c.Path)
			if kind == autoresolve.NotALockfile {
				kept = append(kept, c)
				continue
			}
			if groups[kind] == nil {
				groups[kind] = &group{kind: kind}
			}
			groups[kind].threads = append(groups[kind].threads, c)
			picked = append(picked, c)
		}
		return
	}

	newNeedsLLM, pickedLLM := filter(*needsLLM)
	newAuto, pickedAuto := filter(*auto)
	if len(pickedLLM)+len(pickedAuto) == 0 {
		return nil, 0
	}

	regen := lockfileRegeneratorFn(wtPath)

	for kind, g := range groups {
		bin, _, _ := kind.InstallCommand()
		log("pre-gate: regenerating %s lockfile via `%s install` (%d thread(s))",
			kind, bin, len(g.threads))

		// Note the lockfile content BEFORE install so we can detect whether
		// the run actually made a change; skipping commit on a no-op keeps
		// the PR history clean.
		preSHA := gitIndexSHA(ctx, wtPath, g.threads[0].Path)

		err := regen.Regenerate(ctx, kind)
		if err != nil {
			// PM missing → leave the threads for the LLM to try; any other
			// failure (conflict in lockfile, network, etc.) also falls back.
			log("pre-gate: %s regen failed: %v — threads sent to fix-model", kind, err)
			if errors.Is(err, autoresolve.ErrPMMissing) {
				// no pool rewrite — send them back to the original lists.
				if len(pickedLLM) > 0 && containsAnyOfKind(pickedLLM, kind) {
					newNeedsLLM = append(newNeedsLLM, threadsOfKind(pickedLLM, kind)...)
				}
				if len(pickedAuto) > 0 && containsAnyOfKind(pickedAuto, kind) {
					newAuto = append(newAuto, threadsOfKind(pickedAuto, kind)...)
				}
			} else {
				// Non-PMMissing failure — ask LLM anyway; leave the threads
				// in the LLM pool.
				newNeedsLLM = append(newNeedsLLM, threadsOfKind(pickedLLM, kind)...)
				newNeedsLLM = append(newNeedsLLM, threadsOfKind(pickedAuto, kind)...)
			}
			continue
		}

		postSHA := gitIndexSHA(ctx, wtPath, g.threads[0].Path)
		changed := preSHA != postSHA

		if changed {
			// Stage, commit, push. Failures here are logged but don't block
			// the thread from being marked fixed — the working tree is
			// already consistent, the user can push manually if needed.
			if cerr := commitAndPushLockfile(ctx, wtPath, kind); cerr != nil {
				log("pre-gate: %s regen succeeded but commit/push failed: %v", kind, cerr)
			} else {
				log("pre-gate: %s regen committed and pushed", kind)
			}
		} else {
			log("pre-gate: %s regen produced no change — already in sync", kind)
		}

		for _, t := range g.threads {
			action := "fixed"
			comment := fmt.Sprintf(
				"Regenerated via `%s install` (deterministic fix — no AI model invoked).",
				kind)
			if !changed {
				action = "already_fixed"
				comment = fmt.Sprintf(
					"No change after `%s install` — lockfile is already in sync with the manifest.",
					kind)
			}
			responses = append(responses, ThreadResponse{
				ThreadID: t.ThreadID,
				Action:   action,
				Comment:  comment,
			})
			handled++
		}
	}

	*needsLLM = newNeedsLLM
	*auto = newAuto
	return responses, handled
}

// containsAnyOfKind reports whether any classification in pool points at a
// lockfile of the given kind.
func containsAnyOfKind(pool []triage.Classification, kind autoresolve.LockfileKind) bool {
	for _, c := range pool {
		if autoresolve.DetectLockfile(c.Path) == kind {
			return true
		}
	}
	return false
}

// threadsOfKind returns the classifications from pool whose path matches the
// given lockfile kind.
func threadsOfKind(pool []triage.Classification, kind autoresolve.LockfileKind) []triage.Classification {
	var out []triage.Classification
	for _, c := range pool {
		if autoresolve.DetectLockfile(c.Path) == kind {
			out = append(out, c)
		}
	}
	return out
}

// gitIndexSHA returns the blob SHA git sees for path in wtPath, or "" if
// git can't read it (file missing, not tracked, etc.). Used to cheaply
// detect whether `<pm> install` actually changed the lockfile.
func gitIndexSHA(ctx context.Context, wtPath, path string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", wtPath, "hash-object", path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// hash-object outputs exactly one SHA + newline.
	for i, b := range out {
		if b == '\n' {
			return string(out[:i])
		}
	}
	return string(out)
}

func commitAndPushLockfile(ctx context.Context, wtPath string, kind autoresolve.LockfileKind) error {
	if err := runGit(ctx, wtPath, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := runGit(ctx, wtPath, "commit", "-m",
		fmt.Sprintf("chore: regenerate %s lockfile", kind),
	); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if err := runGit(ctx, wtPath, "push", "--quiet"); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

func runGit(ctx context.Context, wtPath string, args ...string) error {
	full := append([]string{"-C", wtPath}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
