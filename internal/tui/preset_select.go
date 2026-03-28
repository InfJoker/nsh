package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/InfJoker/nsh/internal/config"
)

// presetEntry is a single row in the preset picker.
type presetEntry struct {
	Name         string
	ProviderType string // resolved provider type (e.g. "mlx")
	Model        string
	IsActive     bool
}

// presetSelectState tracks the preset selection overlay.
type presetSelectState struct {
	entries []presetEntry
	cursor  int
}

// newPresetSelectState creates a preset picker from the config.
func newPresetSelectState(cfg *config.Config) *presetSelectState {
	presets := cfg.ListPresets()
	if len(presets) == 0 {
		return nil
	}

	var entries []presetEntry
	for name, p := range presets {
		provType := p.Provider
		if cfg.Providers != nil {
			if pc, ok := cfg.Providers[p.Provider]; ok {
				provType = pc.Type
			}
		}
		entries = append(entries, presetEntry{
			Name:         name,
			ProviderType: provType,
			Model:        p.Model,
			IsActive:     name == cfg.ActivePreset,
		})
	}

	// Sort: active first, then alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsActive != entries[j].IsActive {
			return entries[i].IsActive
		}
		return entries[i].Name < entries[j].Name
	})

	return &presetSelectState{entries: entries}
}

// selected returns the preset name at the cursor.
func (ps *presetSelectState) selected() string {
	if ps.cursor >= 0 && ps.cursor < len(ps.entries) {
		return ps.entries[ps.cursor].Name
	}
	return ""
}

// renderPresetSelect renders the preset picker overlay.
func renderPresetSelect(ps *presetSelectState, theme Theme) string {
	if ps == nil {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	activeStyle := lipgloss.NewStyle().Foreground(theme.Success)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Presets:"))
	sb.WriteString("\n")

	for i, e := range ps.entries {
		prefix := "  "
		if i == ps.cursor {
			prefix = "> "
		}

		name := e.Name
		if i == ps.cursor {
			name = lipgloss.NewStyle().Bold(true).Foreground(theme.Secondary).Render(name)
		}

		marker := ""
		if e.IsActive {
			marker = activeStyle.Render("  ✓ active")
		}

		sb.WriteString(fmt.Sprintf("%s%-12s %-10s %s%s\n", prefix, name, e.ProviderType, e.Model, marker))
	}

	sb.WriteString(hintStyle.Render("  [Enter] Switch  [Esc] Cancel  [↑↓] Navigate"))
	sb.WriteString("\n")

	return sb.String()
}
