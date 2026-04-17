package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleJSON = `{
  "updated_at": "2026-04-09T22:27:42Z",
  "anthropic": ["claude-opus-4-6-20250304", "claude-sonnet-4-6-20250514"],
  "anthropic_aliases": ["haiku","opus","sonnet","claude-sonnet-4-6"],
  "openai": ["gpt-5.4","gpt-5.4-mini","gpt-5","gpt-4.1"],
  "openai_aliases": ["gpt-4.1","gpt-5.4","o1","o3","o4"]
}`

// fixedClock returns a clock function that always yields t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newTestServer returns an httptest server that serves the given body with
// status 200 and the configured content type.
func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// newFailingServer returns a server that always 500s.
func newFailingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
}

func TestFetch_EnvOverrideTakesPriority(t *testing.T) {
	srv := newTestServer(t, sampleJSON)
	t.Cleanup(srv.Close)

	t.Setenv("GH_CRFIX_MODEL_REGISTRY", srv.URL)

	cacheDir := t.TempDir()
	// Write a bogus fresh cache to prove env overrides fresh cache.
	writeCache(t, cacheDir, `{"anthropic_aliases":["bogus"],"openai_aliases":["bogus-openai"]}`, time.Now())

	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		Clock:    fixedClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "env-override" {
		t.Fatalf("Source=%q want env-override", ml.Source)
	}
	if !containsString(ml.AnthropicAliases, "sonnet") {
		t.Fatalf("expected sonnet alias from env server, got %v", ml.AnthropicAliases)
	}
}

func TestFetch_FreshCacheUsed(t *testing.T) {
	t.Setenv("GH_CRFIX_MODEL_REGISTRY", "")
	cacheDir := t.TempDir()
	now := time.Date(2026, 4, 9, 23, 0, 0, 0, time.UTC)

	// Fresh cache: mtime 30 min ago
	cacheMtime := now.Add(-30 * time.Minute)
	writeCache(t, cacheDir, sampleJSON, cacheMtime)

	// The URL is invalid — should never be hit.
	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		URL:      "http://127.0.0.1:1/should-not-be-called",
		Clock:    fixedClock(now),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "cache" {
		t.Fatalf("Source=%q want cache", ml.Source)
	}
	if !containsString(ml.AnthropicAliases, "sonnet") {
		t.Fatalf("missing sonnet: %v", ml.AnthropicAliases)
	}
	if ml.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt should be parsed from cache")
	}
}

