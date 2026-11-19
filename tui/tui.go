// Package tui is the terminal UI behind `revs tui`: a full-screen front
// end over a collection.Collection, with the same data and features as
// the web UI (`revs ui`). Both can run against the same data directory
// at once.
//
// The UI is a stack of screens under five tabs (movies, history,
// servers, identity, data). Esc goes back; prompts and confirmations
// appear at the bottom of the screen. Network operations run in the
// background, one at a time, with a spinner in the status line. The
// shown screen reloads when the collection changes, including changes
// made by another process.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/openrevs/revs/collection"
)

type Options struct {
	DataDir string
}

// Run opens the collection and drives the program until quit.
func Run(opts Options) error {
	c, err := collection.Open(collection.Options{DataDir: opts.DataDir})
	if err != nil {
		return err
	}
	defer c.Close()

	_, err = tea.NewProgram(newApp(c), tea.WithAltScreen()).Run()
	return err
}

// networkTimeout bounds a push, pull, or search.
const networkTimeout = 2 * time.Minute

// watchInterval is how often the app checks for changes made elsewhere,
// such as in `revs ui` running on the same data directory.
const watchInterval = time.Second

// screen is one page of the UI.
type screen interface {
	// title is the screen's breadcrumb.
	title() string
	// load (re)reads the screen's data; it is called when the screen
	// is shown and whenever it becomes the top screen again.
	load(a *app) error
	// update handles a message: key presses not taken by the app or a
	// modal, and other messages such as cursor blinks.
	update(a *app, msg tea.Msg) tea.Cmd
	view(a *app, width, height int) string
	// help lists the screen's keys for the footer.
	help() string
}

// typing is implemented by screens that may have a text field focused;
// while one does, single-letter global keys go to the screen.
type typing interface {
	typing() bool
}

type tab struct {
	key  string
	name string
	root func() screen
}

var tabs = []tab{
	{"1", "Movies & TV", func() screen { return &moviesScreen{} }},
	{"2", "History", func() screen { return &historyScreen{} }},
	{"3", "Servers", func() screen { return &serversScreen{} }},
	{"4", "Identity", func() screen { return &identityScreen{} }},
	{"5", "Data", func() screen { return &dataScreen{} }},
}

type app struct {
	c   *collection.Collection
	ctx context.Context

	tab   int
	stack []screen

	width, height int

	prompt  *prompt
	confirm *confirm

	version int64 // the collection's DataVersion when last shown

	busy    string // label of the running background operation
	syncOps bool   // run operations synchronously, without a spinner (for tests)
	spin    spinner.Model
	flash   string
	isErr   bool
}

func newApp(c *collection.Collection) *app {
	a := &app{c: c, ctx: context.Background(), spin: spinner.New(spinner.WithSpinner(spinner.Dot))}
	a.version, _ = c.DataVersion(a.ctx)
	a.switchTab(0)
	if !c.HasKey(a.ctx) {
		a.info("Welcome! Press 4 to create or import a signing key before publishing.")
	}
	return a
}

func (a *app) Init() tea.Cmd { return a.watch() }

// versionMsg reports the collection's DataVersion.
type versionMsg struct {
	v   int64
	err error
}

// watch checks the collection's version after watchInterval.
func (a *app) watch() tea.Cmd {
	c := a.c
	return tea.Tick(watchInterval, func(time.Time) tea.Msg {
		v, err := c.DataVersion(context.Background())
		return versionMsg{v: v, err: err}
	})
}

func (a *app) top() screen { return a.stack[len(a.stack)-1] }

func (a *app) switchTab(i int) {
	a.tab = i
	a.stack = nil
	a.push(tabs[i].root())
}

// push shows a new screen on top of the current one.
func (a *app) push(s screen) {
	a.stack = append(a.stack, s)
	a.reload()
}

// replace swaps the top screen for s.
func (a *app) replace(s screen) {
	a.stack[len(a.stack)-1] = s
	a.reload()
}

