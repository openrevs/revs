package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs"
)

func textinputBlink() tea.Cmd { return textinput.Blink }

var titleTypes = []string{"", "movie", "tv"}

// titleForm adds (editIdx == -1) or edits a title's name/year/type.
type titleForm struct {
	editIdx int
	name    textinput.Model
	year    textinput.Model
	typeIdx int
	focus   int // 0 name, 1 year, 2 type
	err     string
}

func newTitleForm(editIdx int, t *revs.Title2) *titleForm {
	f := &titleForm{editIdx: editIdx}

	f.name = textinput.New()
	f.name.Placeholder = "title"
	f.name.CharLimit = 200
	f.name.Width = 40
	f.name.Focus()

	f.year = textinput.New()
	f.year.Placeholder = "year (optional)"
	f.year.CharLimit = 4
	f.year.Width = 12

	if t != nil {
		f.name.SetValue(t.Name)
		if t.Year != nil {
			f.year.SetValue(strconv.Itoa(*t.Year))
		}
		if t.Type != nil {
			for i, v := range titleTypes {
				if v == *t.Type {
					f.typeIdx = i
				}
			}
		}
	}
	return f
}

func (f *titleForm) setFocus(i int) tea.Cmd {
	f.focus = (i + 3) % 3
	f.name.Blur()
	f.year.Blur()
	switch f.focus {
	case 0:
		return f.name.Focus()
	case 1:
		return f.year.Focus()
	}
	return nil
}

// values validates and returns the form's title fields.
func (f *titleForm) values() (string, *int, *string, error) {
	name := strings.TrimSpace(f.name.Value())
	if name == "" {
		return "", nil, nil, fmt.Errorf("name is required")
	}

	var year *int
	if y := strings.TrimSpace(f.year.Value()); y != "" {
		n, err := strconv.Atoi(y)
		if err != nil || n < 1870 || n > 2200 {
			return "", nil, nil, fmt.Errorf("year must be a 4-digit year")
		}
		year = &n
	}

	var typ *string
	if t := titleTypes[f.typeIdx]; t != "" {
		typ = &t
	}
	return name, year, typ, nil
}

func (a *app) updateTitleForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := a.titleForm
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return a.closeTitleForm()
		case "tab", "down":
			return a, f.setFocus(f.focus + 1)
		case "shift+tab", "up":
			return a, f.setFocus(f.focus - 1)
		case "left", "right", " ":
			if f.focus == 2 {
				d := 1
				if key.String() == "left" {
					d = -1
				}
				f.typeIdx = (f.typeIdx + d + len(titleTypes)) % len(titleTypes)
				return a, nil
			}
		case "enter":
			name, year, typ, err := f.values()
			if err != nil {
				f.err = err.Error()
				return a, nil
			}
			if f.editIdx == -1 {
				err = a.ws.AddTitle(name, year, typ)
			} else {
				err = a.ws.UpdateTitle(f.editIdx, name, year, typ)
			}
			if err != nil {
				f.err = err.Error()
				return a, nil
			}
			a.flash = "saved " + name
			return a.closeTitleForm()
		}
	}

	var cmd tea.Cmd
	switch f.focus {
	case 0:
		f.name, cmd = f.name.Update(msg)
	case 1:
		f.year, cmd = f.year.Update(msg)
	}
	return a, cmd
}

func (a *app) closeTitleForm() (tea.Model, tea.Cmd) {
	back := screenList
	if a.titleForm.editIdx != -1 {
		back = screenDetail
	}
	a.titleForm = nil
	a.refreshList()
	a.screen = back
	return a, nil
}

func (f *titleForm) view() string {
	action := "Add title"
	if f.editIdx != -1 {
		action = "Edit title"
	}

	typ := titleTypes[f.typeIdx]
	if typ == "" {
		typ = "(none)"
	}
	typeLine := "  type: < " + typ + " >"
	if f.focus == 2 {
		typeLine = styleCursor.Render("> ") + "type: < " + typ + " >"
	}

	return styleDocFrame.Render(
		styleTitle.Render(action) + "\n\n" +
			f.name.View() + "\n" +
			f.year.View() + "\n" +
			typeLine + "\n\n" +
			errLine(f.err) +
			styleHelp.Render("tab next field · space/←/→ cycle type · enter save · esc cancel"))
}

