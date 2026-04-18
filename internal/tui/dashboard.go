// Package tui renders the gh-crfix dashboard in a Bubble Tea program.
//
// The dashboard polls a *progress.Tracker snapshot on a fixed interval and
// draws one row per PR. From the dashboard the user can drill into a
// detail view with the full 14-step checklist and a tail of that PR's
// log file.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/maszynka/gh-crfix/internal/logs"
	"github.com/maszynka/gh-crfix/internal/progress"
)

// keyMap binds every keystroke the dashboard understands. We use
// bubbles/key here so bindings are self-describing; even though the
// dashboard renders its own minimal footer, this keeps the bindings in
// one place for future help output.
type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Enter  key.Binding
	Back   key.Binding
	Quit   key.Binding
	Resize key.Binding
}

var keys = keyMap{
	Up:    key.NewBinding(key.WithKeys("up", "k")),
	Down:  key.NewBinding(key.WithKeys("down", "j")),
	Enter: key.NewBinding(key.WithKeys("enter", "right")),
	Back:  key.NewBinding(key.WithKeys("left", "esc")),
	Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c")),
}

// DashboardConfig drives the Bubble Tea program.
type DashboardConfig struct {
	PRNums  []int          // PRs being processed
	Titles  map[int]string // PR title by PR num (from setup phase)
	Tracker *progress.Tracker
	Run     *logs.Run
	Refresh time.Duration // default 250ms
}

// Run starts the dashboard synchronously and blocks until the user quits
// or ctx is cancelled. It is intended to be called from the main
// goroutine with a TTY attached.
//
// The program runs on the alternate screen buffer. This isolates the
// dashboard's framebuffer from stray writes on the underlying tty (raw
// mode turns bare "\n" into line-feeds-without-carriage-return, which
// otherwise produces a cascading staircase of text as stderr from
// subprocess hooks — autofix, validation — bleeds through). When the
// dashboard exits the primary screen is restored, and the caller's
// summary banners render normally.
func Run(ctx context.Context, cfg DashboardConfig) error {
	if cfg.Refresh <= 0 {
		cfg.Refresh = 250 * time.Millisecond
	}
	p := tea.NewProgram(newModel(cfg), tea.WithAltScreen())

	// Forward ctx cancellation into the program so callers can tear us
	// down cleanly.
	if ctx != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				p.Quit()
			case <-done:
			}
		}()
	}

	_, err := p.Run()
	return err
}

// ─── messages ────────────────────────────────────────────────────────────

// refreshMsg is sent every cfg.Refresh interval and triggers a re-read of
// the Tracker snapshot plus the currently-selected PR's log tail.
type refreshMsg struct{}

// logTailMsg carries freshly-read log lines for the detail view.
type logTailMsg struct {
	pr    int
	lines []string
}

// ─── view modes ──────────────────────────────────────────────────────────

type viewMode int

const (
	viewDashboard viewMode = iota
	viewDetail
)

// ─── model ───────────────────────────────────────────────────────────────

type model struct {
	cfg DashboardConfig

	// snapshot is a shallow copy of Tracker.Snapshot() taken on each
	// refresh tick. Never nil after the first tick.
	snapshot map[int]map[progress.Step]progress.Entry

	// visibleLog is the last ~20 lines read from the selected PR's log.
	visibleLog []string

	mode     viewMode
	selected int // index into cfg.PRNums

	width  int
	height int

	// detailScroll is how many lines to skip from the top of the log
	// tail when scrolling. 0 means show the most recent window.
	detailScroll int
}

// newModel builds a model from cfg. It populates snapshot from the
// tracker so View() works before any tick arrives.
func newModel(cfg DashboardConfig) model {
	m := model{
		cfg:      cfg,
		snapshot: map[int]map[progress.Step]progress.Entry{},
		mode:     viewDashboard,
		width:    80,
		height:   24,
	}
	if cfg.Tracker != nil {
		m.snapshot = cfg.Tracker.Snapshot()
	}
	return m
}

