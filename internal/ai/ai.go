// Package ai invokes Claude or Codex for gate and fix operations.
package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Backend represents the AI backend to use.
type Backend int

const (
	BackendAuto   Backend = iota
	BackendClaude         // Anthropic claude CLI
	BackendCodex          // OpenAI codex CLI
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
// Returns the parsed gate output.
func RunGate(backend Backend, model, prompt string, schema map[string]interface{}) (GateOutput, error) {
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

	var rawOut []byte
	switch effective {
	case BackendClaude:
		rawOut, err = runClaudeStructured(model, pf.Name(), sf.Name())
	case BackendCodex:
		rawOut, err = runCodexStructured(model, pf.Name(), sf.Name())
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

// RunFix runs the fix model in dir with filesystem access.
// The model is expected to make code changes and write thread-responses.json.
func RunFix(backend Backend, model, prompt, dir string) error {
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

	switch effective {
	case BackendClaude:
		return runClaudeFix(model, pf.Name(), dir)
	case BackendCodex:
		return runCodexFix(model, pf.Name(), dir)
	default:
		return fmt.Errorf("no AI backend available (install claude or codex)")
	}
}

func resolveBackend(b Backend) Backend {
	if b != BackendAuto {
		return b
	}
	return Detect()
}

func runClaudeStructured(model, promptFile, schemaFile string) ([]byte, error) {
	schemaBytes, err := os.ReadFile(schemaFile)
	if err != nil {
		return nil, err
	}
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("claude", "-p",
		"--model", model,
		"--output-format", "json",
		"--json-schema", string(schemaBytes),
	)
	cmd.Stdin = strings.NewReader(string(promptBytes))
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("claude: %w\n%s", err, ee.Stderr)
		}
		return nil, err
	}
	return out, nil
}

func runCodexStructured(model, promptFile, schemaFile string) ([]byte, error) {
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
	cmd := exec.Command("codex", "exec",
		"--model", model,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--output-schema", schemaFile,
		"--output-last-message", outFile.Name(),
		"-",
	)
	cmd.Stdin = strings.NewReader(string(promptBytes))
	if out, err := cmd.Output(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("codex: %w\n%s\n%s", err, ee.Stderr, out)
		}
		return nil, err
	}
	return os.ReadFile(outFile.Name())
}

func runClaudeFix(model, promptFile, dir string) error {
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return err
	}
	cmd := exec.Command("claude", "-p",
		"--model", model,
		"--dangerously-skip-permissions",
	)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(promptBytes))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runCodexFix(model, promptFile, dir string) error {
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return err
	}
	cmd := exec.Command("codex", "exec",
		"--model", model,
		"--full-auto",
		"--skip-git-repo-check",
		"-",
	)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(promptBytes))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
