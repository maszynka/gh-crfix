// Package tui hosts the Bubble Tea models used by gh-crfix: the
// interactive launcher shown when the user runs `gh crfix` with no
// arguments on a TTY, and (on a sibling branch) the progress dashboard.
package tui

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/registry"
)

// LauncherResult is what the user picked. Fields other than Submitted and
// SaveRequested are only meaningful when Submitted=true.
type LauncherResult struct {
	Submitted        bool
	Target           string
	Backend          string
	GateModel        string
	FixModel         string
	Concurrency      int
	ScoreNeedsLLM    float64
	ScorePRComment   float64
	ScoreTestFailure float64
	SaveRequested    bool
}

// LauncherConfig configures the launcher with the user's saved defaults and
// the currently-known model list.
type LauncherConfig struct {
	Initial config.Config
	Models  registry.ModelList
}

// Field indices (stable — tests rely on the ordering).
const (
	fieldTarget = iota
	fieldBackend
	fieldGateModel
	fieldFixModel
	fieldConcurrency
	fieldNeedsLLM
	fieldPRComment
	fieldTestFailure
	numFields
)

// fallbackAnthropic/fallbackOpenAI are used when the registry is empty
// (no network, no cache). Matches the hard constraint in the task spec.
var (
	fallbackAnthropic = []string{"sonnet", "opus", "haiku"}
	fallbackOpenAI    = []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5"}
)

// backendChoices are the supported backend names in cycle order.
var backendChoices = []string{"auto", "claude", "codex"}

// Concurrency is cycled in [concurrencyMin, concurrencyMax] inclusive.
const (
	concurrencyMin = 1
	concurrencyMax = 16
)

// launcherModel is the Bubble Tea model for the launcher form.
type launcherModel struct {
	cfg LauncherConfig

	// Current field values (held separately so we can re-default models when
	// the backend is changed).
	target           string
	backend          string
	gateModel        string
	fixModel         string
	concurrency      int
	scoreNeedsLLM    float64
	scorePRComment   float64
	scoreTestFailure float64

	selected int // focused field index

	// errMsg is shown at the bottom of the form. Cleared on most keypresses
	// so the user sees fresh feedback when they try again.
	errMsg string

	// saveRequested is toggled by pressing `s`. It is copied to the returned
	// LauncherResult on successful submit so the caller knows to call
	// config.Save.
	saveRequested bool

	quitting bool
	result   LauncherResult

	// configPath is the destination for Save. It is normally
	// ~/.config/gh-crfix/defaults but can be overridden in tests.
	configPath string
}

// newLauncherModel constructs a launcher model pre-filled from cfg.Initial.
func newLauncherModel(cfg LauncherConfig) *launcherModel {
	c := cfg.Initial
	m := &launcherModel{
		cfg:              cfg,
		target:           "",
		backend:          c.AIBackend,
		gateModel:        c.GateModel,
		fixModel:         c.FixModel,
		concurrency:      c.Concurrency,
		scoreNeedsLLM:    c.ScoreNeedsLLM,
		scorePRComment:   c.ScorePRComment,
		scoreTestFailure: c.ScoreTestFailure,
		selected:         fieldTarget,
		configPath:       defaultConfigPath(),
	}
	// Snap values into valid ranges so a corrupted defaults file does not
	// leave the form unusable.
	if m.backend == "" {
		m.backend = "auto"
	}
	if m.concurrency < concurrencyMin || m.concurrency > concurrencyMax {
		m.concurrency = clampInt(m.concurrency, concurrencyMin, concurrencyMax)
	}
	m.scoreNeedsLLM = clampScore(m.scoreNeedsLLM)
	m.scorePRComment = clampScore(m.scorePRComment)
	m.scoreTestFailure = clampScore(m.scoreTestFailure)

	// Ensure model choices are valid for the chosen backend.
	if !m.modelValidForBackend(m.gateModel, m.backend) {
		m.gateModel = m.defaultGateFor(m.backend)
	}
	if !m.modelValidForBackend(m.fixModel, m.backend) {
		m.fixModel = m.defaultFixFor(m.backend)
	}
	return m
}

// defaultConfigPath returns the canonical ~/.config/gh-crfix/defaults path.
// Falls back to the current working directory if HOME cannot be resolved —
// the caller will see a Save error in that case, which is fine.
func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gh-crfix", "defaults")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "gh-crfix", "defaults")
	}
	return filepath.Join(".gh-crfix-defaults")
}

