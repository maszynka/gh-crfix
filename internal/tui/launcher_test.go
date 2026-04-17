package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/registry"
)

// stubModels returns a ModelList suitable for tests — a mix of anthropic and
// openai aliases so backend-switching can be exercised.
func stubModels() registry.ModelList {
	return registry.ModelList{
		AnthropicAliases: []string{"sonnet", "opus", "haiku"},
		OpenAIAliases:    []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5"},
		Source:           "test",
	}
}

func newTestModel(t *testing.T, cfg LauncherConfig) *launcherModel {
	t.Helper()
	m := newLauncherModel(cfg)
	// Trigger Init — some models resolve size via Init / WindowSizeMsg.
	_ = m.Init()
	return m
}

// keyMsg builds a tea.KeyMsg for the named key. It supports a few special
// names (up, down, left, right, enter, esc, backspace, ctrl+c) and falls back
// to treating the string as a literal rune-run.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// send runs Update with a sequence of keys, returning the final updated model.
func send(t *testing.T, m *launcherModel, keys ...string) *launcherModel {
	t.Helper()
	var model tea.Model = m
	for _, k := range keys {
		var msg tea.Msg
		if len(k) == 1 {
			// Single literal character — send each as a KeyRunes.
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		} else {
			msg = keyMsg(k)
		}
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		_ = cmd
	}
	return model.(*launcherModel)
}

func TestLauncher_InitialValuesFromDefaults(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	if got := m.targetValue(); got != "" {
		t.Errorf("Target = %q, want empty", got)
	}
	if got := m.backendValue(); got != "auto" {
		t.Errorf("Backend = %q, want auto", got)
	}
	if got := m.gateModelValue(); got != "sonnet" {
		t.Errorf("GateModel = %q, want sonnet", got)
	}
	if got := m.fixModelValue(); got != "sonnet" {
		t.Errorf("FixModel = %q, want sonnet", got)
	}
	if got := m.concurrencyValue(); got != 3 {
		t.Errorf("Concurrency = %d, want 3", got)
	}
	if got := m.scoreNeedsLLMValue(); got != 1.0 {
		t.Errorf("ScoreNeedsLLM = %v, want 1.0", got)
	}
	if got := m.scorePRCommentValue(); got != 0.4 {
		t.Errorf("ScorePRComment = %v, want 0.4", got)
	}
	if got := m.scoreTestFailureValue(); got != 1.0 {
		t.Errorf("ScoreTestFailure = %v, want 1.0", got)
	}
}

func TestLauncher_DownDownRightCyclesConcurrency(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	// Target(0) -> Backend(1) -> Gate(2) -> Fix(3) -> Concurrency(4).
	// The task spec lists "↓↓ then →" but fields go Target, Backend, Gate, Fix,
	// Concurrency — so we need 4 down-presses to reach concurrency. Keep the
	// intent (move down twice past text fields, then cycle right).
	// Easier: move to concurrency explicitly by pressing down 4 times.
	m = send(t, m, "down", "down", "down", "down")
	before := m.concurrencyValue()
	m = send(t, m, "right")
	after := m.concurrencyValue()

	if after <= before {
		t.Errorf("right on concurrency %d -> %d; want larger", before, after)
	}
}

func TestLauncher_UpReturnsToPreviousField(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "down")
	if m.selected != 1 {
		t.Fatalf("after down, selected = %d, want 1", m.selected)
	}
	m = send(t, m, "up")
	if m.selected != 0 {
		t.Errorf("after up, selected = %d, want 0", m.selected)
	}
}

func TestLauncher_TypingAppendsToTarget(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "1", "2", "3")
	if got := m.targetValue(); got != "123" {
		t.Errorf("Target = %q, want %q", got, "123")
	}
}

func TestLauncher_BackspaceRemovesFromTarget(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "a", "b", "c", "backspace")
	if got := m.targetValue(); got != "ab" {
		t.Errorf("Target after backspace = %q, want %q", got, "ab")
	}
}

func TestLauncher_SubmitEmptyTargetShowsError(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "enter")

	if m.result.Submitted {
		t.Error("empty-target enter should not set Submitted=true")
	}
	if m.errMsg == "" {
		t.Error("empty-target enter should set an error message")
	}
	if !strings.Contains(strings.ToLower(m.errMsg), "target") {
		t.Errorf("error %q should mention target", m.errMsg)
	}
	// Still rendering the form; quit should not be set.
	if m.quitting {
		t.Error("empty-target enter should not quit the model")
	}
}