func TestFetch_StaleCacheTriggersHTTPAndRewrites(t *testing.T) {
	t.Setenv("GH_CRFIX_MODEL_REGISTRY", "")
	srv := newTestServer(t, sampleJSON)
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	now := time.Date(2026, 4, 9, 23, 0, 0, 0, time.UTC)

	// Stale cache: mtime 2h ago, different contents so we can tell if HTTP ran
	staleBody := `{"anthropic_aliases":["stale-haiku"],"openai_aliases":["stale-gpt"]}`
	writeCache(t, cacheDir, staleBody, now.Add(-2*time.Hour))

	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		URL:      srv.URL,
		Clock:    fixedClock(now),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "http" {
		t.Fatalf("Source=%q want http", ml.Source)
	}
	if !containsString(ml.AnthropicAliases, "sonnet") {
		t.Fatalf("expected sonnet from HTTP, got %v", ml.AnthropicAliases)
	}

	// Cache should have been rewritten.
	b, err := os.ReadFile(filepath.Join(cacheDir, "models.json"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if !strings.Contains(string(b), "sonnet") {
		t.Fatalf("cache not updated: %s", string(b))
	}
	if strings.Contains(string(b), "stale-haiku") {
		t.Fatalf("cache still contains stale data: %s", string(b))
	}
}

func TestFetch_HTTPFailureFallsThroughToLocal(t *testing.T) {
	t.Setenv("GH_CRFIX_MODEL_REGISTRY", "")
	srv := newFailingServer(t)
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir() // empty

	// Repo root with registry/models.json present
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "registry"), 0o755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(repoRoot, "registry", "models.json")
	if err := os.WriteFile(localPath, []byte(sampleJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		URL:      srv.URL,
		RepoRoot: repoRoot,
		Clock:    fixedClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "local" {
		t.Fatalf("Source=%q want local", ml.Source)
	}
	if !containsString(ml.AnthropicAliases, "sonnet") {
		t.Fatalf("expected sonnet, got %v", ml.AnthropicAliases)
	}
}

func TestFetch_AllFailBakedIn(t *testing.T) {
	t.Setenv("GH_CRFIX_MODEL_REGISTRY", "")
	srv := newFailingServer(t)
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()     // empty
	repoRoot := t.TempDir()     // no registry/models.json

	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		URL:      srv.URL,
		RepoRoot: repoRoot,
		Clock:    fixedClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "baked-in" {
		t.Fatalf("Source=%q want baked-in", ml.Source)
	}
	if len(ml.AnthropicAliases) == 0 {
		t.Fatalf("expected non-empty anthropic aliases")
	}
	if len(ml.OpenAIAliases) == 0 {
		t.Fatalf("expected non-empty openai aliases")
	}
	for _, want := range []string{"sonnet", "haiku", "opus"} {
		if !containsString(ml.AnthropicAliases, want) {
			t.Errorf("baked-in missing anthropic alias %q: %v", want, ml.AnthropicAliases)
		}
	}
	for _, want := range []string{"gpt-5.4", "gpt-5.4-mini"} {
		if !containsString(ml.OpenAIAliases, want) {
			t.Errorf("baked-in missing openai alias %q: %v", want, ml.OpenAIAliases)
		}
	}
}

func TestFetch_MalformedJSONFallsThrough(t *testing.T) {
	t.Setenv("GH_CRFIX_MODEL_REGISTRY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json {{{"))
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "registry"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "registry", "models.json"), []byte(sampleJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		URL:      srv.URL,
		RepoRoot: repoRoot,
		Clock:    fixedClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "local" {
		t.Fatalf("Source=%q want local after malformed HTTP", ml.Source)
	}
}

func TestFetch_MalformedCacheFallsThrough(t *testing.T) {
	t.Setenv("GH_CRFIX_MODEL_REGISTRY", "")
	srv := newTestServer(t, sampleJSON)
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	// Fresh mtime but garbage contents.
	writeCache(t, cacheDir, "garbage", time.Now())

	ml, err := Fetch(Options{
		CacheDir: cacheDir,
		URL:      srv.URL,
		Clock:    fixedClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ml.Source != "http" {
		t.Fatalf("Source=%q want http after malformed cache", ml.Source)
	}
}

func TestIsKnownAnthropic(t *testing.T) {
	ml := ModelList{
		Anthropic:        []string{"claude-sonnet-4-6-20250514"},
		AnthropicAliases: []string{"sonnet", "haiku"},
	}
	cases := []struct {
		in   string
		want bool
	}{
		{"sonnet", true},
		{"haiku", true},
		{"claude-sonnet-4-6-20250514", true},
		{"opus", false},
		{"gpt-5.4", false},
		{"", false},
	}
	for _, c := range cases {
		got := ml.IsKnownAnthropic(c.in)
		if got != c.want {
			t.Errorf("IsKnownAnthropic(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestIsKnownOpenAI(t *testing.T) {
	ml := ModelList{
		OpenAI:        []string{"gpt-5.4-mini"},
		OpenAIAliases: []string{"gpt-5.4", "o3"},
	}
	cases := []struct {
		in   string
		want bool
	}{
		{"gpt-5.4", true},
		{"o3", true},
		{"gpt-5.4-mini", true},
		{"sonnet", false},
		{"", false},
	}
	for _, c := range cases {
		got := ml.IsKnownOpenAI(c.in)
		if got != c.want {
			t.Errorf("IsKnownOpenAI(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestDefaultGateFix(t *testing.T) {
	ml := ModelList{}
	cases := []struct {
		backend              string
		wantGate, wantFix    string
	}{
		{"claude", "sonnet", "sonnet"},
		{"codex", "gpt-5.4-mini", "gpt-5.4"},
		{"auto", "sonnet", "sonnet"},
		{"", "sonnet", "sonnet"},
		{"unknown-thing", "sonnet", "sonnet"},
	}
	for _, c := range cases {
		if g := ml.DefaultGate(c.backend); g != c.wantGate {
			t.Errorf("DefaultGate(%q)=%q want %q", c.backend, g, c.wantGate)
		}
		if f := ml.DefaultFix(c.backend); f != c.wantFix {
			t.Errorf("DefaultFix(%q)=%q want %q", c.backend, f, c.wantFix)
		}
	}
}

// --- helpers ---

func writeCache(t *testing.T, dir, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "models.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Ensure the JSON shape the tests assume is actually parseable by stdlib
// (sanity guard so future registry JSON drift surfaces loudly).
func TestSampleJSONShape(t *testing.T) {
	var raw struct {
		UpdatedAt        string   `json:"updated_at"`
		Anthropic        []string `json:"anthropic"`
		AnthropicAliases []string `json:"anthropic_aliases"`
		OpenAI           []string `json:"openai"`
		OpenAIAliases    []string `json:"openai_aliases"`
	}
	if err := json.Unmarshal([]byte(sampleJSON), &raw); err != nil {
		t.Fatalf("sample json invalid: %v", err)
	}
	if raw.UpdatedAt == "" || len(raw.AnthropicAliases) == 0 {
		t.Fatalf("sample json missing fields: %+v", raw)
	}
}
