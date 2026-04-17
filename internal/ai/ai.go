// Package ai invokes Claude or Codex for gate and fix operations.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Backend represents the AI backend to use.
type Backend int

const (
	BackendAuto   Backend = iota
	BackendClaude         // Anthropic claude CLI
	BackendCodex          // OpenAI codex CLI
)

// Default per-call exec timeouts. These bound external-CLI invocations so a
// hung claude/codex doesn't stall the entire pipeline. Overridable via env:
//   - GH_CRFIX_GATE_TIMEOUT (duration, e.g. "5m")
//   - GH_CRFIX_FIX_TIMEOUT  (duration, e.g. "15m")
const (
	defaultGateTimeout = 5 * time.Minute
	defaultFixTimeout  = 15 * time.Minute
)

// ParseBackend parses a backend string into a Backend constant.
func ParseBackend(s string) Backend {
	switch strings.ToLower(s) {
	case "claude", "anthropic":
		return BackendClaude
	case "codex", "openai":
		return BackendCodex
	default:
		return BackendAuto
	}
}

// Detect picks the first available backend.
func Detect() Backend {
	if _, err := exec.LookPath("claude"); err == nil {
		return BackendClaude
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return BackendCodex
	}
	return BackendAuto
}

// GateOutput is the structured response from the gate model.
type GateOutput struct {
	NeedsAdvancedModel bool     `json:"needs_advanced_model"`
	Reason             string   `json:"reason"`
	ThreadsToFix       []string `json:"threads_to_fix"`
}

// RunGate runs the gate model with a structured JSON schema.
// Returns the parsed gate output. Honors ctx cancellation/deadline and
// caps the call at GH_CRFIX_GATE_TIMEOUT (default 5m).
func RunGate(ctx context.Context, backend Backend, model, prompt string, schema map[string]interface{}) (GateOutput, error) {
	effective := resolveBackend(backend)

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return GateOutput{}, fmt.Errorf("marshal schema: %w", err)
	}

	// Write schema to temp file.
	sf, err := os.CreateTemp("", "gh-crfix-schema-*.json")
	if err != nil {
		return GateOutput{}, fmt.Errorf("create schema tmp: %w", err)
	}
	defer os.Remove(sf.Name())
	if _, err := sf.Write(schemaBytes); err != nil {
		sf.Close()
		return GateOutput{}, err
	}
	sf.Close()

	// Write prompt to temp file.
	pf, err := os.CreateTemp("", "gh-crfix-prompt-*.txt")
	if err != nil {
		return GateOutput{}, fmt.Errorf("create prompt tmp: %w", err)
	}
	defer os.Remove(pf.Name())
	if _, err := pf.WriteString(prompt); err != nil {
		pf.Close()
		return GateOutput{}, err
	}
	pf.Close()

	ctx, cancel := withTimeout(ctx, envDuration("GH_CRFIX_GATE_TIMEOUT", defaultGateTimeout))
	defer cancel()

	var rawOut []byte
	switch effective {
	case BackendClaude:
		rawOut, err = runClaudeStructured(ctx, model, pf.Name(), sf.Name())
	case BackendCodex:
		rawOut, err = runCodexStructured(ctx, model, pf.Name(), sf.Name())
	default:
		return GateOutput{}, fmt.Errorf("no AI backend available (install claude or codex)")
	}
	if err != nil {
		return GateOutput{}, fmt.Errorf("gate model: %w", err)
	}

	// Parse the output — claude wraps in {structured_output: ...}
	var wrapper struct {
		StructuredOutput *GateOutput `json:"structured_output"`
	}
	if jerr := json.Unmarshal(rawOut, &wrapper); jerr == nil && wrapper.StructuredOutput != nil {
		return *wrapper.StructuredOutput, nil
	}
	// Try direct parse.
	var out GateOutput
	if jerr := json.Unmarshal(rawOut, &out); jerr != nil {
		return GateOutput{}, fmt.Errorf("parse gate output: %w\nraw: %s", jerr, rawOut)
	}
	return out, nil
}

