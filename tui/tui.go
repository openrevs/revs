// Package tui is the terminal UI for a personal revs movie collection:
// a Bubble Tea front end over tui/workspace, which owns all state and
// sync logic. Screens: a first-run wizard (server → key → initial
// pull), a filterable title list, a title detail view with the full
// review history, add/edit forms, and confirm/conflict prompts.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/tui/workspace"
)

// Run opens the workspace and drives the program until quit. Draft
// load problems become an in-app error screen; only environmental
// failures (unusable data dir, bad key file) error out here.
func Run(opts workspace.Options) error {
	ws, err := workspace.Open(opts)
	if err != nil {
		return err
	}

	p := tea.NewProgram(newApp(ws), tea.WithAltScreen())
	_, err = p.Run()
	return err
}
