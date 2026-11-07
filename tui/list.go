package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/kalafut/dmrs/tui/workspace"
)

// titleItem adapts a TitleRef for bubbles/list.
type titleItem struct {
	ref workspace.TitleRef
}

func (i titleItem) Title() string {
	t := i.ref.Title
	s := t.Name
	if t.Year != nil {
		s += fmt.Sprintf(" (%d)", *t.Year)
	}
	if t.Type != nil && *t.Type != "" {
		s += " [" + *t.Type + "]"
	}
	return s
}

func (i titleItem) Description() string {
	t := i.ref.Title
	latest := workspace.LatestReview(t)
	if latest == nil {
		return "no reviews"
	}
	parts := []string{}
	if latest.Rating != nil {
		parts = append(parts, fmt.Sprintf("rated %d", *latest.Rating))
	} else {
		parts = append(parts, "no rating")
	}
	if latest.Date != nil {
		parts = append(parts, *latest.Date)
	}
	if n := len(t.Reviews); n > 1 {
		parts = append(parts, fmt.Sprintf("%d reviews", n))
	}
	return strings.Join(parts, " · ")
}

func (i titleItem) FilterValue() string { return i.ref.Title.Name }

func (a *app) newList() {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "My movies"
	l.SetShowHelp(true)
	l.DisableQuitKeybindings()
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "push")),
			key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "pull")),
		}
	}
	a.list = l
}

// refreshList rebuilds the items from the workspace, keeping the
// cursor near where it was.
func (a *app) refreshList() {
	sel := a.list.Index()
	items := []list.Item{}
	for _, ref := range a.ws.Titles() {
		items = append(items, titleItem{ref: ref})
	}
	a.list.SetItems(items)
	if sel >= len(items) {
		sel = len(items) - 1
	}
	if sel >= 0 {
		a.list.Select(sel)
	}
}

func (a *app) selectedTitle() (workspace.TitleRef, bool) {
	item, ok := a.list.SelectedItem().(titleItem)
	if !ok {
		return workspace.TitleRef{}, false
	}
	return item.ref, true
}

func (a *app) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && a.list.FilterState() != list.Filtering {
		switch key.String() {
		case "q":
			return a, tea.Quit
		case "enter":
			if ref, ok := a.selectedTitle(); ok {
				a.detailIdx = ref.Index
				a.reviewCur = 0
				a.screen = screenDetail
			}
			return a, nil
		case "a":
			a.titleForm = newTitleForm(-1, nil)
			a.screen = screenTitleForm
			return a, textinput.Blink
		case "d":
			if ref, ok := a.selectedTitle(); ok {
				idx := ref.Index
				a.confirm = &confirmState{
					prompt: fmt.Sprintf("Delete %q and all its reviews?", ref.Title.Name),
					do: func() (string, error) {
						if err := a.ws.DeleteTitle(idx); err != nil {
							return "", err
						}
						return "deleted " + ref.Title.Name, nil
					},
					back: screenList,
				}
				a.screen = screenConfirm
			}
			return a, nil
		case "s":
			return a.startSync(pushCmd(a.ws))
		case "u":
			return a.startSync(pullCmd(a.ws))
		}
	}

	var cmd tea.Cmd
	a.list, cmd = a.list.Update(msg)
	return a, cmd
}

func (a *app) updateLoadError(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	switch key.String() {
	case "r":
		report, err := a.ws.LoadDraft()
		if err != nil {
			a.loadErr = err.Error()
			return a, nil
		}
		a.loadErr = ""
		a.warnings = report.Warnings
		a.refreshList()
		a.screen = screenList
		return a, nil
	case "q", "esc":
		return a, tea.Quit
	}
	return a, nil
}

func (a *app) viewLoadError() string {
	return styleDocFrame.Render(
		styleTitle.Render("draft problem") + "\n\n" +
			"The draft file could not be loaded:\n\n  " +
			a.ws.DraftPath() + "\n\n" +
			styleError.Render(a.loadErr) + "\n\n" +
			"Fix the file in your editor, then:\n\n" +
			"  r  reload\n" +
			"  q  quit\n")
}
