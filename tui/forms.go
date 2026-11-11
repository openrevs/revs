package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/collection/localdb"
)

// pushForm shows a screen with a focused text field.
func (a *app) pushForm(s screen) tea.Cmd {
	a.push(s)
	return textinput.Blink
}

// ---- generic form ----

// field is one labelled input: a line (input) or a text area (area).
type field struct {
	label string
	hint  string
	input *textinput.Model
	area  *textarea.Model
}

type form struct {
	fields []field
	focus  int
	err    string
}

func lineField(label, value, hint string, width int) field {
	in := textinput.New()
	in.SetValue(value)
	in.CharLimit = 500
	in.Width = width
	in.Prompt = ""
	return field{label: label, hint: hint, input: &in}
}

func areaField(label, value, hint string, width, height int) field {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "│ "
	ta.CharLimit = 0
	ta.SetWidth(width)
	ta.SetHeight(height)
	ta.SetValue(value)
	return field{label: label, hint: hint, area: &ta}
}

func (f *form) value(i int) string {
	if f.fields[i].area != nil {
		return f.fields[i].area.Value()
	}
	return f.fields[i].input.Value()
}

func (f *form) set(i int, v string) {
	if f.fields[i].area != nil {
		f.fields[i].area.SetValue(v)
	} else {
		f.fields[i].input.SetValue(v)
	}
}

func (f *form) setFocus(i int) tea.Cmd {
	f.focus = (i + len(f.fields)) % len(f.fields)
	var cmd tea.Cmd
	for j, fl := range f.fields {
		switch {
		case fl.input != nil && j == f.focus:
			cmd = fl.input.Focus()
		case fl.input != nil:
			fl.input.Blur()
		case fl.area != nil && j == f.focus:
			cmd = fl.area.Focus()
		case fl.area != nil:
			fl.area.Blur()
		}
	}
	return cmd
}

// update handles a message, reporting whether the user asked to save:
// ctrl+s anywhere, or enter in the last line field.
func (f *form) update(msg tea.Msg) (save bool, cmd tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		inArea := f.fields[f.focus].area != nil
		switch k.String() {
		case "ctrl+s":
			return true, nil
		case "tab":
			return false, f.setFocus(f.focus + 1)
		case "shift+tab":
			return false, f.setFocus(f.focus - 1)
		case "down":
			if !inArea {
				return false, f.setFocus(f.focus + 1)
			}
		case "up":
			if !inArea {
				return false, f.setFocus(f.focus - 1)
			}
		case "enter":
			if !inArea {
				if f.focus == len(f.fields)-1 {
					return true, nil
				}
				return false, f.setFocus(f.focus + 1)
			}
		}
	}

	fl := f.fields[f.focus]
	if fl.area != nil {
		*fl.area, cmd = fl.area.Update(msg)
	} else {
		*fl.input, cmd = fl.input.Update(msg)
	}
	return false, cmd
}

func (f *form) view() string {
	labelW := 0
	for _, fl := range f.fields {
		labelW = max(labelW, len(fl.label))
	}
	var b strings.Builder
	for i, fl := range f.fields {
		label := pad(fl.label, labelW)
		if i == f.focus {
			label = styleSelected.Render(label)
		} else {
			label = styleDim.Render(label)
		}
		if fl.area != nil {
			b.WriteString(label + "\n" + fl.area.View() + "\n")
		} else {
			b.WriteString(label + "  " + fl.input.View() + "\n")
		}
		if fl.hint != "" && i == f.focus {
			b.WriteString(strings.Repeat(" ", labelW+2) + styleDim.Render(fl.hint) + "\n")
		}
		b.WriteString("\n")
	}
	if f.err != "" {
		b.WriteString(styleError.Render(f.err) + "\n")
	}
	return b.String()
}

const formHelp = "tab next field · ctrl+s save · esc cancel"

// ---- movie form ----

const (
	fTitle = iota
	fYear
	fIMDB
	fTMDB
	fWikidata
)

type movieForm struct {
	movie *localdb.Movie // nil when adding
	form
}

func newMovieForm(m *localdb.Movie, mf collection.MovieFields) *movieForm {
	f := &movieForm{movie: m}
	f.fields = []field{
		lineField("Title", mf.Title, "", 50),
		lineField("Year", mf.Year, "optional", 6),
		lineField("IMDb", mf.IMDB, "optional: an id like tt0110912, or an IMDb URL", 50),
		lineField("TMDB", mf.TMDB, "optional: an id like 680, or a TMDB URL", 50),
		lineField("Wikidata", mf.Wikidata, "optional: an id like Q104123, or a Wikidata URL", 50),
	}
	f.setFocus(0)
	return f
}

func (s *movieForm) title() string {
	if s.movie == nil {
		return "Add a movie"
	}
	return "Edit"
}

func (s *movieForm) load(a *app) error { return nil }
func (s *movieForm) typing() bool      { return true }
func (s *movieForm) help() string      { return formHelp + " · ctrl+f look up on Wikidata" }

