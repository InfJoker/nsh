package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/InfJoker/nsh/internal/llm"
)

// providerSelectState tracks an active provider selection overlay.
type providerSelectState struct {
	providers []llm.ProviderInfo
	cursor    int
}

// newProviderSelectState creates a provider selection overlay with detected providers.
func newProviderSelectState() *providerSelectState {
	return &providerSelectState{
		providers: llm.DetectAvailableProviders(),
	}
}

// selected returns the provider at the cursor, or nil if none.
func (ps *providerSelectState) selected() *llm.ProviderInfo {
	if ps.cursor >= 0 && ps.cursor < len(ps.providers) {
		p := ps.providers[ps.cursor]
		return &p
	}
	return nil
}

// renderProviderSelect renders the provider selection overlay.
func renderProviderSelect(ps *providerSelectState, theme Theme) string {
	if ps == nil {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	availStyle := lipgloss.NewStyle().Foreground(theme.Success)
	unavailStyle := lipgloss.NewStyle().Foreground(theme.Danger)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Select provider:"))
	sb.WriteString("\n")

	for i, p := range ps.providers {
		prefix := "  "
		if i == ps.cursor {
			prefix = "> "
		}

		status := availStyle.Render("available")
		if !p.Available {
			status = unavailStyle.Render("not available")
		}

		name := p.Name
		if i == ps.cursor {
			name = lipgloss.NewStyle().Bold(true).Foreground(theme.Secondary).Render(name)
		}

		sb.WriteString(fmt.Sprintf("%s%-12s %s\n", prefix, name, status))
		if !p.Available && p.Hint != "" && i == ps.cursor {
			sb.WriteString(hintStyle.Render(fmt.Sprintf("    setup: %s", p.Hint)))
			sb.WriteString("\n")
		}
	}

	sb.WriteString(hintStyle.Render("  [Enter] Select  [Esc] Cancel  [↑↓] Navigate"))
	sb.WriteString("\n")

	return sb.String()
}