// Init implements tea.Model.
func (m *launcherModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *launcherModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches a keypress. It returns (m, cmd) — cmd is tea.Quit
// when the user confirms or cancels.
func (m *launcherModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		m.result = LauncherResult{Submitted: false}
		return m, tea.Quit
	case tea.KeyEsc:
		m.quitting = true
		m.result = LauncherResult{Submitted: false}
		return m, tea.Quit
	case tea.KeyUp:
		m.errMsg = ""
		m.selected = (m.selected - 1 + numFields) % numFields
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		m.errMsg = ""
		m.selected = (m.selected + 1) % numFields
		return m, nil
	case tea.KeyLeft:
		m.errMsg = ""
		m.cycleField(m.selected, -1)
		return m, nil
	case tea.KeyRight:
		m.errMsg = ""
		m.cycleField(m.selected, +1)
		return m, nil
	case tea.KeyBackspace:
		m.errMsg = ""
		if m.selected == fieldTarget && len(m.target) > 0 {
			r := []rune(m.target)
			m.target = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeyEnter:
		return m.handleSubmit()
	case tea.KeyRunes, tea.KeySpace:
		return m.handleRunes(k)
	}
	return m, nil
}

// handleRunes dispatches a printable keystroke. When the Target field is
// focused, every rune is appended. Otherwise we interpret a leading `q` as
// quit and a leading `s` as save-toggle; any other runes are ignored.
func (m *launcherModel) handleRunes(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.selected == fieldTarget {
		runes := k.Runes
		if k.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		m.target += string(runes)
		m.errMsg = ""
		return m, nil
	}
	// Non-text field: treat literal control runes.
	if len(k.Runes) == 1 {
		switch k.Runes[0] {
		case 'q', 'Q':
			m.quitting = true
			m.result = LauncherResult{Submitted: false}
			return m, tea.Quit
		case 's', 'S':
			m.saveRequested = !m.saveRequested
			m.errMsg = ""
			return m, nil
		}
	}
	return m, nil
}

// handleSubmit validates all fields. If validation passes the model is
// populated with the result and tea.Quit is issued. Otherwise errMsg is set
// and the form stays open.
func (m *launcherModel) handleSubmit() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.target) == "" {
		m.errMsg = "Target is required."
		return m, nil
	}
	if m.concurrency < concurrencyMin || m.concurrency > concurrencyMax {
		m.errMsg = fmt.Sprintf("Concurrency must be in [%d, %d].", concurrencyMin, concurrencyMax)
		return m, nil
	}
	if !isValidScore(m.scoreNeedsLLM) || !isValidScore(m.scorePRComment) || !isValidScore(m.scoreTestFailure) {
		m.errMsg = "Scores must be in [0.0, 1.0]."
		return m, nil
	}
	m.result = LauncherResult{
		Submitted:        true,
		Target:           m.target,
		Backend:          m.backend,
		GateModel:        m.gateModel,
		FixModel:         m.fixModel,
		Concurrency:      m.concurrency,
		ScoreNeedsLLM:    roundScore(m.scoreNeedsLLM),
		ScorePRComment:   roundScore(m.scorePRComment),
		ScoreTestFailure: roundScore(m.scoreTestFailure),
		SaveRequested:    m.saveRequested,
	}
	// If the user requested a save, persist before quitting. Save failures
	// keep the form open so the user can see the error and try again.
	if m.saveRequested {
		cfg := config.Config{
			AIBackend:        m.backend,
			GateModel:        m.gateModel,
			FixModel:         m.fixModel,
			Concurrency:      m.concurrency,
			ScoreNeedsLLM:    m.result.ScoreNeedsLLM,
			ScorePRComment:   m.result.ScorePRComment,
			ScoreTestFailure: m.result.ScoreTestFailure,
		}
		if err := config.Save(m.configPath, cfg); err != nil {
			m.errMsg = "Could not save defaults: " + err.Error()
			m.result = LauncherResult{}
			return m, nil
		}
	}
	m.quitting = true
	return m, tea.Quit
}

