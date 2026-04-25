// Package github wraps the gh CLI for all GitHub API operations.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// defaultGHTimeout bounds every gh CLI invocation so a hung network/API
// call doesn't block the pipeline. Overridable via GH_CRFIX_GH_TIMEOUT.
const defaultGHTimeout = 2 * time.Minute

// PRInfo is basic metadata about a pull request.
type PRInfo struct {
	HeadRefName    string
	Title          string
	State          string
	IsDraft        bool
	HeadSHA        string
	BaseRefName    string
	MergeableState string // "MERGEABLE" | "CONFLICTING" | "UNKNOWN"
}

// Comment is a single comment within a review thread.
type Comment struct {
	ID           string
	Body         string
	Path         string
	Line         int
	OriginalLine int
	Author       string
	CreatedAt    string
}

// Thread is a review thread on a PR.
type Thread struct {
	ID         string
	IsResolved bool
	IsOutdated bool
	Path       string
	Line       int
	Comments   []Comment
}

// CICheck is a failing CI check with an optional log snippet.
type CICheck struct {
	Name    string
	LogText string
}

// FetchPR returns basic info about a pull request.
func FetchPR(ctx context.Context, repo string, prNum int) (PRInfo, error) {
	out, err := gh(ctx, "pr", "view", fmt.Sprintf("%d", prNum), "--repo", repo,
		"--json", "headRefName,baseRefName,title,state,isDraft,headRefOid,mergeable")
	if err != nil {
		return PRInfo{}, err
	}
	var raw struct {
		HeadRefName string `json:"headRefName"`
		BaseRefName string `json:"baseRefName"`
		Title       string `json:"title"`
		State       string `json:"state"`
		IsDraft     bool   `json:"isDraft"`
		HeadRefOid  string `json:"headRefOid"`
		Mergeable   string `json:"mergeable"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return PRInfo{}, fmt.Errorf("parse pr: %w", err)
	}
	return PRInfo{
		HeadRefName:    raw.HeadRefName,
		BaseRefName:    raw.BaseRefName,
		Title:          raw.Title,
		State:          raw.State,
		IsDraft:        raw.IsDraft,
		HeadSHA:        raw.HeadRefOid,
		MergeableState: raw.Mergeable,
	}, nil
}

// FetchThreads returns unresolved review threads for a PR.
func FetchThreads(ctx context.Context, repo string, prNum, maxThreads int) ([]Thread, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo %q", repo)
	}
	owner, repoName := parts[0], parts[1]

	const query = `query($owner: String!, $repo: String!, $prNum: Int!, $maxT: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $prNum) {
      reviewThreads(last: $maxT) {
        nodes {
          id isResolved isOutdated line path
          comments(first: 50) {
            nodes {
              id body path line originalLine
              author { login }
              createdAt
            }
          }
        }
      }
    }
  }
}`

	out, err := gh(ctx, "api", "graphql",
		"-f", "query="+query,
		"-F", "owner="+owner,
		"-F", "repo="+repoName,
		"-F", fmt.Sprintf("prNum=%d", prNum),
		"-F", fmt.Sprintf("maxT=%d", maxThreads),
	)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							IsOutdated bool   `json:"isOutdated"`
							Line       int    `json:"line"`
							Path       string `json:"path"`
							Comments   struct {
								Nodes []struct {
									ID           string      `json:"id"`
									Body         string      `json:"body"`
									Path         string      `json:"path"`
									Line         int         `json:"line"`
									OriginalLine int         `json:"originalLine"`
									Author       struct{ Login string } `json:"author"`
									CreatedAt    string      `json:"createdAt"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse threads: %w", err)
	}

	nodes := resp.Data.Repository.PullRequest.ReviewThreads.Nodes
	threads := make([]Thread, 0, len(nodes))
	for _, n := range nodes {
		if n.IsResolved {
			continue
		}
		t := Thread{
			ID:         n.ID,
			IsResolved: n.IsResolved,
			IsOutdated: n.IsOutdated,
			Path:       n.Path,
			Line:       n.Line,
		}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, Comment{
				ID:           c.ID,
				Body:         c.Body,
				Path:         c.Path,
				Line:         c.Line,
				OriginalLine: c.OriginalLine,
				Author:       c.Author.Login,
				CreatedAt:    c.CreatedAt,
			})
		}
		threads = append(threads, t)
	}
	return threads, nil
}

