package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// InputModel handles text input with history navigation.
type InputModel struct {
	value      string
	cursor     int
	history    []string
	historyIdx int // -1 = current input, 0..n = history entries (from end)
	savedInput string
	focused    bool
	width      int
	theme      Theme
}

// NewInputModel creates a new input model.
func NewInputModel(theme Theme) InputModel {
	return InputModel{
		historyIdx: -1,
		focused:    true,
		theme:      theme,
	}
}

// SetHistory updates the history entries.
func (m *InputModel) SetHistory(history []string) {
	m.history = history
}

// SetWidth sets the available width.
func (m *InputModel) SetWidth(w int) {
	m.width = w
}

// Value returns the current input value.
func (m InputModel) Value() string {
	return m.value
}

// Reset clears the input.
func (m *InputModel) Reset() {
	m.value = ""
	m.cursor = 0
	m.historyIdx = -1
	m.savedInput = ""
}

// SetFocused enables or disables input.
func (m *InputModel) SetFocused(focused bool) {
	m.focused = focused
}

// Update handles key events.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	if !m.focused {
		return m, nil
	}

	kmsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	key := kmsg.Key()

	switch key.Code {
	case tea.KeyUp:
		m.navigateHistory(1)
	case tea.KeyDown:
		m.navigateHistory(-1)
	case tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyRight:
		if m.cursor < len(m.value) {
			m.cursor++
		}
	case tea.KeyBackspace:
		if m.cursor > 0 {
			m.value = m.value[:m.cursor-1] + m.value[m.cursor:]
			m.cursor--
		}
	case tea.KeyDelete:
		if m.cursor < len(m.value) {
			m.value = m.value[:m.cursor] + m.value[m.cursor+1:]
		}
	default:
		// Handle Ctrl combinations
		if key.Mod&tea.ModCtrl != 0 {
			switch key.Code {
			case 'a':
				m.cursor = 0
			case 'e':
				m.cursor = len(m.value)
			case 'k':
				m.value = m.value[:m.cursor]
			case 'u':
				m.value = m.value[m.cursor:]
				m.cursor = 0
			case 'w':
				m.deleteWord()
			}
			return m, nil
		}

		// Regular text input
		text := key.Text
		if text != "" {
			m.value = m.value[:m.cursor] + text + m.value[m.cursor:]
			m.cursor += len(text)
		}
	}

	return m, nil
}

func (m *InputModel) navigateHistory(direction int) {
	if len(m.history) == 0 {
		return
	}

	if m.historyIdx == -1 && direction > 0 {
		m.savedInput = m.value
		m.historyIdx = 0
	} else {
		m.historyIdx += direction
	}

	if m.historyIdx < -1 {
		m.historyIdx = -1
	}
	if m.historyIdx >= len(m.history) {
		m.historyIdx = len(m.history) - 1
	}

	if m.historyIdx == -1 {
		m.value = m.savedInput
	} else {
		idx := len(m.history) - 1 - m.historyIdx
		if idx >= 0 && idx < len(m.history) {
			m.value = m.history[idx]
		}
	}
	m.cursor = len(m.value)
}

func (m *InputModel) deleteWord() {
	if m.cursor == 0 {
		return
	}
	i := m.cursor - 1
	for i > 0 && m.value[i-1] == ' ' {
		i--
	}
	for i > 0 && m.value[i-1] != ' ' {
		i--
	}
	m.value = m.value[:i] + m.value[m.cursor:]
	m.cursor = i
}

// View renders the input line.
func (m InputModel) View() string {
	prompt := lipgloss.NewStyle().Foreground(m.theme.Primary).Bold(true).Render("❯ ")

	if !m.focused {
		muted := lipgloss.NewStyle().Foreground(m.theme.Muted)
		return prompt + muted.Render(m.value)
	}

	var sb strings.Builder
	sb.WriteString(prompt)

	if m.cursor < len(m.value) {
		sb.WriteString(m.value[:m.cursor])
		cursorStyle := lipgloss.NewStyle().Reverse(true)
		sb.WriteString(cursorStyle.Render(string(m.value[m.cursor])))
		sb.WriteString(m.value[m.cursor+1:])
	} else {
		sb.WriteString(m.value)
		cursorStyle := lipgloss.NewStyle().Reverse(true)
		sb.WriteString(cursorStyle.Render(" "))
	}

	return sb.String()
}