// cycleField advances or rewinds the currently-focused field by `delta`.
// Only enum and number fields respond; text fields ignore cycle.
func (m *launcherModel) cycleField(idx, delta int) {
	switch idx {
	case fieldBackend:
		next := cycleString(backendChoices, m.backend, delta)
		if next == m.backend {
			return
		}
		m.backend = next
		// Re-default gate/fix model if current model is not valid for the
		// new backend.
		if !m.modelValidForBackend(m.gateModel, m.backend) {
			m.gateModel = m.defaultGateFor(m.backend)
		}
		if !m.modelValidForBackend(m.fixModel, m.backend) {
			m.fixModel = m.defaultFixFor(m.backend)
		}
	case fieldGateModel:
		choices := m.modelChoicesFor(m.backend)
		m.gateModel = cycleString(choices, m.gateModel, delta)
	case fieldFixModel:
		choices := m.modelChoicesFor(m.backend)
		m.fixModel = cycleString(choices, m.fixModel, delta)
	case fieldConcurrency:
		m.concurrency = clampInt(m.concurrency+delta, concurrencyMin, concurrencyMax)
	case fieldNeedsLLM:
		m.scoreNeedsLLM = cycleScore(m.scoreNeedsLLM, delta)
	case fieldPRComment:
		m.scorePRComment = cycleScore(m.scorePRComment, delta)
	case fieldTestFailure:
		m.scoreTestFailure = cycleScore(m.scoreTestFailure, delta)
	}
}

// modelChoicesFor returns the list of model names selectable for `backend`.
// For backend=auto it concatenates both families so the user can mix.
func (m *launcherModel) modelChoicesFor(backend string) []string {
	anth := m.cfg.Models.AnthropicAliases
	if len(anth) == 0 {
		anth = fallbackAnthropic
	}
	oai := m.cfg.Models.OpenAIAliases
	if len(oai) == 0 {
		oai = fallbackOpenAI
	}
	switch backend {
	case "claude":
		return append([]string{}, anth...)
	case "codex":
		return append([]string{}, oai...)
	default:
		out := make([]string, 0, len(anth)+len(oai))
		out = append(out, anth...)
		out = append(out, oai...)
		return out
	}
}

// modelValidForBackend reports whether `model` is a valid choice when the
// currently-selected backend is `backend`. An empty model is never valid.
func (m *launcherModel) modelValidForBackend(model, backend string) bool {
	if model == "" {
		return false
	}
	for _, v := range m.modelChoicesFor(backend) {
		if v == model {
			return true
		}
	}
	return false
}

// defaultGateFor returns the preferred gate model for `backend`, using the
// registry helper when available so tests + runtime stay in sync.
func (m *launcherModel) defaultGateFor(backend string) string {
	if g := m.cfg.Models.DefaultGate(backend); g != "" {
		return g
	}
	if backend == "codex" {
		return "gpt-5.4-mini"
	}
	return "sonnet"
}

// defaultFixFor returns the preferred fix model for `backend`.
func (m *launcherModel) defaultFixFor(backend string) string {
	if f := m.cfg.Models.DefaultFix(backend); f != "" {
		return f
	}
	if backend == "codex" {
		return "gpt-5.4"
	}
	return "sonnet"
}

// View implements tea.Model.
func (m *launcherModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Render("gh crfix launcher")
	b.WriteString(title)
	b.WriteString("\n")
	hint := lipgloss.NewStyle().Faint(true).Render(
		"Enter a PR number, range, list, or full GitHub URL. Use arrows on the rest.")
	b.WriteString(hint)
	b.WriteString("\n\n")

	b.WriteString(m.renderField(fieldTarget, "Target", m.targetDisplay()))
	b.WriteString(m.renderField(fieldBackend, "AI backend", decorateCycle(m.backend)))
	b.WriteString(m.renderField(fieldGateModel, "Gate model", decorateCycle(m.gateModel)))
	b.WriteString(m.renderField(fieldFixModel, "Fix model", decorateCycle(m.fixModel)))
	b.WriteString(m.renderField(fieldConcurrency, "Concurrency", decorateCycle(fmt.Sprintf("%d", m.concurrency))))
	b.WriteString(m.renderField(fieldNeedsLLM, "needs_llm", decorateCycle(fmt.Sprintf("%.1f", m.scoreNeedsLLM))))
	b.WriteString(m.renderField(fieldPRComment, "pr_comment", decorateCycle(fmt.Sprintf("%.1f", m.scorePRComment))))
	b.WriteString(m.renderField(fieldTestFailure, "test_failure", decorateCycle(fmt.Sprintf("%.1f", m.scoreTestFailure))))

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(
		"[Enter] run   [s] save defaults   [q] cancel   up/down navigate   left/right change"))
	b.WriteString("\n")

	if m.saveRequested {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("(save-on-submit armed)"))
		b.WriteString("\n")
	}
	if m.errMsg != "" {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("error: "))
		b.WriteString(m.errMsg)
		b.WriteString("\n")
	}
	return b.String()
}