// ReplyToThread posts a reply on the given review thread.
func ReplyToThread(ctx context.Context, threadID, body string) error {
	const query = `mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $threadId, body: $body}) {
    comment { id }
  }
}`
	_, err := gh(ctx, "api", "graphql",
		"-f", "query="+query,
		"-f", "threadId="+threadID,
		"-f", "body="+body,
	)
	return err
}

// ResolveThread marks a review thread as resolved.
func ResolveThread(ctx context.Context, threadID string) error {
	const query = `mutation($threadId: ID!) {
  resolveReviewThread(input: {threadId: $threadId}) {
    thread { id }
  }
}`
	_, err := gh(ctx, "api", "graphql",
		"-f", "query="+query,
		"-f", "threadId="+threadID,
	)
	return err
}

// PostComment posts a comment on the PR.
func PostComment(ctx context.Context, repo string, prNum int, body string) error {
	_, err := gh(ctx, "pr", "comment", fmt.Sprintf("%d", prNum), "--repo", repo, "--body", body)
	return err
}

// RequestCopilotReview requests a Copilot re-review.
func RequestCopilotReview(ctx context.Context, repo string, prNum int) error {
	_, err := gh(ctx, "api", "--method", "POST",
		fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, prNum),
		"-f", "reviewers[]=Copilot",
	)
	return err
}

var runIDRe = regexp.MustCompile(`/runs/([0-9]+)`)

// FetchFailingChecks returns failing CI checks with log snippets.
//
// API failures are surfaced as a non-nil error (wrapping the underlying
// `gh api` error) so the caller can log a visible warning. Callers that
// treat CI context as best-effort should log+continue rather than abort —
// CI surfacing is a "make the gate/fix model more informed" feature, not
// a hard requirement, but silent failures here have masked regressions in
// the past, so the error must reach the master log.
func FetchFailingChecks(ctx context.Context, repo, headSHA string) ([]CICheck, error) {
	out, err := gh(ctx, "api",
		fmt.Sprintf("repos/%s/commits/%s/check-runs", repo, headSHA),
		"--paginate",
		"--jq", `.check_runs[] | select(.conclusion == "failure") | {name, details_url}`,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch CI checks: %w", err)
	}

	var checks []CICheck
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			Name       string `json:"name"`
			DetailsURL string `json:"details_url"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		logText := fetchRunLog(ctx, repo, item.DetailsURL)
		checks = append(checks, CICheck{
			Name:    item.Name,
			LogText: logText,
		})
	}
	return checks, nil
}

func fetchRunLog(ctx context.Context, repo, detailsURL string) string {
	m := runIDRe.FindStringSubmatch(detailsURL)
	if len(m) < 2 {
		return ""
	}
	out, err := gh(ctx, "run", "view", m[1], "--repo", repo, "--log-failed")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) > 80 {
		lines = lines[:80]
	}
	return strings.Join(lines, "\n")
}

// gh runs the gh CLI and returns stdout. Every invocation is bounded by
// GH_CRFIX_GH_TIMEOUT (default 2 minutes) so a hung network call cannot
// stall the whole pipeline.
func gh(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := withGHTimeout(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.Output()
	if err != nil {
		preview := strings.Join(args[:min(3, len(args))], " ")
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("gh %s: %w", preview, cerr)
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", preview, err, ee.Stderr)
		}
		return nil, fmt.Errorf("gh %s: %w", preview, err)
	}
	return out, nil
}

func withGHTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d := defaultGHTimeout
	if v := os.Getenv("GH_CRFIX_GH_TIMEOUT"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			d = parsed
		}
	}
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	if existing, ok := ctx.Deadline(); ok && time.Until(existing) <= d {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