func (s *movieForm) movieFields() collection.MovieFields {
	return collection.MovieFields{
		Title: s.value(fTitle), Year: s.value(fYear),
		IMDB: s.value(fIMDB), TMDB: s.value(fTMDB), Wikidata: s.value(fWikidata),
	}
}

func (s *movieForm) update(a *app, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			a.pop()
			return nil
		case "ctrl+f":
			return a.pushForm(newSearchScreen(s.value(fTitle), func(a *app, r collection.SearchResult) {
				mf := collection.MovieFieldsFromResult(r)
				for i, v := range []string{mf.Title, mf.Year, mf.IMDB, mf.TMDB, mf.Wikidata} {
					s.set(i, v)
				}
				a.pop()
				a.info("Filled in from Wikidata. Save to keep it.")
			}))
		}
	}

	save, cmd := s.form.update(msg)
	if !save {
		return cmd
	}
	mf := s.movieFields()
	in, err := mf.Parse()
	if err != nil {
		s.err = capitalize(strings.ReplaceAll(err.Error(), "\n", "; "))
		return nil
	}
	if s.movie != nil {
		if err := a.c.UpdateMovie(a.ctx, s.movie.ID, in); err != nil {
			s.err = err.Error()
			return nil
		}
		a.info("Saved.")
		a.pop()
		return nil
	}
	return a.addMovie(in)
}

// addMovie adds a movie and shows it in place of the top screen.
func (a *app) addMovie(in collection.MovieInput) tea.Cmd {
	m, existed, err := a.c.AddMovie(a.ctx, in)
	if err != nil {
		a.fail(err)
		return nil
	}
	if existed {
		a.info(fmt.Sprintf("%s is already in your collection.", m.Title))
	} else {
		a.info(fmt.Sprintf("Added %s. Press v to add a viewing.", m.Title))
	}
	a.replace(&movieScreen{id: m.ID})
	return nil
}

func (s *movieForm) view(a *app, width, height int) string {
	return s.form.view()
}

// ---- viewing form ----

const (
	fDate = iota
	fRating
	fReview
)

type viewingForm struct {
	movie   localdb.Movie
	viewing *localdb.Viewing // nil when adding
	form
}

func newViewingForm(m localdb.Movie, v *localdb.Viewing) *viewingForm {
	vf := collection.NewViewingFields()
	if v != nil {
		vf = collection.ViewingFieldsFrom(*v)
	}
	if vf.Rating == "none" {
		vf.Rating = ""
	}
	f := &viewingForm{movie: m, viewing: v}
	f.fields = []field{
		lineField("Date", vf.Date, "YYYY-MM-DD, or blank if you don't remember", 12),
		lineField("Rating", vf.Rating, fmt.Sprintf("0–%d stars in steps of ½ (e.g. 3.5), or blank for no rating", collection.MaxStars), 3),
		areaField("Review", vf.Review, "", 70, 8),
	}
	f.setFocus(0)
	return f
}

func (s *viewingForm) title() string {
	if s.viewing == nil {
		return "Add a viewing"
	}
	return "Edit viewing"
}

func (s *viewingForm) load(a *app) error { return nil }
func (s *viewingForm) typing() bool      { return true }
func (s *viewingForm) help() string      { return formHelp }

func (s *viewingForm) update(a *app, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		a.pop()
		return nil
	}
	save, cmd := s.form.update(msg)
	if !save {
		return cmd
	}

	vf := collection.ViewingFields{Date: s.value(fDate), Rating: s.value(fRating), Review: s.value(fReview)}
	in, err := vf.Parse()
	if err != nil {
		s.err = capitalize(strings.ReplaceAll(err.Error(), "\n", "; "))
		return nil
	}
	if s.viewing == nil {
		_, err = a.c.AddViewing(a.ctx, s.movie.ID, in)
	} else {
		err = a.c.UpdateViewing(a.ctx, s.viewing.ID, in)
	}
	if err != nil {
		s.err = err.Error()
		return nil
	}
	if s.viewing == nil {
		a.info("Viewing added.")
	} else {
		a.info("Viewing saved.")
	}
	a.pop()
	return nil
}

func (s *viewingForm) view(a *app, width, height int) string {
	stars := ""
	if r := strings.TrimSpace(s.value(fRating)); r != "" {
		vf := collection.ViewingFields{Rating: r}
		if in, err := vf.Parse(); err == nil {
			stars = "  " + styleStars.Render(collection.Stars(in.Rating))
		}
	}
	return styleTitle.Render(movieTitle(s.movie.Title, s.movie.Year)) + stars + "\n\n" + s.form.view()
}

// ---- Wikidata search ----

// searchScreen looks up a movie. With onPick nil it adds the chosen
// result to the collection; otherwise onPick receives it.
type searchScreen struct {
	query     textinput.Model
	ran       string // query of the shown results
	results   []collection.SearchResult
	searchErr string
	cur       cursor
	inResults bool
	onPick    func(a *app, r collection.SearchResult)
}

