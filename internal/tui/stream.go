package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// StreamModel displays streaming text from the LLM.
type StreamModel struct {
	content strings.Builder
	done    bool
	theme   Theme
}

// NewStreamModel creates a new stream display.
func NewStreamModel(theme Theme) *StreamModel {
	return &StreamModel{theme: theme}
}

// AddToken appends streaming text.
func (m *StreamModel) AddToken(text string) {
	m.content.WriteString(text)
}

// Done marks the stream as complete.
func (m *StreamModel) Done() {
	m.done = true
}

// Reset clears the stream for a new response.
func (m *StreamModel) Reset() {
	m.content.Reset()
	m.done = false
}

// Content returns the accumulated text.
func (m *StreamModel) Content() string {
	return m.content.String()
}

// IsEmpty returns true if no content has been received.
func (m *StreamModel) IsEmpty() bool {
	return m.content.Len() == 0
}

// View renders the streaming text.
func (m *StreamModel) View() string {
	if m.content.Len() == 0 {
		return ""
	}

	text := m.content.String()
	style := lipgloss.NewStyle().Foreground(m.theme.Text)

	if !m.done {
		// Show cursor at end while streaming
		cursor := lipgloss.NewStyle().Foreground(m.theme.Primary).Render("▊")
		return style.Render(text) + cursor
	}

	return style.Render(text)
}