// Init is required by tea.Model. It kicks off the refresh cadence.
func (m model) Init() tea.Cmd {
	return tea.Batch(m.tickCmd(), m.refreshLogCmd())
}

func (m model) tickCmd() tea.Cmd {
	d := m.cfg.Refresh
	if d <= 0 {
		d = 250 * time.Millisecond
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return refreshMsg{} })
}

// refreshLogCmd reads the selected PR's log tail off the event loop so
// Update never blocks on disk I/O.
func (m model) refreshLogCmd() tea.Cmd {
	if m.cfg.Run == nil || len(m.cfg.PRNums) == 0 {
		return nil
	}
	pr := m.selectedPR()
	if pr == 0 {
		return nil
	}
	path := m.cfg.Run.PRLog(pr)
	return func() tea.Msg {
		lines := tailFile(path, 20)
		return logTailMsg{pr: pr, lines: lines}
	}
}

func (m model) selectedPR() int {
	if len(m.cfg.PRNums) == 0 {
		return 0
	}
	if m.selected < 0 || m.selected >= len(m.cfg.PRNums) {
		return m.cfg.PRNums[0]
	}
	return m.cfg.PRNums[m.selected]
}

// ─── update ──────────────────────────────────────────────────────────────

// Update handles all messages and must never block on I/O.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = clampMin(msg.Width, 1)
		m.height = clampMin(msg.Height, 1)
		return m, nil

	case refreshMsg:
		if m.cfg.Tracker != nil {
			m.snapshot = m.cfg.Tracker.Snapshot()
		}
		return m, tea.Batch(m.tickCmd(), m.refreshLogCmd())

	case logTailMsg:
		// Only apply tail if it matches currently selected PR.
		if msg.pr == m.selectedPR() {
			m.visibleLog = msg.lines
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global quit (via bubbles/key binding).
	if key.Matches(k, keys.Quit) {
		return m, tea.Quit
	}

	switch m.mode {
	case viewDashboard:
		switch {
		case key.Matches(k, keys.Up):
			if m.selected > 0 {
				m.selected--
				m.visibleLog = nil
				return m, m.refreshLogCmd()
			}
		case key.Matches(k, keys.Down):
			if m.selected < len(m.cfg.PRNums)-1 {
				m.selected++
				m.visibleLog = nil
				return m, m.refreshLogCmd()
			}
		case key.Matches(k, keys.Enter):
			m.mode = viewDetail
			m.detailScroll = 0
			return m, m.refreshLogCmd()
		}
	case viewDetail:
		switch {
		case key.Matches(k, keys.Back):
			m.mode = viewDashboard
			return m, nil
		case key.Matches(k, keys.Up):
			if m.detailScroll > 0 {
				m.detailScroll--
			}
		case key.Matches(k, keys.Down):
			if m.detailScroll < len(m.visibleLog) {
				m.detailScroll++
			}
		}
	}
	return m, nil
}

// ─── styles ──────────────────────────────────────────────────────────────

