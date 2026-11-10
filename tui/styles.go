package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	styleBrand     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Padding(0, 1)
	styleTab       = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	styleTabActive = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Bold(true).Padding(0, 1)
	styleCrumb     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleTitle     = lipgloss.NewStyle().Bold(true)
	styleHeading   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleHelp      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleOK        = lipgloss.NewStyle().Foreground(lipgloss.Color("71"))
	styleError     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleWarn      = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleStars     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	styleTag       = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("241")).Padding(0, 1)
	styleBanner    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("203")).Padding(0, 1)
)

// truncate shortens s to at most w cells.
func truncate(s string, w int) string {
	return ansi.Truncate(s, max(w, 0), "…")
}

// pad truncates or pads s to exactly w cells.
func pad(s string, w int) string {
	s = truncate(s, w)
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// wrap word-wraps s to w cells.
func wrap(s string, w int) string {
	return ansi.Wrap(s, max(w, 10), "")
}

// indent prefixes every line of s.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
