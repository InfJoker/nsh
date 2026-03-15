package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// StatusModel renders the bottom status bar.
type StatusModel struct {
	cwd     string
	model   string
	busy    bool
	width   int
	theme   Theme
}

// NewStatusModel creates a new status bar.
func NewStatusModel(theme Theme) StatusModel {
	return StatusModel{theme: theme}
}

// SetCwd updates the displayed working directory.
func (m *StatusModel) SetCwd(cwd string) {
	m.cwd = cwd
}

// SetModel sets the displayed LLM model name.
func (m *StatusModel) SetModel(model string) {
	m.model = model
}

// SetBusy toggles the busy spinner.
func (m *StatusModel) SetBusy(busy bool) {
	m.busy = busy
}

// SetWidth sets the available width.
func (m *StatusModel) SetWidth(w int) {
	m.width = w
}

// View renders the status bar.
func (m StatusModel) View() string {
	barStyle := lipgloss.NewStyle().
		Background(m.theme.Muted).
		Foreground(m.theme.Text).
		Width(m.width)

	// Left: cwd
	cwd := abbreviatePath(m.cwd)
	left := fmt.Sprintf(" %s", cwd)

	// Right: model + spinner
	right := m.model
	if m.busy {
		right = "⠋ " + right
	}
	right = right + " "

	// Pad middle
	padding := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if padding < 0 {
		padding = 0
	}

	content := left + strings.Repeat(" ", padding) + right
	return barStyle.Render(content)
}

func abbreviatePath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(p, home) {
		rel := p[len(home):]
		if rel == "" {
			return "~"
		}
		return "~" + rel
	}
	return p
}