// pop returns to the previous screen.
func (a *app) pop() {
	if len(a.stack) > 1 {
		a.stack = a.stack[:len(a.stack)-1]
		a.reload()
	}
}

// reload refreshes the top screen's data, popping screens whose subject
// is gone (e.g. a movie deleted from the other UI).
func (a *app) reload() {
	for {
		err := a.top().load(a)
		if err == nil {
			return
		}
		if errors.Is(err, collection.ErrNotFound) {
			err = fmt.Errorf("%s was deleted, perhaps in another window", a.top().title())
		}
		a.fail(err)
		if len(a.stack) == 1 {
			return
		}
		a.stack = a.stack[:len(a.stack)-1]
	}
}

func (a *app) info(msg string) { a.flash, a.isErr = msg, false }

func (a *app) fail(err error) {
	a.flash, a.isErr = capitalize(err.Error()), true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// doneMsg finishes a background operation on the UI goroutine.
type doneMsg struct {
	apply func(a *app) tea.Cmd
}

// run starts a background operation. work runs off the UI goroutine
// and must not touch the app's state; the function it returns is applied to
// the app once it finishes; work may use a.c, which is safe for
// concurrent use. Only one operation runs at a time.
func (a *app) run(label string, work func(ctx context.Context) func(a *app) tea.Cmd) tea.Cmd {
	if a.busy != "" {
		a.fail(fmt.Errorf("still %s", a.busy))
		return nil
	}
	a.busy = label
	a.flash = ""
	ctx := a.ctx
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, networkTimeout)
		defer cancel()
		return doneMsg{apply: work(ctx)}
	}
	if a.syncOps {
		msg := cmd()
		return func() tea.Msg { return msg }
	}
	return tea.Batch(a.spin.Tick, cmd)
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case spinner.TickMsg:
		if a.busy == "" {
			return a, nil
		}
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		return a, cmd

	case versionMsg:
		// Reload when the data changed, here or elsewhere; but not under
		// a prompt or question, which may be about what is shown. The
		// next check catches up once it's closed.
		if msg.err == nil && msg.v != a.version && a.prompt == nil && a.confirm == nil {
			a.version = msg.v
			a.reload()
		}
		return a, a.watch()

	case doneMsg:
		a.busy = ""
		return a, msg.apply(a)

	case tea.KeyMsg:
		// Keys typed faster than they are read arrive together; handle
		// them one at a time so each can be a command.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
			var cmds []tea.Cmd
			for _, r := range msg.Runes {
				_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt})
				cmds = append(cmds, cmd)
			}
			return a, tea.Batch(cmds...)
		}
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		if a.confirm != nil {
			return a, a.updateConfirm(msg)
		}
		if a.prompt != nil {
			return a, a.updatePrompt(msg)
		}
		if t, ok := a.top().(typing); !ok || !t.typing() {
			k := msg.String()
			for i, t := range tabs {
				if k == t.key {
					a.switchTab(i)
					return a, nil
				}
			}
			switch k {
			case "q":
				return a, tea.Quit
			case "esc":
				a.pop()
				return a, nil
			}
		}
		// Any other key dismisses the last message.
		if a.busy == "" {
			a.flash = ""
		}
	}

	if a.prompt != nil {
		var cmd tea.Cmd
		a.prompt.input, cmd = a.prompt.input.Update(msg)
		return a, cmd
	}
	return a, a.top().update(a, msg)
}