// reviewForm adds (reviewIdx == -1) or edits one review of a title.
type reviewForm struct {
	titleIdx  int
	reviewIdx int
	date      textinput.Model
	rating    textinput.Model
	comment   textinput.Model
	minRating int
	maxRating int
	focus     int // 0 date, 1 rating, 2 comment
	err       string
}

func newReviewForm(titleIdx, reviewIdx int, r *revs.Review2, user revs.User2) *reviewForm {
	f := &reviewForm{
		titleIdx:  titleIdx,
		reviewIdx: reviewIdx,
		minRating: user.MinRating,
		maxRating: user.MaxRating,
	}

	f.date = textinput.New()
	f.date.Placeholder = "YYYY-MM-DD (optional)"
	f.date.CharLimit = 10
	f.date.Width = 14
	f.date.Focus()

	f.rating = textinput.New()
	f.rating.Placeholder = fmt.Sprintf("%d-%d (blank = no rating)", f.minRating, f.maxRating)
	f.rating.CharLimit = 4
	f.rating.Width = 24

	f.comment = textinput.New()
	f.comment.Placeholder = "comment (optional)"
	f.comment.CharLimit = 2000
	f.comment.Width = 60

	if r == nil {
		f.date.SetValue(time.Now().Format("2006-01-02"))
	} else {
		if r.Date != nil {
			f.date.SetValue(*r.Date)
		}
		if r.Rating != nil {
			f.rating.SetValue(strconv.Itoa(*r.Rating))
		}
		if r.Comment != nil {
			f.comment.SetValue(*r.Comment)
		}
	}
	return f
}

func (f *reviewForm) setFocus(i int) tea.Cmd {
	f.focus = (i + 3) % 3
	f.date.Blur()
	f.rating.Blur()
	f.comment.Blur()
	switch f.focus {
	case 0:
		return f.date.Focus()
	case 1:
		return f.rating.Focus()
	default:
		return f.comment.Focus()
	}
}

// review validates and assembles the Review2 the form describes.
func (f *reviewForm) review() (revs.Review2, error) {
	var r revs.Review2

	if d := strings.TrimSpace(f.date.Value()); d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return r, fmt.Errorf("date must be YYYY-MM-DD")
		}
		r.Date = &d
	}
	if v := strings.TrimSpace(f.rating.Value()); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < f.minRating || n > f.maxRating {
			return r, fmt.Errorf("rating must be %d..%d (or blank)", f.minRating, f.maxRating)
		}
		r.Rating = &n
	}
	if c := f.comment.Value(); strings.TrimSpace(c) != "" {
		r.Comment = &c
	}
	return r, nil
}

func (a *app) updateReviewForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := a.reviewForm
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			a.reviewForm = nil
			a.screen = screenDetail
			return a, nil
		case "tab", "down":
			return a, f.setFocus(f.focus + 1)
		case "shift+tab", "up":
			return a, f.setFocus(f.focus - 1)
		case "enter":
			r, err := f.review()
			if err != nil {
				f.err = err.Error()
				return a, nil
			}
			if f.reviewIdx == -1 {
				err = a.ws.AddReview(f.titleIdx, r)
			} else {
				err = a.ws.UpdateReview(f.titleIdx, f.reviewIdx, r)
			}
			if err != nil {
				f.err = err.Error()
				return a, nil
			}
			a.flash = "review saved"
			a.reviewForm = nil
			a.refreshList()
			a.screen = screenDetail
			return a, nil
		}
	}

	var cmd tea.Cmd
	switch f.focus {
	case 0:
		f.date, cmd = f.date.Update(msg)
	case 1:
		f.rating, cmd = f.rating.Update(msg)
	default:
		f.comment, cmd = f.comment.Update(msg)
	}
	return a, cmd
}

func (f *reviewForm) view() string {
	action := "New review"
	if f.reviewIdx != -1 {
		action = "Edit review"
	}
	return styleDocFrame.Render(
		styleTitle.Render(action) + "\n\n" +
			f.date.View() + "\n" +
			f.rating.View() + "\n" +
			f.comment.View() + "\n\n" +
			errLine(f.err) +
			styleHelp.Render("tab next field · enter save · esc cancel"))
}
