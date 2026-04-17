// Package registry provides the list of Claude/OpenAI models gh-crfix
// knows about.
//
// The launcher and the backend auto-resolution code call Fetch to learn
// which aliases are valid and which default gate/fix model to use for a
// given backend. The resolution order (env override, fresh cache, HTTP
// fetch, repo-local fallback, baked-in defaults) mirrors the bash
// implementation and is documented in docs/registry.md.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultURL is the canonical location of the registry JSON. It is used
// when no explicit URL is provided via Options or the environment.
const DefaultURL = "https://raw.githubusercontent.com/maszynka/gh-crfix/main/registry/models.json"

// EnvURL is the environment variable users set to point gh-crfix at a
// custom registry URL (useful for staging, forks, or offline mirrors).
const EnvURL = "GH_CRFIX_MODEL_REGISTRY"

// cacheTTL is how long a cached registry JSON is considered fresh
// enough to skip HTTP. Matches the bash 1h TTL.
const cacheTTL = time.Hour

// httpTimeout is the total budget for an HTTP fetch. On timeout we fall
// through to the cache / local / baked-in chain.
const httpTimeout = 5 * time.Second

// cacheFileName is the basename used inside the cache dir for the
// downloaded registry JSON.
const cacheFileName = "models.json"

// ModelList is the decoded registry, plus metadata about where it came
// from. Callers use IsKnownAnthropic / IsKnownOpenAI to validate a
// user-supplied model string and DefaultGate / DefaultFix to pick
// defaults for a backend.
type ModelList struct {
	UpdatedAt        time.Time
	Anthropic        []string
	AnthropicAliases []string
	OpenAI           []string
	OpenAIAliases    []string
	// Source describes where the data came from: one of
	// "env-override", "cache", "http", "local", "baked-in".
	Source string
}

// Options controls how Fetch resolves the registry. All fields are
// optional; zero values produce production defaults.
type Options struct {
	// HTTPClient is used for all network requests. Defaults to an
	// http.Client with a 5s total timeout.
	HTTPClient *http.Client
	// CacheDir is where the downloaded registry is cached. Defaults to
	// $XDG_CACHE_HOME/gh-crfix or ~/.cache/gh-crfix.
	CacheDir string
	// Clock lets tests freeze time. Defaults to time.Now.
	Clock func() time.Time
	// URL overrides the registry URL. If empty, Fetch reads
	// GH_CRFIX_MODEL_REGISTRY, then falls back to DefaultURL.
	URL string
	// RepoRoot is the directory checked for a repo-local
	// registry/models.json when HTTP fails. Defaults to ".".
	RepoRoot string
}

// jsonShape is the on-wire JSON structure of the registry file.
type jsonShape struct {
	UpdatedAt        string   `json:"updated_at"`
	Anthropic        []string `json:"anthropic"`
	AnthropicAliases []string `json:"anthropic_aliases"`
	OpenAI           []string `json:"openai"`
	OpenAIAliases    []string `json:"openai_aliases"`
}

// bakedInAnthropicAliases is the last-resort list of Claude aliases.
// It must stay in sync with the bash default list used when the
// registry is unreachable.
var bakedInAnthropicAliases = []string{
	"haiku",
	"sonnet",
	"opus",
	"claude-haiku-4-5",
	"claude-sonnet-4-5",
	"claude-sonnet-4-6",
	"claude-opus-4-6",
}

// bakedInOpenAIAliases is the last-resort list of OpenAI aliases.
var bakedInOpenAIAliases = []string{
	"gpt-5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-4.1",
	"o1",
	"o3",
	"o4-mini",
}

// Fetch resolves the registry using the documented 5-step chain and
// returns the first source that decodes cleanly. It never returns an
// error in practice because the baked-in list is always available; the
// error return exists for forward compatibility.
func Fetch(opts Options) (ModelList, error) {
	opts = withDefaults(opts)

	// Step 1: explicit env override.
	if env := os.Getenv(EnvURL); env != "" {
		if ml, ok := fetchHTTP(opts.HTTPClient, env); ok {
			ml.Source = "env-override"
			// Env-override intentionally does not touch the cache —
			// the user is pointing at a non-default URL and we do not
			// want to pollute the default cache slot with it.
			return ml, nil
		}
		// Env override failed; continue down the chain so the user
		// is not left without any model list.
	}

	// Step 2: fresh cache.
	cachePath := filepath.Join(opts.CacheDir, cacheFileName)
	if ml, ok := readFreshCache(cachePath, opts.Clock()); ok {
		ml.Source = "cache"
		return ml, nil
	}

	// Step 3: HTTP from the configured URL.
	url := opts.URL
	if url == "" {
		url = os.Getenv(EnvURL)
	}
	if url == "" {
		url = DefaultURL
	}
	if ml, ok := fetchHTTP(opts.HTTPClient, url); ok {
		ml.Source = "http"
		// Best-effort cache write; ignore errors so a read-only cache
		// dir does not break the launcher.
		_ = writeCacheAtomic(cachePath, ml)
		return ml, nil
	}

	// Step 4: repo-local fallback.
	localPath := filepath.Join(opts.RepoRoot, "registry", cacheFileName)
	if ml, ok := readLocal(localPath); ok {
		ml.Source = "local"
		return ml, nil
	}

	// Step 5: baked-in defaults.
	return ModelList{
		AnthropicAliases: append([]string(nil), bakedInAnthropicAliases...),
		OpenAIAliases:    append([]string(nil), bakedInOpenAIAliases...),
		Source:           "baked-in",
	}, nil
}