func newSearchScreen(q string, onPick func(a *app, r collection.SearchResult)) *searchScreen {
	in := textinput.New()
	in.Placeholder = "title, and year if you like"
	in.SetValue(q)
	in.Width = 50
	in.CharLimit = 200
	in.Focus()
	return &searchScreen{query: in, onPick: onPick}
}

func (s *searchScreen) title() string {
	if s.onPick == nil {
		return "Add a movie"
	}
	return "Look up"
}

func (s *searchScreen) load(a *app) error { return nil }
func (s *searchScreen) typing() bool      { return !s.inResults }

func (s *searchScreen) help() string {
	verb := "add"
	if s.onPick != nil {
		verb = "use"
	}
	if s.inResults {
		return "enter " + verb + " · / edit search"
	}
	h := "enter search"
	if len(s.results) > 0 {
		h += " · ↓ pick a result"
	}
	if s.onPick == nil {
		h += " · ctrl+n enter details yourself"
	}
	return h + " · esc back"
}

func (s *searchScreen) manual(a *app) tea.Cmd {
	if s.onPick != nil {
		return nil
	}
	a.replace(newMovieForm(nil, collection.MovieFields{Title: strings.TrimSpace(s.query.Value())}))
	return textinput.Blink
}

func (s *searchScreen) update(a *app, msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyMsg)
	if isKey {
		switch k.String() {
		case "esc":
			if s.inResults {
				s.focusQuery()
				return textinput.Blink
			}
			a.pop()
			return nil
		case "ctrl+n":
			return s.manual(a)
		}
	}

	if s.inResults {
		if s.cur.move(msg, len(s.results), 5) {
			return nil
		}
		if !isKey {
			return nil
		}
		switch k.String() {
		case "/", "tab":
			s.focusQuery()
			return textinput.Blink
		case "enter":
			r := s.results[s.cur.i]
			if s.onPick != nil {
				s.onPick(a, r)
				return nil
			}
			mf := collection.MovieFieldsFromResult(r)
			in, err := mf.Parse()
			if err != nil {
				// A result we can't store as is: let the user fix it.
				a.replace(newMovieForm(nil, mf))
				return textinput.Blink
			}
			return a.addMovie(in)
		}
		return nil
	}

	if isKey {
		switch k.String() {
		case "enter":
			q := strings.TrimSpace(s.query.Value())
			if q == "" {
				return nil
			}
			return a.run("searching", func(ctx context.Context) func(*app) tea.Cmd {
				results, searchErr := a.c.Search(ctx, q)
				return func(a *app) tea.Cmd {
					s.ran, s.results, s.searchErr, s.cur = q, results, searchErr, cursor{}
					if len(results) > 0 {
						s.inResults = true
						s.query.Blur()
					}
					return nil
				}
			})
		case "down", "tab":
			if len(s.results) > 0 {
				s.inResults = true
				s.query.Blur()
			}
			return nil
		}
	}

	var cmd tea.Cmd
	s.query, cmd = s.query.Update(msg)
	return cmd
}

func (s *searchScreen) focusQuery() {
	s.inResults = false
	s.query.Focus()
}

func (s *searchScreen) view(a *app, width, height int) string {
	var b strings.Builder
	intro := "Search Wikidata and a built-in list of popular films."
	if s.onPick != nil {
		intro = "Pick a result to replace the title, year, and ids in the form."
	}
	b.WriteString(styleDim.Render(intro) + "\n\n")
	b.WriteString(styleTitle.Render("Search ") + s.query.View() + "\n\n")

	if s.searchErr != "" {
		msg := s.searchErr
		if len(s.results) > 0 {
			msg = "Showing built-in matches only. " + msg
		}
		b.WriteString(styleWarn.Render(wrap(msg, width)) + "\n\n")
	}
	if s.ran != "" && len(s.results) == 0 {
		b.WriteString("No movies found for " + fmt.Sprintf("%q", s.ran) + ".")
		if s.onPick == nil {
			b.WriteString(" Press ctrl+n to enter the details yourself.")
		}
		return b.String()
	}

	// Two lines per result: title, then description and ids.
	used := strings.Count(b.String(), "\n")
	b.WriteString(s.cur.render(len(s.results), max((height-used)/2, 1), func(i int, sel bool) string {
		r := s.results[i]
		t := movieTitle(r.Title, r.Year)
		if sel && s.inResults {
			t = styleSelected.Render(t)
		} else {
			t = styleTitle.Render(t)
		}
		ids := []string{r.ID}
		if r.IMDB != nil {
			ids = append(ids, *r.IMDB)
		}
		if r.TMDB != nil {
			ids = append(ids, "tmdb "+*r.TMDB)
		}
		detail := strings.Join(ids, " · ")
		if r.Description != "" {
			detail = r.Description + " · " + detail
		}
		return t + "\n    " + styleDim.Render(truncate(detail, width-4))
	}))
	return b.String()
}