// renderField produces a single labelled row. Focused rows are marked with
// a right-pointing ASCII arrow so screen readers and plain-text copies stay
// readable.
func (m *launcherModel) renderField(idx int, label, value string) string {
	prefix := "   "
	if idx == m.selected {
		prefix = " > "
	}
	row := fmt.Sprintf("%s%-14s %s", prefix, label+":", value)
	if idx == m.selected {
		return lipgloss.NewStyle().Bold(true).Render(row) + "\n"
	}
	return row + "\n"
}

// targetDisplay renders the Target field — showing a caret when the field
// is focused so the user can see where typing lands.
func (m *launcherModel) targetDisplay() string {
	if m.selected == fieldTarget {
		return "[" + m.target + "_]"
	}
	if m.target == "" {
		return "(PR spec)"
	}
	return m.target
}

// decorateCycle wraps a cycle-field value with ASCII chevrons so the user
// knows it responds to left/right.
func decorateCycle(v string) string {
	return "< " + v + " >"
}

// --- accessors used by tests -------------------------------------------------

func (m *launcherModel) targetValue() string       { return m.target }
func (m *launcherModel) backendValue() string      { return m.backend }
func (m *launcherModel) gateModelValue() string    { return m.gateModel }
func (m *launcherModel) fixModelValue() string     { return m.fixModel }
func (m *launcherModel) concurrencyValue() int     { return m.concurrency }
func (m *launcherModel) scoreNeedsLLMValue() float64 {
	return roundScore(m.scoreNeedsLLM)
}
func (m *launcherModel) scorePRCommentValue() float64 {
	return roundScore(m.scorePRComment)
}
func (m *launcherModel) scoreTestFailureValue() float64 {
	return roundScore(m.scoreTestFailure)
}

// --- small helpers -----------------------------------------------------------

// cycleString advances `current` by `delta` steps through `choices`. If
// `current` isn't in `choices`, the first element is returned.
func cycleString(choices []string, current string, delta int) string {
	if len(choices) == 0 {
		return current
	}
	idx := -1
	for i, v := range choices {
		if v == current {
			idx = i
			break
		}
	}
	if idx < 0 {
		if delta >= 0 {
			return choices[0]
		}
		return choices[len(choices)-1]
	}
	next := (idx + delta) % len(choices)
	if next < 0 {
		next += len(choices)
	}
	return choices[next]
}

// clampInt constrains v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampScore constrains v to [0, 1] rounded to the nearest 0.1 step.
func clampScore(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return roundScore(v)
}

// roundScore rounds v to 1 decimal place to keep the displayed/returned
// value clean after repeated cycling (floating-point arithmetic otherwise
// drifts).
func roundScore(v float64) float64 {
	return math.Round(v*10) / 10
}

// cycleScore advances a score by delta * 0.1, wrapping at [0, 1].
func cycleScore(v float64, delta int) float64 {
	steps := int(math.Round(v*10)) + delta
	if steps < 0 {
		steps = 10
	}
	if steps > 10 {
		steps = 0
	}
	return float64(steps) / 10
}

// isValidScore reports whether v is in the inclusive [0, 1] range.
func isValidScore(v float64) bool { return v >= 0 && v <= 1 }

// RunLauncher blocks on a Bubble Tea program until the user submits or
// cancels. The returned LauncherResult has Submitted=true on successful
// submit and Submitted=false on cancel / context-cancel.
func RunLauncher(ctx context.Context, cfg LauncherConfig) (LauncherResult, error) {
	m := newLauncherModel(cfg)
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithAltScreen()}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if err != nil {
		return LauncherResult{}, err
	}
	if fm, ok := final.(*launcherModel); ok {
		return fm.result, nil
	}
	return LauncherResult{}, nil
}