// withDefaults fills in the production defaults for any zero-value
// field in opts.
func withDefaults(opts Options) Options {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.CacheDir == "" {
		opts.CacheDir = defaultCacheDir()
	}
	if opts.RepoRoot == "" {
		opts.RepoRoot = "."
	}
	return opts
}

// defaultCacheDir resolves the canonical gh-crfix cache directory,
// preferring XDG_CACHE_HOME and falling back to ~/.cache/gh-crfix. If
// even that cannot be determined we return "" so the caller skips the
// cache step rather than writing into an unexpected place.
func defaultCacheDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "gh-crfix")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "gh-crfix")
	}
	return ""
}

// fetchHTTP GETs url and decodes the body. Returns ok=false on any
// network, status, or JSON error so the caller can fall through.
func fetchHTTP(client *http.Client, url string) (ModelList, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ModelList{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return ModelList{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ModelList{}, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ModelList{}, false
	}
	ml, err := decode(body)
	if err != nil {
		return ModelList{}, false
	}
	return ml, true
}

// readFreshCache returns the cached registry if the file exists and its
// mtime is within cacheTTL of now. Any error (missing file, stale file,
// malformed JSON) yields ok=false.
func readFreshCache(path string, now time.Time) (ModelList, bool) {
	if path == "" {
		return ModelList{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return ModelList{}, false
	}
	if now.Sub(info.ModTime()) >= cacheTTL {
		return ModelList{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ModelList{}, false
	}
	ml, err := decode(b)
	if err != nil {
		return ModelList{}, false
	}
	return ml, true
}

// readLocal loads a repo-local registry file, used as the last
// on-disk fallback before baked-in defaults.
func readLocal(path string) (ModelList, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ModelList{}, false
	}
	ml, err := decode(b)
	if err != nil {
		return ModelList{}, false
	}
	return ml, true
}

// decode parses a registry JSON payload into a ModelList. It tolerates
// a missing or unparseable updated_at (leaves UpdatedAt as zero) but
// propagates structural JSON errors so the caller can fall through.
func decode(b []byte) (ModelList, error) {
	var raw jsonShape
	if err := json.Unmarshal(b, &raw); err != nil {
		return ModelList{}, err
	}
	ml := ModelList{
		Anthropic:        raw.Anthropic,
		AnthropicAliases: raw.AnthropicAliases,
		OpenAI:           raw.OpenAI,
		OpenAIAliases:    raw.OpenAIAliases,
	}
	if raw.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, raw.UpdatedAt); err == nil {
			ml.UpdatedAt = t
		}
	}
	// Require at least one alias list to be populated — a payload
	// with no aliases is not a valid registry and we should fall
	// through rather than hand the caller an empty list that looks
	// like it came from the network.
	if len(ml.AnthropicAliases) == 0 && len(ml.OpenAIAliases) == 0 {
		return ModelList{}, errors.New("registry: empty alias lists")
	}
	return ml, nil
}

// writeCacheAtomic writes the registry to path using a tmp-file +
// rename so readers never see a half-written file.
func writeCacheAtomic(path string, ml ModelList) error {
	if path == "" {
		return errors.New("registry: empty cache path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload := jsonShape{
		Anthropic:        ml.Anthropic,
		AnthropicAliases: ml.AnthropicAliases,
		OpenAI:           ml.OpenAI,
		OpenAIAliases:    ml.OpenAIAliases,
	}
	if !ml.UpdatedAt.IsZero() {
		payload.UpdatedAt = ml.UpdatedAt.UTC().Format(time.RFC3339)
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".models.json.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Make sure a failed write does not leave a stray temp file around.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// IsKnownAnthropic reports whether model matches any Anthropic full ID
// or alias in the registry. Empty input is always unknown.
func (m ModelList) IsKnownAnthropic(model string) bool {
	if model == "" {
		return false
	}
	return containsExact(m.Anthropic, model) || containsExact(m.AnthropicAliases, model)
}

// IsKnownOpenAI reports whether model matches any OpenAI full ID or
// alias in the registry. Empty input is always unknown.
func (m ModelList) IsKnownOpenAI(model string) bool {
	if model == "" {
		return false
	}
	return containsExact(m.OpenAI, model) || containsExact(m.OpenAIAliases, model)
}

// DefaultGate returns the default gate model for backend. Mirrors the
// bash default_gate_model_for_backend helper: claude -> sonnet, codex
// -> gpt-5.4-mini, auto/unknown -> sonnet.
func (m ModelList) DefaultGate(backend string) string {
	switch backend {
	case "codex":
		return "gpt-5.4-mini"
	default:
		return "sonnet"
	}
}

// DefaultFix returns the default fix model for backend. Mirrors the
// bash default_fix_model_for_backend helper: claude -> sonnet, codex
// -> gpt-5.4, auto/unknown -> sonnet.
func (m ModelList) DefaultFix(backend string) string {
	switch backend {
	case "codex":
		return "gpt-5.4"
	default:
		return "sonnet"
	}
}

func containsExact(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
