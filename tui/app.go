package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/tui/workspace"
)

type screen int

const (
	screenWizardServer screen = iota
	screenWizardKey
	screenWizardSync
	screenLoadError
	screenList
	screenDetail
	screenTitleForm
	screenReviewForm
	screenConfirm
	screenConflict
	screenFatal
)

// confirmState is a yes/no prompt. Exactly one of do (synchronous
// mutation, returns the flash message) or cmd (async operation) is set.
type confirmState struct {
	prompt string
	do     func() (string, error)
	cmd    tea.Cmd
	back   screen
}

// conflictState runs the take-server/keep-mine prompt, for both a
// stale push and a pull that found a newer head under local edits.
type conflictState struct {
	fromPush  bool
	serverRev int
}

type app struct {
	ws     *workspace.Workspace
	screen screen
	width  int
	height int

	// wizard
	serverInput textinput.Model
	keyInput    textinput.Model
	keyImport   bool
	spin        spinner.Model
	wizardErr   string

	// main screens
	list       list.Model
	detailIdx  int // underlying title index shown in detail
	reviewCur  int // cursor into displayReviews order
	titleForm  *titleForm
	reviewForm *reviewForm
	confirm    *confirmState
	conflict   *conflictState

	loadErr  string
	fatalErr string
	warnings []string
	flash    string
	busy     bool
}

func newApp(ws *workspace.Workspace) *app {
	a := &app{ws: ws}

	a.serverInput = textinput.New()
	a.serverInput.Placeholder = "http://localhost:8080"
	a.serverInput.CharLimit = 200
	a.serverInput.Width = 50

	a.keyInput = textinput.New()
	a.keyInput.Placeholder = "base64 private key from `revs keygen`"
	a.keyInput.CharLimit = 200
	a.keyInput.Width = 70

	a.spin = spinner.New(spinner.WithSpinner(spinner.Dot))

	a.newList()

	switch {
	case ws.NeedsServer():
		a.screen = screenWizardServer
		a.serverInput.Focus()
	case ws.NeedsKey():
		a.screen = screenWizardKey
	default:
		if err := ws.InitClient(); err != nil {
			a.fatalErr = err.Error()
			a.screen = screenFatal
			return a
		}
		if !ws.DraftExists() {
			a.screen = screenWizardSync
			return a
		}
		report, err := ws.LoadDraft()
		if err != nil {
			a.loadErr = err.Error()
			a.screen = screenLoadError
			return a
		}
		a.warnings = report.Warnings
		a.refreshList()
		a.screen = screenList
	}
	return a
}

func (a *app) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if a.screen == screenWizardSync {
		cmds = append(cmds, a.spin.Tick, wizardSyncCmd(a.ws))
	}
	return tea.Batch(cmds...)
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.list.SetSize(msg.Width, msg.Height-2)
		return a, nil

	case spinner.TickMsg:
		if a.busy || a.screen == screenWizardSync {
			var cmd tea.Cmd
			a.spin, cmd = a.spin.Update(msg)
			return a, cmd
		}
		return a, nil

	case wizardSyncDoneMsg:
		return a.updateWizardSyncDone(msg)
	case pullDoneMsg:
		return a.updatePullDone(msg)
	case pushDoneMsg:
		return a.updatePushDone(msg)
	case takeServerDoneMsg:
		return a.updateTakeServerDone(msg)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	}

	switch a.screen {
	case screenWizardServer:
		return a.updateWizardServer(msg)
	case screenWizardKey:
		return a.updateWizardKey(msg)
	case screenWizardSync:
		if key, ok := msg.(tea.KeyMsg); ok && a.wizardErr != "" {
			return a.updateWizardSyncKeys(key.String())
		}
		return a, nil
	case screenLoadError:
		return a.updateLoadError(msg)
	case screenList:
		return a.updateList(msg)
	case screenDetail:
		return a.updateDetail(msg)
	case screenTitleForm:
		return a.updateTitleForm(msg)
	case screenReviewForm:
		return a.updateReviewForm(msg)
	case screenConfirm:
		return a.updateConfirm(msg)
	case screenConflict:
		return a.updateConflict(msg)
	case screenFatal:
		if _, ok := msg.(tea.KeyMsg); ok {
			return a, tea.Quit
		}
	}
	return a, nil
}

func (a *app) View() string {
	var body string
	switch a.screen {
	case screenWizardServer:
		body = a.viewWizardServer()
	case screenWizardKey:
		body = a.viewWizardKey()
	case screenWizardSync:
		body = a.viewWizardSync()
	case screenLoadError:
		body = a.viewLoadError()
	case screenList:
		body = a.list.View()
	case screenDetail:
		body = a.viewDetail()
	case screenTitleForm:
		body = a.titleForm.view()
	case screenReviewForm:
		body = a.reviewForm.view()
	case screenConfirm:
		body = a.viewConfirm()
	case screenConflict:
		body = a.viewConflict()
	case screenFatal:
		return styleDocFrame.Render(
			styleError.Render("fatal: "+a.fatalErr) + "\n\n" +
				styleHelp.Render("press any key to quit"))
	}
	return body + "\n" + a.statusBar()
}