// RunFix runs the fix model in dir with filesystem access. Honors ctx
// cancellation/deadline and caps the call at GH_CRFIX_FIX_TIMEOUT (15m).
// The model is expected to make code changes and write thread-responses.json.
func RunFix(ctx context.Context, backend Backend, model, prompt, dir string) error {
	effective := resolveBackend(backend)

	pf, err := os.CreateTemp("", "gh-crfix-fix-prompt-*.txt")
	if err != nil {
		return fmt.Errorf("create prompt tmp: %w", err)
	}
	defer os.Remove(pf.Name())
	if _, err := pf.WriteString(prompt); err != nil {
		pf.Close()
		return err
	}
	pf.Close()

	ctx, cancel := withTimeout(ctx, envDuration("GH_CRFIX_FIX_TIMEOUT", defaultFixTimeout))
	defer cancel()

	switch effective {
	case BackendClaude:
		return runClaudeFix(ctx, model, pf.Name(), dir)
	case BackendCodex:
		return runCodexFix(ctx, model, pf.Name(), dir)
	default:
		return fmt.Errorf("no AI backend available (install claude or codex)")
	}
}

// RunPlain runs the fix model with filesystem access on a free-form prompt.
// Unlike RunFix it does not expect thread-responses.json — used for case
// collision normalization and committed conflict marker fixes.
func RunPlain(ctx context.Context, backend Backend, model, prompt, dir string) error {
	return RunFix(ctx, backend, model, prompt, dir)
}

func resolveBackend(b Backend) Backend {
	if b != BackendAuto {
		return b
	}
	return Detect()
}

// withTimeout wraps ctx with a timeout if it doesn't already have an earlier
// deadline. Always returns a cancel func (no-op when input ctx already has a
// deadline stricter than `d`).
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	if existing, ok := ctx.Deadline(); ok && time.Until(existing) <= d {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// envDuration reads a duration from env (e.g. "5m"), returns fallback on
// empty/invalid.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func runClaudeStructured(ctx context.Context, model, promptFile, schemaFile string) ([]byte, error) {
	schemaBytes, err := os.ReadFile(schemaFile)
	if err != nil {
		return nil, err
	}
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--model", model,
		"--output-format", "json",
		"--json-schema", string(schemaBytes),
	)
	cmd.Stdin = strings.NewReader(string(promptBytes))
	out, err := cmd.Output()
	if err != nil {
		return nil, wrapExecErr(ctx, err, "claude")
	}
	return out, nil
}

func runCodexStructured(ctx context.Context, model, promptFile, schemaFile string) ([]byte, error) {
	outFile, err := os.CreateTemp("", "gh-crfix-codex-out-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(outFile.Name())
	outFile.Close()

	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "codex", "exec",
		"--model", model,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--output-schema", schemaFile,
		"--output-last-message", outFile.Name(),
		"-",
	)
	cmd.Stdin = strings.NewReader(string(promptBytes))
	if _, err := cmd.Output(); err != nil {
		return nil, wrapExecErr(ctx, err, "codex")
	}
	return os.ReadFile(outFile.Name())
}

func runClaudeFix(ctx context.Context, model, promptFile, dir string) error {
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--model", model,
		"--dangerously-skip-permissions",
	)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(promptBytes))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return wrapExecErr(ctx, err, "claude")
	}
	return nil
}

func runCodexFix(ctx context.Context, model, promptFile, dir string) error {
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "codex", "exec",
		"--model", model,
		"--full-auto",
		"--skip-git-repo-check",
		"-",
	)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(promptBytes))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return wrapExecErr(ctx, err, "codex")
	}
	return nil
}

// wrapExecErr maps exec errors into context-aware errors so callers can use
// errors.Is(err, context.DeadlineExceeded) / context.Canceled.
func wrapExecErr(ctx context.Context, err error, bin string) error {
	if err == nil {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%s: %w", bin, cerr)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("%s: %w\n%s", bin, err, ee.Stderr)
	}
	return fmt.Errorf("%s: %w", bin, err)
}
