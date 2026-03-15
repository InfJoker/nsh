// Package tui implements the bubbletea terminal UI for nsh.
package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme holds the color scheme for the TUI.
type Theme struct {
	Name       string
	Primary    color.Color
	Secondary  color.Color
	Accent     color.Color
	Danger     color.Color
	Muted      color.Color
	Text       color.Color
	Background color.Color
	Success    color.Color
	Warning    color.Color
}

var themes = map[string]Theme{
	"catppuccin": {
		Name:       "catppuccin",
		Primary:    lipgloss.Color("#cba6f7"), // Mauve
		Secondary:  lipgloss.Color("#89b4fa"), // Blue
		Accent:     lipgloss.Color("#f5c2e7"), // Pink
		Danger:     lipgloss.Color("#f38ba8"), // Red
		Muted:      lipgloss.Color("#6c7086"), // Overlay0
		Text:       lipgloss.Color("#cdd6f4"), // Text
		Background: lipgloss.Color("#1e1e2e"), // Base
		Success:    lipgloss.Color("#a6e3a1"), // Green
		Warning:    lipgloss.Color("#f9e2af"), // Yellow
	},
	"dracula": {
		Name:       "dracula",
		Primary:    lipgloss.Color("#bd93f9"),
		Secondary:  lipgloss.Color("#8be9fd"),
		Accent:     lipgloss.Color("#ff79c6"),
		Danger:     lipgloss.Color("#ff5555"),
		Muted:      lipgloss.Color("#6272a4"),
		Text:       lipgloss.Color("#f8f8f2"),
		Background: lipgloss.Color("#282a36"),
		Success:    lipgloss.Color("#50fa7b"),
		Warning:    lipgloss.Color("#f1fa8c"),
	},
	"nord": {
		Name:       "nord",
		Primary:    lipgloss.Color("#88c0d0"),
		Secondary:  lipgloss.Color("#81a1c1"),
		Accent:     lipgloss.Color("#b48ead"),
		Danger:     lipgloss.Color("#bf616a"),
		Muted:      lipgloss.Color("#4c566a"),
		Text:       lipgloss.Color("#eceff4"),
		Background: lipgloss.Color("#2e3440"),
		Success:    lipgloss.Color("#a3be8c"),
		Warning:    lipgloss.Color("#ebcb8b"),
	},
	"monokai": {
		Name:       "monokai",
		Primary:    lipgloss.Color("#ae81ff"),
		Secondary:  lipgloss.Color("#66d9ef"),
		Accent:     lipgloss.Color("#f92672"),
		Danger:     lipgloss.Color("#f92672"),
		Muted:      lipgloss.Color("#75715e"),
		Text:       lipgloss.Color("#f8f8f2"),
		Background: lipgloss.Color("#272822"),
		Success:    lipgloss.Color("#a6e22e"),
		Warning:    lipgloss.Color("#e6db74"),
	},
	"gruvbox": {
		Name:       "gruvbox",
		Primary:    lipgloss.Color("#d3869b"),
		Secondary:  lipgloss.Color("#83a598"),
		Accent:     lipgloss.Color("#fe8019"),
		Danger:     lipgloss.Color("#fb4934"),
		Muted:      lipgloss.Color("#928374"),
		Text:       lipgloss.Color("#ebdbb2"),
		Background: lipgloss.Color("#282828"),
		Success:    lipgloss.Color("#b8bb26"),
		Warning:    lipgloss.Color("#fabd2f"),
	},
	"solarized-dark": {
		Name:       "solarized-dark",
		Primary:    lipgloss.Color("#268bd2"),
		Secondary:  lipgloss.Color("#2aa198"),
		Accent:     lipgloss.Color("#d33682"),
		Danger:     lipgloss.Color("#dc322f"),
		Muted:      lipgloss.Color("#586e75"),
		Text:       lipgloss.Color("#839496"),
		Background: lipgloss.Color("#002b36"),
		Success:    lipgloss.Color("#859900"),
		Warning:    lipgloss.Color("#b58900"),
	},
	"solarized-light": {
		Name:       "solarized-light",
		Primary:    lipgloss.Color("#268bd2"),
		Secondary:  lipgloss.Color("#2aa198"),
		Accent:     lipgloss.Color("#d33682"),
		Danger:     lipgloss.Color("#dc322f"),
		Muted:      lipgloss.Color("#93a1a1"),
		Text:       lipgloss.Color("#657b83"),
		Background: lipgloss.Color("#fdf6e3"),
		Success:    lipgloss.Color("#859900"),
		Warning:    lipgloss.Color("#b58900"),
	},
}

// GetTheme returns a theme by name, falling back to catppuccin.
func GetTheme(name string) Theme {
	if t, ok := themes[name]; ok {
		return t
	}
	return themes["catppuccin"]
}
