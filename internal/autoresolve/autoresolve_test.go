package autoresolve

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		path string
		want Side
		ok   bool
	}{
		// Lockfiles → theirs.
		{"bun.lock", TheirSide, true},
		{"bun.lockb", TheirSide, true},
		{"package-lock.json", TheirSide, true},
		{"yarn.lock", TheirSide, true},
		{"pnpm-lock.yaml", TheirSide, true},
		{"apps/psypapka/bun.lock", TheirSide, true},
		{"go.sum", TheirSide, true},
		{"Cargo.lock", TheirSide, true},
		{"poetry.lock", TheirSide, true},
		{"uv.lock", TheirSide, true},
		{"Gemfile.lock", TheirSide, true},
		{"dist/tsconfig.tsbuildinfo", TheirSide, true},
		// Changelogs → ours.
		{"CHANGELOG.md", OurSide, true},
		{"apps/psypapka/CHANGELOG.md", OurSide, true},
		{"apps/ops-dashboard/changelog.md", OurSide, true},
		{"docs/Changelog.md", OurSide, true},
		{"CHANGELOG", OurSide, true},
		// GitHub configuration → ours.
		{".github/workflows/ci.yml", OurSide, true},
		{".github/workflows/release.yaml", OurSide, true},
		{".github/docs/contributing.md", OurSide, true},
		{".github/.auto-fix-iterations", OurSide, true},
		// Artifact → ours.
		{"thread-responses.json", OurSide, true},
		// Unclassified → no side.
		{"src/main.go", "", false},
		{"README.md", "", false},
		{"package.json", "", false},
		{"a-thread-responses.json", "", false},
		{"not-a-lockfile.json", "", false},
	}
	for _, c := range cases {
		got, ok := Classify(c.path)
		if ok != c.ok || got != c.want {
			t.Errorf("Classify(%q) = (%q, %v); want (%q, %v)",
				c.path, got, ok, c.want, c.ok)
		}
	}
}

// Apply_LockfileOnly covers the token-waste scenario the user flagged:
// a conflict in a lockfile should be handled end-to-end without the LLM.
func TestApply_LockfileOnly(t *testing.T) {
	fake := &fakeGit{conflicted: []string{"bun.lock"}}
	r := &Runner{Ctx: context.Background(), WtPath: "/wt", git: fake.git, listFn: fake.list}

	got, err := r.Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Remaining) != 0 {
		t.Fatalf("Remaining=%v; want none", got.Remaining)
	}
	if got.Resolved["bun.lock"] != TheirSide {
		t.Fatalf("Resolved[bun.lock]=%q; want %q", got.Resolved["bun.lock"], TheirSide)
	}
	// Verify exactly one checkout and one add call on bun.lock.
	wantCalls := [][]string{
		{"checkout", "--theirs", "bun.lock"},
		{"add", "bun.lock"},
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Fatalf("git calls: %v\nwant: %v", fake.calls, wantCalls)
	}
}

// Apply_MixedLeavesUnhandled — real lockfile + real source conflict.
// The lockfile should be resolved deterministically, the source file should
// appear in Remaining for the caller to hand to the LLM.
func TestApply_MixedLeavesUnhandled(t *testing.T) {
	fake := &fakeGit{conflicted: []string{
		"apps/psypapka/CHANGELOG.md",
		"bun.lock",
		"src/main.go",
	}}
	r := &Runner{Ctx: context.Background(), WtPath: "/wt", git: fake.git, listFn: fake.list}

	got, err := r.Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Resolved) != 2 {
		t.Fatalf("Resolved size=%d; want 2 (CHANGELOG, bun.lock)", len(got.Resolved))
	}
	if got.Resolved["apps/psypapka/CHANGELOG.md"] != OurSide {
		t.Fatalf("CHANGELOG.md: %q; want %q", got.Resolved["apps/psypapka/CHANGELOG.md"], OurSide)
	}
	if got.Resolved["bun.lock"] != TheirSide {
		t.Fatalf("bun.lock: %q; want %q", got.Resolved["bun.lock"], TheirSide)
	}
	want := []string{"src/main.go"}
	sort.Strings(got.Remaining)
	if !reflect.DeepEqual(got.Remaining, want) {
		t.Fatalf("Remaining=%v; want %v", got.Remaining, want)
	}
}

// Apply_GitCheckoutFails — when checkout itself errors (edge case), the
// file goes to Remaining so the caller can still route it through the LLM.
func TestApply_GitCheckoutFails(t *testing.T) {
	fake := &fakeGit{
		conflicted: []string{"bun.lock"},
		fail:       map[string]bool{"checkout --theirs bun.lock": true},
	}
	r := &Runner{Ctx: context.Background(), WtPath: "/wt", git: fake.git, listFn: fake.list}

	got, err := r.Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Resolved) != 0 {
		t.Fatalf("Resolved=%v; want empty when checkout errors", got.Resolved)
	}
	if len(got.Remaining) != 1 || got.Remaining[0] != "bun.lock" {
		t.Fatalf("Remaining=%v; want [bun.lock]", got.Remaining)
	}
}

// Apply_NoConflicts is a no-op.
func TestApply_NoConflicts(t *testing.T) {
	fake := &fakeGit{}
	r := &Runner{Ctx: context.Background(), WtPath: "/wt", git: fake.git, listFn: fake.list}
	got, err := r.Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Resolved) != 0 || len(got.Remaining) != 0 {
		t.Fatalf("Apply on clean tree should be a no-op; got %+v", got)
	}
}

// CommitAndPush happy-path.
func TestCommitAndPush(t *testing.T) {
	fake := &fakeGit{}
	r := &Runner{Ctx: context.Background(), WtPath: "/wt", git: fake.git, listFn: fake.list}
	if err := r.CommitAndPush(); err != nil {
		t.Fatalf("CommitAndPush: %v", err)
	}
	want := [][]string{
		{"commit", "--no-edit"},
		{"push", "--quiet"},
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls=%v; want %v", fake.calls, want)
	}
}

func TestCommitAndPush_CommitFails(t *testing.T) {
	fake := &fakeGit{fail: map[string]bool{"commit --no-edit": true}}
	r := &Runner{Ctx: context.Background(), WtPath: "/wt", git: fake.git, listFn: fake.list}
	if err := r.CommitAndPush(); err == nil {
		t.Fatal("CommitAndPush should propagate commit failure")
	}
}

// --- tiny in-memory fake ----------------------------------------------------

type fakeGit struct {
	conflicted []string
	fail       map[string]bool
	calls      [][]string
}

func (f *fakeGit) list(ctx context.Context, wtPath string) ([]string, error) {
	return f.conflicted, nil
}

func (f *fakeGit) git(ctx context.Context, wtPath string, args ...string) error {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := ""
	for i, a := range args {
		if i > 0 {
			key += " "
		}
		key += a
	}
	if f.fail[key] {
		return errors.New("simulated failure for " + key)
	}
	return nil
}
