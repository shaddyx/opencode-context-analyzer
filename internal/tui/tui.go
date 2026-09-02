// Package tui implements the interactive terminal UI for the context analyzer.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"opencode-context-analyzer/internal/analyzer"
	"opencode-context-analyzer/internal/db"
	"opencode-context-analyzer/internal/report"
	"opencode-context-analyzer/internal/tokenizer"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0"))

	accentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))
)

// sessionItem adapts a db.Session to a list.Item.
type sessionItem struct {
	s db.Session
}

func (i sessionItem) Title() string       { return i.s.Title }
func (i sessionItem) Description() string { return sessionDesc(i.s) }
func (i sessionItem) FilterValue() string { return i.s.Title + " " + i.s.Directory }

func sessionDesc(s db.Session) string {
	toks := s.TokensInput + s.TokensOutput + s.TokensCacheRead + s.TokensCacheWrite
	return fmt.Sprintf("%s  |  %s  |  %d msgs  |  %d tools  |  %s tokens",
		timeAgo(s.TimeUpdated),
		shortModel(s.Model),
		s.MessageCount,
		s.ToolCallCount,
		tokenizer.Format(toks),
	)
}

func shortModel(m string) string {
	if m == "" {
		return "unknown"
	}
	// model is stored as JSON like {"id":"...","providerID":"..."}
	if strings.Contains(m, `"id"`) {
		var parsed struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(m), &parsed)
		if parsed.ID != "" {
			return parsed.ID
		}
	}
	return m
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Model is the root TUI model.
type Model struct {
	db        *db.DB
	sources   analyzer.Sources
	width     int
	height    int
	state     state
	list      list.Model
	viewport  viewport.Model
	report    *analyzer.Report
	reportMD  string
	exported  string
	loading   bool
	err       error
}

type state int

const (
	stateList state = iota
	stateReport
)

// New creates the root model.
func New(d *db.DB, sources analyzer.Sources) Model {
	items := make([]list.Item, 0)
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "OpenCode Sessions"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)
	l.Styles.Title = titleStyle

	return Model{
		db:      d,
		sources: sources,
		state:   stateList,
		list:    l,
	}
}

// Init loads the session list.
func (m Model) Init() tea.Cmd {
	return m.loadSessions
}

func (m Model) loadSessions() tea.Msg {
	sessions, err := m.db.ListSessions()
	if err != nil {
		return errMsg{err}
	}
	return sessionsLoadedMsg{sessions}
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height)
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateList:
			return m.updateList(msg)
		case stateReport:
			return m.updateReport(msg)
		}

	case sessionsLoadedMsg:
		items := make([]list.Item, 0, len(msg.sessions))
		for _, s := range msg.sessions {
			items = append(items, sessionItem{s})
		}
		m.list.SetItems(items)
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case analysisDoneMsg:
		m.loading = false
		m.report = msg.report
		m.reportMD = report.Render(msg.report)
		m.viewport = viewport.New(m.width, m.height)
		m.viewport.SetContent(m.reportMD)
		m.state = stateReport
		return m, nil

	case exportDoneMsg:
		m.exported = msg.path
		return m, nil
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		item, ok := m.list.SelectedItem().(sessionItem)
		if !ok {
			return m, nil
		}
		m.loading = true
		m.list.SetFilteringEnabled(false)
		return m, func() tea.Msg {
			rep, err := analyzer.Analyze(m.db.SQL(), item.s.ID, m.sources)
			if err != nil {
				return errMsg{err}
			}
			return analysisDoneMsg{rep}
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) updateReport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		m.state = stateList
		m.list.SetFilteringEnabled(true)
		return m, nil
	case "e":
		if m.report == nil {
			return m, nil
		}
		return m, func() tea.Msg {
			path := exportPath(m.report)
			if err := os.WriteFile(path, []byte(m.reportMD), 0o644); err != nil {
				return errMsg{err}
			}
			return exportDoneMsg{path}
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func exportPath(rep *analyzer.Report) string {
	dir := "."
	if rep.Directory != "" {
		dir = rep.Directory
	}
	slug := sanitize(rep.SessionTitle)
	if slug == "" {
		slug = rep.SessionID
	}
	return filepath.Join(dir, fmt.Sprintf("context-report-%s.md", slug))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// View renders the UI.
func (m Model) View() string {
	if m.err != nil {
		return helpStyle.Render("Error: "+m.err.Error()) + "\n\nPress q to quit.\n"
	}
	switch m.state {
	case stateList:
		return m.list.View()
	case stateReport:
		return m.reportView()
	}
	return ""
}

func (m Model) reportView() string {
	if m.loading {
		return titleStyle.Render("Analyzing session...") + "\n"
	}
	if m.report == nil {
		return "No report.\n"
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Context Report: "+m.report.SessionTitle) + "\n")
	b.WriteString(infoStyle.Render(fmt.Sprintf("Total: %s tokens | %d tool calls | %d skills | %d agents",
		tokenizer.Format(m.report.TotalTokens),
		m.report.TotalToolCalls,
		len(m.report.Skills),
		len(m.report.Agents))) + "\n\n")
	b.WriteString(m.viewport.View())
	b.WriteString("\n" + helpStyle.Render("e: export MD  |  esc: back  |  q: quit") + "\n")
	if m.exported != "" {
		b.WriteString(accentStyle.Render("Exported to: "+m.exported) + "\n")
	}
	return b.String()
}

// message types
type sessionsLoadedMsg struct {
	sessions []db.Session
}
type analysisDoneMsg struct {
	report *analyzer.Report
}
type exportDoneMsg struct {
	path string
}
type errMsg struct {
	err error
}