func TestLauncher_SubmitValidPopulatesResult(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "1", "2", "3")
	m = send(t, m, "enter")

	if !m.result.Submitted {
		t.Fatalf("expected Submitted=true, got result=%+v errMsg=%q", m.result, m.errMsg)
	}
	if m.result.Target != "123" {
		t.Errorf("result.Target = %q, want 123", m.result.Target)
	}
	if m.result.Backend != "auto" {
		t.Errorf("result.Backend = %q, want auto", m.result.Backend)
	}
	if m.result.GateModel != "sonnet" {
		t.Errorf("result.GateModel = %q, want sonnet", m.result.GateModel)
	}
	if m.result.FixModel != "sonnet" {
		t.Errorf("result.FixModel = %q, want sonnet", m.result.FixModel)
	}
	if m.result.Concurrency != 3 {
		t.Errorf("result.Concurrency = %d, want 3", m.result.Concurrency)
	}
	if m.result.ScoreNeedsLLM != 1.0 {
		t.Errorf("result.ScoreNeedsLLM = %v, want 1.0", m.result.ScoreNeedsLLM)
	}
	if m.result.ScorePRComment != 0.4 {
		t.Errorf("result.ScorePRComment = %v, want 0.4", m.result.ScorePRComment)
	}
	if m.result.ScoreTestFailure != 1.0 {
		t.Errorf("result.ScoreTestFailure = %v, want 1.0", m.result.ScoreTestFailure)
	}
}

func TestLauncher_QuitReturnsUnsubmitted(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "1")
	// Move to a non-text field first so that "q" is treated as quit.
	m = send(t, m, "down")
	m = send(t, m, "q")
	if m.result.Submitted {
		t.Error("q should leave Submitted=false")
	}
	if !m.quitting {
		t.Error("q should mark model quitting")
	}
}

func TestLauncher_CtrlCReturnsUnsubmitted(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	m = send(t, m, "ctrl+c")
	if m.result.Submitted {
		t.Error("ctrl+c should leave Submitted=false")
	}
	if !m.quitting {
		t.Error("ctrl+c should mark model quitting")
	}
}

func TestLauncher_SSetsSaveRequested(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	// Fill target, move off text field, press s, then move back and submit.
	m = send(t, m, "1", "2", "3")
	m = send(t, m, "down")
	m = send(t, m, "s")
	if !m.saveRequested {
		t.Error("pressing s should set saveRequested")
	}
	// Submit should carry saveRequested through.
	m = send(t, m, "enter")
	if !m.result.Submitted {
		t.Fatalf("expected submitted, errMsg=%q", m.errMsg)
	}
	if !m.result.SaveRequested {
		t.Error("result.SaveRequested should be true after s + enter")
	}
}

func TestLauncher_BackendSwitchRedefaultsModels(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	// Move to backend field (index 1) then cycle until it's "codex".
	m = send(t, m, "down")
	// Cycle right through the backend values: auto -> claude -> codex.
	m = send(t, m, "right")
	m = send(t, m, "right")
	if got := m.backendValue(); got != "codex" {
		t.Fatalf("backend after 2 rights = %q, want codex", got)
	}
	// Because current models (sonnet/sonnet) are anthropic-only, switching to
	// codex should re-default gate/fix to codex defaults.
	if got := m.gateModelValue(); got != "gpt-5.4-mini" {
		t.Errorf("gate model after backend=codex = %q, want gpt-5.4-mini", got)
	}
	if got := m.fixModelValue(); got != "gpt-5.4" {
		t.Errorf("fix model after backend=codex = %q, want gpt-5.4", got)
	}
}

func TestLauncher_ViewContainsKeyLabels(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: stubModels()}
	m := newTestModel(t, cfg)

	view := m.View()
	for _, want := range []string{"Target", "AI backend", "Gate model", "Fix model", "Concurrency"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q; got:\n%s", want, view)
		}
	}
}

func TestLauncher_EmptyModelListFallsBackToHardcoded(t *testing.T) {
	cfg := LauncherConfig{Initial: config.Defaults(), Models: registry.ModelList{}}
	m := newTestModel(t, cfg)

	// sonnet must still be a valid gate model choice when Models list is empty.
	if got := m.gateModelValue(); got != "sonnet" {
		t.Errorf("gate with empty registry = %q, want sonnet", got)
	}
}
