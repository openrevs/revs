package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleDirty    = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	styleClean    = lipgloss.NewStyle().Foreground(lipgloss.Color("71"))
	styleFlash    = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleCursor   = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	styleBar      = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236")).Padding(0, 1)
	styleDocFrame = lipgloss.NewStyle().Padding(1, 2)
)

// relTime renders "just now", "5m ago", ... for the status bar.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
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