var (
	styleRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	styleDone    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	styleFailed  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	styleQueued  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray
	styleSkipped = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleGray    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// ─── rendering ───────────────────────────────────────────────────────────

// View dispatches on mode.
func (m model) View() string {
	switch m.mode {
	case viewDetail:
		return m.viewDetail()
	default:
		return m.viewDashboard()
	}
}

// overallState collapses a PR's per-step state into one bucket for the
// dashboard row. The rules mirror the bash pipeline:
//   - any failed step -> failed
//   - every known step done/skipped -> done
//   - any running step -> running
//   - otherwise queued
func overallState(entries map[progress.Step]progress.Entry) string {
	if len(entries) == 0 {
		return "queued"
	}
	running := false
	allDone := true
	hasAny := false
	for _, s := range progress.AllSteps() {
		e, ok := entries[s]
		if !ok {
			allDone = false
			continue
		}
		hasAny = true
		switch e.Status {
		case progress.Failed:
			return "failed"
		case progress.Running:
			running = true
			allDone = false
		case progress.Done, progress.Skipped:
			// counts toward done
		default:
			allDone = false
		}
	}
	if running {
		return "running"
	}
	if hasAny && allDone {
		return "done"
	}
	return "queued"
}

// activeStep returns the first running step for a PR, or "" if none.
func activeStep(entries map[progress.Step]progress.Entry) string {
	for _, s := range progress.AllSteps() {
		if e, ok := entries[s]; ok && e.Status == progress.Running {
			return string(s)
		}
	}
	return ""
}

func statusIcon(state string) string {
	switch state {
	case "running":
		return "●"
	case "done":
		return "✓"
	case "failed":
		return "✗"
	case "skipped":
		return "–"
	default:
		return "○"
	}
}

func statusStyle(state string) lipgloss.Style {
	switch state {
	case "running":
		return styleRunning
	case "done":
		return styleDone
	case "failed":
		return styleFailed
	case "skipped":
		return styleSkipped
	default:
		return styleQueued
	}
}

func (m model) viewDashboard() string {
	var b strings.Builder

	width := m.width
	if width <= 0 {
		width = 80
	}

	// Header row.
	header := fmt.Sprintf(" %-5s %-11s %-14s %-9s %s",
		"PR#", "Status", "Step", "Elapsed", "Title")
	b.WriteString(styleHeader.Render(header))
	b.WriteByte('\n')

	// Separator.
	b.WriteString(styleGray.Render(strings.Repeat("─", minInt(width, 120))))
	b.WriteByte('\n')

	var nRun, nDone, nFail, nQueued int

	for i, pr := range m.cfg.PRNums {
		entries := m.snapshot[pr]
		state := overallState(entries)
		switch state {
		case "running":
			nRun++
		case "done":
			nDone++
		case "failed":
			nFail++
		default:
			nQueued++
		}

		step := activeStep(entries)
		if step == "" {
			step = "—"
		}

		elapsed := "—"
		if m.cfg.Run != nil {
			if d, ok := m.cfg.Run.Elapsed(pr); ok && d > 0 {
				elapsed = formatElapsed(d)
			}
		}

		title := ""
		if m.cfg.Titles != nil {
			title = m.cfg.Titles[pr]
		}
		titleBudget := width - 45
		if titleBudget < 10 {
			titleBudget = 10
		}
		title = truncate(title, titleBudget)

		sel := "  "
		if i == m.selected {
			sel = "▸ "
		}

		icon := statusIcon(state)
		st := statusStyle(state)

		// Two-column status: icon + label, both colored.
		statusCell := fmt.Sprintf("%s %-7s", icon, state)

		row := fmt.Sprintf("%s%-5d %s %-14s %-9s %s",
			sel,
			pr,
			st.Render(statusCell),
			step,
			elapsed,
			title,
		)
		b.WriteString(row)
		b.WriteByte('\n')
	}

	// Totals.
	b.WriteByte('\n')
	totals := fmt.Sprintf(" %d running | %d done | %d failed | %d queued",
		nRun, nDone, nFail, nQueued)
	b.WriteString(styleGray.Render(totals))
	b.WriteByte('\n')

	// Footer keys.
	b.WriteString(styleGray.Render(" ↑↓ navigate  ⏎ detail  q quit"))
	return b.String()
}

func (m model) viewDetail() string {
	var b strings.Builder
	width := m.width
	if width <= 0 {
		width = 80
	}

	pr := m.selectedPR()
	entries := m.snapshot[pr]
	state := overallState(entries)
	st := statusStyle(state)

	title := ""
	if m.cfg.Titles != nil {
		title = m.cfg.Titles[pr]
	}

	elapsed := ""
	if m.cfg.Run != nil {
		if d, ok := m.cfg.Run.Elapsed(pr); ok && d > 0 {
			elapsed = " " + formatElapsed(d)
		}
	}

	header := fmt.Sprintf(" PR #%d — %s  %s",
		pr,
		truncate(title, maxInt(width-30, 10)),
		st.Render(fmt.Sprintf("[%s %s%s]", statusIcon(state), state, elapsed)),
	)
	b.WriteString(styleHeader.Render(header))
	b.WriteByte('\n')
	b.WriteString(styleGray.Render(strings.Repeat("─", minInt(width, 120))))
	b.WriteByte('\n')

	// Checklist: 14 canonical steps.
	b.WriteString(styleGray.Render(" Checklist:"))
	b.WriteByte('\n')
	for _, s := range progress.AllSteps() {
		e, ok := entries[s]
		var stText string
		if !ok {
			stText = "queued"
		} else {
			stText = string(e.Status)
		}
		marker := stepMarker(stText)
		stepStyle := stepStyleFor(stText)
		line := fmt.Sprintf(" %s %-22s %s",
			marker,
			progress.Label(s),
			e.Note,
		)
		line = truncate(line, maxInt(width-2, 10))
		b.WriteString(stepStyle.Render(line))
		b.WriteByte('\n')
	}

	// Log tail.
	b.WriteByte('\n')
	b.WriteString(styleGray.Render(" Recent log:"))
	b.WriteByte('\n')
	if len(m.visibleLog) == 0 {
		b.WriteString(styleGray.Render(" (no log yet)"))
		b.WriteByte('\n')
	} else {
		start := m.detailScroll
		if start < 0 {
			start = 0
		}
		if start > len(m.visibleLog) {
			start = len(m.visibleLog)
		}
		for _, line := range m.visibleLog[start:] {
			line = strings.TrimRight(line, "\n")
			line = truncate(line, maxInt(width-2, 10))
			b.WriteString(" ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(styleGray.Render(" ← back  ↑↓ scroll  q quit"))
	return b.String()
}

func stepMarker(status string) string {
	switch progress.Status(status) {
	case progress.Running:
		return "●"
	case progress.Done:
		return "✓"
	case progress.Failed:
		return "✗"
	case progress.Skipped:
		return "–"
	default:
		return "○"
	}
}

func stepStyleFor(status string) lipgloss.Style {
	switch progress.Status(status) {
	case progress.Running:
		return styleRunning
	case progress.Done:
		return styleDone
	case progress.Failed:
		return styleFailed
	case progress.Skipped:
		return styleSkipped
	default:
		return styleQueued
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	mins := total / 60
	secs := total % 60
	return fmt.Sprintf("%d:%02d", mins, secs)
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func clampMin(v, lo int) int {
	if v < lo {
		return lo
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// tailFile returns the last n lines of path. Missing or unreadable files
// yield an empty slice — never an error — because the dashboard must
// keep rendering even while pipeline workers are still spinning up.
//
// Implementation note: this is called every refresh tick (≈250ms) on the
// master log file, which grows throughout a batch run. Slurping the full
// file (os.ReadFile + strings.Split) gets quadratic in total work as the
// file grows to 10s of MB — so we seek from EOF and read chunks backwards
// until we have n newlines or we hit the start of file. Memory use is
// bounded by (n + 1) * max-line-length + one chunk.
func tailFile(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	size := fi.Size()
	if size == 0 {
		return nil
	}

	// Read chunks of this size from the end, doubling if a single chunk
	// doesn't yield enough newlines. 8KB is comfortable for the typical
	// master-log line widths we see in practice.
	const chunkSize int64 = 8 * 1024
	var (
		buf      []byte
		pos      = size
		newlines = 0
	)
	for pos > 0 {
		read := chunkSize
		if pos < read {
			read = pos
		}
		pos -= read
		chunk := make([]byte, read)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return nil
		}
		// Prepend chunk to buf (we're walking backwards).
		buf = append(chunk, buf...)
		newlines = 0
		for _, b := range buf {
			if b == '\n' {
				newlines++
			}
		}
		// We need n newlines AND one extra to guarantee the first full line
		// isn't truncated. If the file is smaller than our window we just
		// stop when pos hits 0.
		if newlines > n {
			break
		}
	}

	// Drop a trailing newline (file ends in \n) so Split doesn't add an
	// empty element.
	if len(buf) > 0 && buf[len(buf)-1] == '\n' {
		buf = buf[:len(buf)-1]
	}
	lines := strings.Split(string(buf), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}
