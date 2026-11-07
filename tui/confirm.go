package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func (a *app) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}

	c := a.confirm
	switch key.String() {
	case "y", "enter":
		a.confirm = nil
		a.screen = c.back
		if c.cmd != nil {
			return a.startSync(c.cmd)
		}
		flash, err := c.do()
		if err != nil {
			a.flash = "error: " + err.Error()
		} else {
			a.flash = flash
			a.refreshList()
		}
		return a, nil
	case "n", "esc", "q":
		a.confirm = nil
		a.screen = c.back
		return a, nil
	}
	return a, nil
}

func (a *app) viewConfirm() string {
	return styleDocFrame.Render(
		a.confirm.prompt + "\n\n" +
			styleHelp.Render("y confirm · n cancel"))
}

// The conflict prompt covers both flavors of divergence between the
// draft and the server: a stale push (another device published past
// our base) and a pull that found a newer head under local edits.
func (a *app) updateConflict(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}

	c := a.conflict
	switch key.String() {
	case "t":
		a.conflict = nil
		a.screen = screenList
		return a.startSync(takeServerCmd(a.ws, c.serverRev))
	case "k":
		a.conflict = nil
		a.screen = screenList
		if c.fromPush {
			return a.startSync(keepMineCmd(a.ws))
		}
		a.flash = "keeping local edits — your next push will overwrite the server"
		return a, nil
	case "esc":
		a.conflict = nil
		a.screen = screenList
		a.flash = "conflict unresolved — draft unchanged"
		return a, nil
	}
	return a, nil
}

func (a *app) viewConflict() string {
	c := a.conflict
	source := "While pulling, a newer version of your collection was found"
	keep := "keep mine — keep editing; your next push will overwrite the server"
	if c.fromPush {
		source = "The push was refused: the server holds a newer version of your collection"
		keep = "keep mine — push anyway, overwriting the server version"
	}

	return styleDocFrame.Render(
		styleTitle.Render("sync conflict") + "\n\n" +
			fmt.Sprintf("%s (rev %d, yours is based on rev %d).\n", source, c.serverRev, a.ws.BaseRev()) +
			"This usually means you pushed from another device.\n\n" +
			"  t  take server — replace local draft (a backup is kept)\n" +
			"  k  " + keep + "\n\n" +
			styleHelp.Render("esc decide later"))
}
