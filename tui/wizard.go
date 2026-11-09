package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// The first-run wizard: server URL (skipped when configured) → key
// import-or-generate (skipped when present) → initial pull, seeding
// the draft from a published head when one exists.

func (a *app) updateWizardServer(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "enter":
			url := strings.TrimSpace(a.serverInput.Value())
			if err := a.ws.SetServer(url); err != nil {
				a.wizardErr = err.Error()
				return a, nil
			}
			a.wizardErr = ""
			if a.ws.NeedsKey() {
				a.screen = screenWizardKey
				return a, nil
			}
			return a.enterWizardSync()
		case "esc":
			return a, tea.Quit
		}
	}
	var cmd tea.Cmd
	a.serverInput, cmd = a.serverInput.Update(msg)
	return a, cmd
}

func (a *app) viewWizardServer() string {
	return styleDocFrame.Render(
		styleTitle.Render("revs — first run") + "\n\n" +
			"Server URL:\n" + a.serverInput.View() + "\n\n" +
			errLine(a.wizardErr) +
			styleHelp.Render("enter continue · esc quit"))
}

func (a *app) updateWizardKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if a.keyImport {
			var cmd tea.Cmd
			a.keyInput, cmd = a.keyInput.Update(msg)
			return a, cmd
		}
		return a, nil
	}

	if a.keyImport {
		switch key.String() {
		case "enter":
			uid, err := a.ws.ImportKey(strings.TrimSpace(a.keyInput.Value()))
			if err != nil {
				a.wizardErr = err.Error()
				return a, nil
			}
			a.wizardErr = ""
			a.flash = "imported identity " + uid[:8]
			return a.enterWizardSync()
		case "esc":
			a.keyImport = false
			a.wizardErr = ""
			return a, nil
		}
		var cmd tea.Cmd
		a.keyInput, cmd = a.keyInput.Update(msg)
		return a, cmd
	}

	switch key.String() {
	case "g":
		uid, err := a.ws.GenerateKey()
		if err != nil {
			a.wizardErr = err.Error()
			return a, nil
		}
		a.flash = "generated identity " + uid[:8]
		return a.enterWizardSync()
	case "i":
		a.keyImport = true
		a.keyInput.Focus()
		return a, textinput.Blink
	case "esc", "q":
		return a, tea.Quit
	}
	return a, nil
}

func (a *app) viewWizardKey() string {
	head := styleTitle.Render("revs — identity") + "\n\n"
	if a.keyImport {
		return styleDocFrame.Render(head +
			"Paste your private key (hex):\n" + a.keyInput.View() + "\n\n" +
			errLine(a.wizardErr) +
			styleHelp.Render("enter import · esc back"))
	}
	return styleDocFrame.Render(head +
		"No signing key found in this data directory.\n\n" +
		"  g  generate a new identity\n" +
		"  i  import an existing private key\n\n" +
		errLine(a.wizardErr) +
		styleHelp.Render("esc quit"))
}

func (a *app) enterWizardSync() (tea.Model, tea.Cmd) {
	a.screen = screenWizardSync
	a.wizardErr = ""
	return a, tea.Batch(a.spin.Tick, wizardSyncCmd(a.ws))
}

func (a *app) viewWizardSync() string {
	if a.wizardErr != "" {
		return styleDocFrame.Render(
			styleTitle.Render("revs — first sync") + "\n\n" +
				styleError.Render("sync failed: "+a.wizardErr) + "\n\n" +
				"  r  retry\n" +
				"  c  continue offline with an empty collection\n" +
				"  q  quit\n")
	}
	return styleDocFrame.Render(
		styleTitle.Render("revs — first sync") + "\n\n" +
			a.spin.View() + " contacting server...")
}

func (a *app) updateWizardSyncDone(msg wizardSyncDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.wizardErr = msg.err.Error()
		return a, nil
	}

	a.wizardErr = ""
	if !a.ws.HasDraft() {
		// No published document for this user: start empty.
		if err := a.ws.NewEmptyDraft(); err != nil {
			a.fatalErr = err.Error()
			a.screen = screenFatal
			return a, nil
		}
		a.flash = "no published collection found — starting empty"
	} else if msg.pr != nil && msg.pr.Head != nil {
		a.flash = fmt.Sprintf("loaded your collection at rev %d", msg.pr.Head.Rev)
	}
	a.refreshList()
	a.screen = screenList
	return a, nil
}

// wizard sync retry/offline keys are handled here because the sync
// screen otherwise ignores input.
func (a *app) updateWizardSyncKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "r":
		return a.enterWizardSync()
	case "c":
		if err := a.ws.NewEmptyDraft(); err != nil {
			a.fatalErr = err.Error()
			a.screen = screenFatal
			return a, nil
		}
		a.refreshList()
		a.screen = screenList
		a.flash = "offline — starting empty; pull once the server is reachable"
		return a, nil
	case "q", "esc":
		return a, tea.Quit
	}
	return a, nil
}

func errLine(s string) string {
	if s == "" {
		return ""
	}
	return styleError.Render(s) + "\n\n"
}
