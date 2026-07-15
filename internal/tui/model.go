package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

const refreshInterval = time.Second

type Client interface {
	List(context.Context) ([]orchestrator.Run, error)
	State(context.Context, string) (daemon.RunState, error)
}

type Model struct {
	ctx                 context.Context
	client              Client
	runs                []orchestrator.Run
	state               daemon.RunState
	selectedRunIndex    int
	selectedThreadIndex int
	focus               Focus
	detail              bool
	width               int
	height              int
	lastRefresh         time.Time
	err                 error
	attachRunID         string
}

type fetchMsg struct {
	runs  []orchestrator.Run
	state daemon.RunState
	at    time.Time
	err   error
}

type tickMsg time.Time

func Run(ctx context.Context, client Client) (string, error) {
	model := Model{ctx: ctx, client: client, focus: FocusRuns}
	program := tea.NewProgram(model)
	final, err := program.Run()
	if err != nil {
		return "", err
	}
	if model, ok := final.(Model); ok {
		return model.attachRunID, nil
	}
	return "", nil
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(""), tickCmd())
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetchCmd(m.selectedRunID()), tickCmd())
	case fetchMsg:
		m.err = message.err
		if message.err != nil {
			return m, nil
		}
		m.runs = sortedRuns(message.runs)
		if message.state.Run.ID != "" {
			if index := runIndex(m.runs, message.state.Run.ID); index >= 0 {
				m.state = message.state
				m.selectedRunIndex = index
				m.selectedThreadIndex = clamp(m.selectedThreadIndex, len(m.threadRows()))
			} else {
				m.selectedRunIndex = clamp(m.selectedRunIndex, len(m.runs))
				m.state = daemon.RunState{}
				m.selectedThreadIndex = 0
			}
		} else {
			m.selectedRunIndex = clamp(m.selectedRunIndex, len(m.runs))
			m.state = daemon.RunState{}
			m.selectedThreadIndex = 0
		}
		m.lastRefresh = message.at
		return m, nil
	case tea.KeyMsg:
		if m.detail {
			switch message.String() {
			case "esc":
				m.detail = false
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		switch message.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m, m.fetchCmd(m.selectedRunID())
		case "tab":
			if m.focus == FocusRuns {
				m.focus = FocusThreads
			} else {
				m.focus = FocusRuns
			}
			return m, nil
		case "enter":
			if len(m.threadRows()) != 0 {
				m.detail = true
			}
			return m, nil
		case "a":
			if id := m.selectedRunID(); id != "" {
				m.attachRunID = id
				return m, tea.Quit
			}
			return m, nil
		case "up":
			if m.focus == FocusRuns {
				m.selectedRunIndex = clamp(m.selectedRunIndex-1, len(m.runs))
				m.selectedThreadIndex = 0
				return m, m.fetchCmd(m.selectedRunID())
			}
			m.selectedThreadIndex = clamp(m.selectedThreadIndex-1, len(m.threadRows()))
			return m, nil
		case "down":
			if m.focus == FocusRuns {
				m.selectedRunIndex = clamp(m.selectedRunIndex+1, len(m.runs))
				m.selectedThreadIndex = 0
				return m, m.fetchCmd(m.selectedRunID())
			}
			m.selectedThreadIndex = clamp(m.selectedThreadIndex+1, len(m.threadRows()))
			return m, nil
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.detail {
		return m.detailView()
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	leftWidth := 24
	if width < 80 {
		leftWidth = 20
	}
	rightWidth := width - leftWidth - 5
	if rightWidth < 40 {
		rightWidth = 40
	}
	left := panelStyle.Width(leftWidth).Render(m.runsView())
	right := panelStyle.Width(rightWidth).Render(m.treeView())
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right) + "\n" + m.statusLine(width)
}

func (m Model) fetchCmd(selectedRunID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		runs, err := m.client.List(ctx)
		if err != nil {
			return fetchMsg{err: err}
		}
		runs = sortedRuns(runs)
		runID := selectedRunID
		if runID == "" || !containsRun(runs, runID) {
			if len(runs) != 0 {
				runID = runs[0].ID
			}
		}
		var state daemon.RunState
		if runID != "" {
			state, err = m.client.State(ctx, runID)
			if err != nil {
				return fetchMsg{err: err}
			}
		}
		return fetchMsg{runs: runs, state: state, at: time.Now()}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) runsView() string {
	lines := []string{headerStyle.Render("Runs")}
	if len(m.runs) == 0 {
		lines = append(lines, mutedStyle.Render("no runs"))
		return strings.Join(lines, "\n")
	}
	for index, run := range m.runs {
		line := fmt.Sprintf("%s  %s", run.ID, run.Status)
		if index == m.selectedRunIndex {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) treeView() string {
	lines := []string{headerStyle.Render("Active Tree")}
	rows := m.threadRows()
	if len(rows) == 0 {
		lines = append(lines, mutedStyle.Render("no active threads"))
		return strings.Join(lines, "\n")
	}
	for index, row := range rows {
		line := fmt.Sprintf("%-6s %-24s %-14s %s", row.Role, row.ID, row.Status, formatCapacity(row.Capacity))
		if index == m.selectedThreadIndex {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) detailView() string {
	rows := m.threadRows()
	if len(rows) == 0 {
		return "No thread selected\n\nesc: back  q: quit"
	}
	row := rows[m.selectedThreadIndex]
	thread := row.Thread
	lines := []string{
		headerStyle.Render("Thread: " + thread.ID),
		"Role: " + string(thread.Role),
		"Status: " + string(thread.Status),
		"Assigned task: " + emptyAsUnknown(thread.AssignedTask),
		"Current turn: " + emptyAsUnknown(thread.CurrentTurnID),
		"Token capacity: " + formatCapacityDetail(row.Capacity),
		"",
		headerStyle.Render("Progress"),
		"Completed work: " + joinValues(thread.Progress.CompletedWork),
		"In progress work: " + joinValues(thread.Progress.InProgressWork),
		"Next action: " + emptyAsUnknown(thread.Progress.NextAction),
		"Blockers: " + joinValues(thread.Progress.Blockers),
		"",
		headerStyle.Render("Handoffs"),
		"Accepted: " + joinValues(thread.AcceptedHandoffs),
		"Transcript: " + emptyAsUnknown(thread.TranscriptPath),
		"",
		mutedStyle.Render("esc: back  q: quit"),
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	if width < 44 {
		width = 44
	}
	return panelStyle.Width(width - 4).Render(strings.Join(lines, "\n"))
}

func (m Model) statusLine(width int) string {
	selected := emptyAsUnknown(m.selectedRunID())
	refreshed := "never"
	if !m.lastRefresh.IsZero() {
		refreshed = m.lastRefresh.Format("15:04:05")
	}
	message := fmt.Sprintf("refresh %s  run %s  keys: up/down tab enter a r q", refreshed, selected)
	if m.err != nil {
		message = "error: " + m.err.Error()
	}
	if width > 0 && len(message) > width {
		message = message[:width]
	}
	return statusStyle.Render(message)
}

func (m Model) selectedRunID() string {
	if len(m.runs) == 0 || m.selectedRunIndex < 0 || m.selectedRunIndex >= len(m.runs) {
		if m.state.Run.ID != "" {
			return m.state.Run.ID
		}
		return ""
	}
	return m.runs[m.selectedRunIndex].ID
}

func (m Model) threadRows() []ThreadRow {
	if m.state.Run.ID == "" {
		return nil
	}
	return activeTreeRows(m.state.Run)
}

func clamp(index, length int) int {
	if length <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

func containsRun(runs []orchestrator.Run, id string) bool {
	return runIndex(runs, id) >= 0
}

func runIndex(runs []orchestrator.Run, id string) int {
	for index, run := range runs {
		if run.ID == id {
			return index
		}
	}
	return -1
}

func joinValues(values []string) string {
	if len(values) == 0 {
		return "unknown"
	}
	return strings.Join(values, "; ")
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

var (
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1)
	headerStyle   = lipgloss.NewStyle().Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("15"))
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238")).Padding(0, 1)
)
