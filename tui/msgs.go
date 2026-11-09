package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/tui/workspace"
)

// Async sync operations run in tea.Cmds and come back as these
// messages. Exactly one sync operation is in flight at a time (a.busy).

type pullDoneMsg struct {
	pr  *workspace.PullResult
	err error
}

type pushDoneMsg struct {
	out *workspace.PushOutcome
	err error
}

// takeServerDoneMsg is a resolve-conflict pull; it flashes differently
// from a plain pull (and may report "not yet published").
type takeServerDoneMsg struct {
	pr       *workspace.PullResult
	staleRev int
	err      error
}

// wizardSyncDoneMsg ends the wizard's initial pull.
type wizardSyncDoneMsg struct {
	pr  *workspace.PullResult
	err error
}

func pullCmd(ws *workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		pr, err := ws.Pull(context.Background())
		return pullDoneMsg{pr: pr, err: err}
	}
}

func pushCmd(ws *workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		out, err := ws.Push(context.Background())
		return pushDoneMsg{out: out, err: err}
	}
}

func keepMineCmd(ws *workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		out, err := ws.ResolveKeepMine(context.Background())
		return pushDoneMsg{out: out, err: err}
	}
}

func takeServerCmd(ws *workspace.Workspace, staleRev int) tea.Cmd {
	return func() tea.Msg {
		pr, err := ws.ResolveTakeServer(context.Background(), staleRev)
		return takeServerDoneMsg{pr: pr, staleRev: staleRev, err: err}
	}
}

func rebootstrapCmd(ws *workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		pr, err := ws.Rebootstrap(context.Background())
		return pullDoneMsg{pr: pr, err: err}
	}
}

func wizardSyncCmd(ws *workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		if err := ws.InitClient(); err != nil {
			return wizardSyncDoneMsg{err: err}
		}
		pr, err := ws.Pull(context.Background())
		return wizardSyncDoneMsg{pr: pr, err: err}
	}
}
