package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// CommandModel displays a command block with output and danger highlighting.
type CommandModel struct {
	command     string
	output      strings.Builder
	isDangerous bool
	done        bool
	theme       Theme
}

// NewCommandModel creates a new command display.
func NewCommandModel(command string, isDangerous bool, theme Theme) CommandModel {
	return CommandModel{
		command:     command,
		isDangerous: isDangerous,
		theme:       theme,
	}
}

// AddOutput appends a line of output.
func (m *CommandModel) AddOutput(line string, isStderr bool) {
	if isStderr {
		m.output.WriteString(lipgloss.NewStyle().Foreground(m.theme.Warning).Render(line))
	} else {
		m.output.WriteString(line)
	}
	m.output.WriteString("\n")
}

// Done marks the command as complete.
func (m *CommandModel) Done() {
	m.done = true
}

// View renders the command block.
func (m CommandModel) View() string {
	var sb strings.Builder

	// Command header
	cmdStyle := lipgloss.NewStyle().Foreground(m.theme.Secondary).Bold(true)
	if m.isDangerous {
		cmdStyle = lipgloss.NewStyle().Foreground(m.theme.Danger).Bold(true)
	}

	prefix := "$ "
	sb.WriteString(cmdStyle.Render(prefix + m.command))
	sb.WriteString("\n")

	// Output
	if m.output.Len() > 0 {
		outStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
		sb.WriteString(outStyle.Render(strings.TrimRight(m.output.String(), "\n")))
		sb.WriteString("\n")
	}

	return sb.String()
}