// statusBar is the always-visible one-liner: identity, base revision,
// dirty state, last sync, and the most recent event flash.
func (a *app) statusBar() string {
	if a.screen <= screenWizardSync {
		return ""
	}

	parts := []string{}
	if id := a.ws.UserID(); id != "" {
		parts = append(parts, "user "+id[:8])
	}
	if s := a.ws.ServerURL(); s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, fmt.Sprintf("rev %d", a.ws.BaseRev()))
	if a.ws.Dirty() {
		parts = append(parts, styleDirty.Render("unpushed edits"))
	} else {
		parts = append(parts, styleClean.Render("clean"))
	}
	parts = append(parts, "synced "+relTime(a.ws.LastSync()))

	bar := styleBar.Render(strings.Join(parts, "  ·  "))

	line := ""
	switch {
	case a.busy:
		line = a.spin.View() + " working..."
	case a.flash != "":
		line = styleFlash.Render(a.flash)
	case len(a.warnings) > 0:
		line = styleWarn.Render(fmt.Sprintf("⚠ %d draft warning(s): %s", len(a.warnings), a.warnings[0]))
	}
	if line == "" {
		return bar
	}
	return line + "\n" + bar
}

// startSync guards the single in-flight sync operation.
func (a *app) startSync(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if a.busy {
		return a, nil
	}
	a.busy = true
	a.flash = ""
	return a, tea.Batch(a.spin.Tick, cmd)
}

func (a *app) updatePullDone(msg pullDoneMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	if msg.err != nil {
		return a.handleSyncError("pull", msg.err)
	}

	pr := msg.pr
	switch {
	case pr.DirtyConflict:
		a.conflict = &conflictState{fromPush: false, serverRev: pr.Head.Rev}
		a.screen = screenConflict
	case pr.Seeded:
		a.refreshList()
		a.screen = screenList
		a.flash = fmt.Sprintf("updated to rev %d", pr.Head.Rev)
	default:
		a.flash = fmt.Sprintf("up to date (epoch %d)", pr.Sync.Epoch)
	}
	return a, nil
}

func (a *app) updatePushDone(msg pushDoneMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	if msg.err != nil {
		return a.handleSyncError("push", msg.err)
	}

	if msg.out.Stale != nil {
		a.conflict = &conflictState{fromPush: true, serverRev: msg.out.Stale.LatestRev}
		a.screen = screenConflict
		return a, nil
	}
	if msg.out.Result.Pushed {
		a.flash = fmt.Sprintf("pushed rev %d", msg.out.Result.Rev)
	} else {
		a.flash = fmt.Sprintf("already up to date (rev %d)", msg.out.Result.Rev)
	}
	a.refreshList()
	return a, nil
}

func (a *app) updateTakeServerDone(msg takeServerDoneMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	if msg.err != nil {
		return a.handleSyncError("pull", msg.err)
	}

	if !msg.pr.Seeded {
		a.flash = fmt.Sprintf("server hasn't published rev %d yet — try pull later, or keep mine", msg.staleRev)
		a.screen = screenList
		return a, nil
	}
	a.refreshList()
	a.screen = screenList
	a.flash = fmt.Sprintf("replaced local draft with server rev %d (backup kept)", msg.pr.Head.Rev)
	return a, nil
}

// handleSyncError sorts protocol errors into their UX: divergence
// offers a re-bootstrap, a changed server key is fatal, the rest flash.
func (a *app) handleSyncError(op string, err error) (tea.Model, tea.Cmd) {
	var diverged *client.DivergedError
	var keyChanged *client.KeyChangedError
	var rejected *client.RejectedError

	switch {
	case errors.As(err, &keyChanged):
		a.fatalErr = "server signing key changed — possible impersonation, refusing to sync: " + err.Error()
		a.screen = screenFatal
	case errors.As(err, &diverged):
		a.confirm = &confirmState{
			prompt: "Server history diverged from local sync state.\nRe-bootstrap from the latest snapshot? Your draft is not touched.",
			cmd:    rebootstrapCmd(a.ws),
			back:   screenList,
		}
		a.screen = screenConfirm
	case errors.As(err, &rejected):
		a.flash = "server rejected the upload: " + rejected.Reason
	default:
		a.flash = op + " failed: " + err.Error()
	}
	return a, nil
}
