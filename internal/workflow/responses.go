package workflow

import (
	"os/exec"

	"github.com/maszynka/gh-crfix/internal/triage"
)

// deterministicResponses builds the deterministic (skip + already-fixed)
// ThreadResponses from a triage result. Mirrors the Bash
// `write_deterministic_responses` shape.
func deterministicResponses(skipList, alreadyFixedList []triage.Classification) []ThreadResponse {
	responses := make([]ThreadResponse, 0, len(skipList)+len(alreadyFixedList))
	for _, c := range alreadyFixedList {
		responses = append(responses, ThreadResponse{
			ThreadID: c.ThreadID,
			Action:   "already_fixed",
			Comment:  "Likely already addressed: " + c.Reason + ".",
		})
	}
	for _, c := range skipList {
		responses = append(responses, ThreadResponse{
			ThreadID:           c.ThreadID,
			Action:             "skipped",
			Comment:            "Skipped automatically: " + c.Reason + ".",
			ResolveWhenSkipped: c.ResolveWhenSkipped,
		})
	}
	return responses
}

// uncoveredResponses computes fallback "skipped" responses for threads that
// were in the triage's auto/needs_llm bucket but are not covered by any of
// the existing responses. Mirrors the Bash `write_uncovered_responses`.
func uncoveredResponses(
	autoList, needsLLMList []triage.Classification,
	existing []ThreadResponse,
	selected []string,
) []ThreadResponse {
	covered := map[string]bool{}
	for _, r := range existing {
		covered[r.ThreadID] = true
	}
	selectedSet := map[string]bool{}
	for _, s := range selected {
		selectedSet[s] = true
	}

	var out []ThreadResponse
	for _, c := range autoList {
		if covered[c.ThreadID] {
			continue
		}
		out = append(out, ThreadResponse{
			ThreadID: c.ThreadID,
			Action:   "skipped",
			Comment:  "Not auto-fixed: " + c.Reason + ". Leaving unresolved for manual follow-up.",
		})
	}
	for _, c := range needsLLMList {
		if covered[c.ThreadID] {
			continue
		}
		comment := "Automation did not select this thread for automatic code changes. Leaving unresolved for manual follow-up."
		if selectedSet[c.ThreadID] {
			comment = "Automation did not apply a code change for this thread. Leaving unresolved for manual follow-up."
		}
		out = append(out, ThreadResponse{
			ThreadID: c.ThreadID,
			Action:   "skipped",
			Comment:  comment,
		})
	}
	return out
}

// cleanupThreadResponsesArtifact removes a committed thread-responses.json
// artifact from the worktree (rm from git, commit, push). Failures are
// swallowed to match Bash semantics.
func cleanupThreadResponsesArtifact(wtPath string) {
	// `git rm -f` returns nonzero if the file isn't tracked — fall back to
	// plain filesystem remove so we never leave the artifact behind.
	rm := exec.Command("git", "-C", wtPath, "rm", "-f", "thread-responses.json")
	if err := rm.Run(); err != nil {
		_ = exec.Command("rm", "-f", wtPath+"/thread-responses.json").Run()
	}
	commit := exec.Command("git", "-C", wtPath, "commit", "-q", "-m",
		"chore: remove thread-responses.json artifact")
	_ = commit.Run()
	_ = exec.Command("git", "-C", wtPath, "push", "-q").Run()
}