func (a *app) View() string {
	if a.width == 0 {
		return ""
	}
	header := a.header()
	footer := a.footer()
	h := a.height - lipgloss.Height(header) - lipgloss.Height(footer)
	body := a.top().view(a, a.width-4, max(h-1, 1))
	body = lipgloss.NewStyle().Padding(0, 2).Height(max(h, 1)).MaxHeight(max(h, 1)).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (a *app) header() string {
	var parts []string
	for i, t := range tabs {
		label := t.key + " " + t.name
		if i == a.tab {
			parts = append(parts, styleTabActive.Render(label))
		} else {
			parts = append(parts, styleTab.Render(label))
		}
	}
	left := styleBrand.Render("revs") + " " + strings.Join(parts, "")
	right := styleDim.Render("no key")
	if id := a.c.UserID(a.ctx); id != "" {
		right = styleDim.Render("you: " + collection.ShortID(id))
	}
	gap := max(a.width-lipgloss.Width(left)-lipgloss.Width(right)-1, 1)
	line := left + strings.Repeat(" ", gap) + right

	var crumbs []string
	for _, s := range a.stack {
		crumbs = append(crumbs, s.title())
	}
	crumb := styleCrumb.Render(truncate(strings.Join(crumbs, " › "), a.width-4))
	return line + "\n\n  " + crumb + "\n"
}

func (a *app) footer() string {
	var lines []string
	switch {
	case a.confirm != nil:
		lines = append(lines, a.viewConfirm())
	case a.prompt != nil:
		lines = append(lines, a.viewPrompt())
	}

	switch {
	case a.busy != "":
		lines = append(lines, a.spin.View()+" "+a.busy+"…")
	case a.flash != "" && a.isErr:
		lines = append(lines, styleError.Render(wrap(a.flash, a.width-2)))
	case a.flash != "":
		lines = append(lines, styleOK.Render(wrap(a.flash, a.width-2)))
	default:
		lines = append(lines, "")
	}

	help := a.top().help()
	if a.confirm == nil && a.prompt == nil {
		nav := "1-5 tabs · q quit"
		if len(a.stack) > 1 {
			nav = "esc back · " + nav
		}
		if t, ok := a.top().(typing); ok && t.typing() {
			nav = "ctrl+c quit"
		}
		if help != "" {
			help += " · "
		}
		lines = append(lines, styleHelp.Render(wrap(help+nav, a.width-2)))
	}
	return " " + strings.Join(lines, "\n ")
}

// ---- prompt: one line of text input at the bottom ----

type prompt struct {
	label    string
	input    textinput.Model
	onSubmit func(a *app, value string) tea.Cmd
}

// ask prompts for a line of text.
func (a *app) ask(label, initial string, onSubmit func(a *app, value string) tea.Cmd) tea.Cmd {
	in := textinput.New()
	in.SetValue(initial)
	in.Width = max(a.width-lipgloss.Width(label)-6, 20)
	in.CharLimit = 4096
	cmd := in.Focus()
	a.prompt = &prompt{label: label, input: in, onSubmit: onSubmit}
	return cmd
}

func (a *app) updatePrompt(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.prompt = nil
		return nil
	case "enter":
		p := a.prompt
		a.prompt = nil
		return p.onSubmit(a, strings.TrimSpace(p.input.Value()))
	}
	var cmd tea.Cmd
	a.prompt.input, cmd = a.prompt.input.Update(msg)
	return cmd
}

func (a *app) viewPrompt() string {
	return styleTitle.Render(a.prompt.label) + " " + a.prompt.input.View() + "\n " +
		styleHelp.Render("enter ok · esc cancel")
}

// ---- confirm: a yes/no question at the bottom ----

type confirm struct {
	question string
	onYes    func(a *app) tea.Cmd
}

// ask asks a yes/no question, running onYes on yes.
func (a *app) confirmThen(question string, onYes func(a *app) tea.Cmd) {
	a.confirm = &confirm{question: question, onYes: onYes}
}

func (a *app) updateConfirm(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		c := a.confirm
		a.confirm = nil
		return c.onYes(a)
	case "n", "N", "esc", "q":
		a.confirm = nil
	}
	return nil
}

func (a *app) viewConfirm() string {
	return styleWarn.Render(wrap(a.confirm.question, a.width-2)) + "\n " +
		styleHelp.Render("y yes · n no")
}
