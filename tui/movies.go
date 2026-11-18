package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/collection/localdb"
)

// ---- movie list ----

type moviesScreen struct {
	query  string
	movies []localdb.ListMoviesRow
	counts localdb.CountMoviesAndViewingsRow
	cur    cursor
}

func (s *moviesScreen) title() string { return "Movies" }

func (s *moviesScreen) load(a *app) error {
	var err error
	if s.movies, err = a.c.Movies(a.ctx, s.query); err != nil {
		return err
	}
	s.counts, err = a.c.Counts(a.ctx)
	return err
}

func (s *moviesScreen) help() string {
	return "enter open · r rate · +/- nudge rating · a add · / filter · d delete"
}

func (s *moviesScreen) selected() (localdb.ListMoviesRow, bool) {
	if len(s.movies) == 0 {
		return localdb.ListMoviesRow{}, false
	}
	s.cur.clamp(len(s.movies))
	return s.movies[s.cur.i], true
}

func (s *moviesScreen) update(a *app, msg tea.Msg) tea.Cmd {
	if s.cur.move(msg, len(s.movies), a.height/2) {
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "enter":
		if m, ok := s.selected(); ok {
			a.push(&movieScreen{id: m.ID})
		}
	case "r":
		if m, ok := s.selected(); ok {
			return a.askRating(m.ID, m.Title, m.LatestRating)
		}
	case "+", "=", "-":
		if m, ok := s.selected(); ok {
			a.nudgeRating(m.ID, m.Title, m.LatestRating, k.String() == "-")
		}
	case "a":
		a.push(newSearchScreen(s.query, nil))
	case "/":
		return a.ask("Filter titles:", s.query, func(a *app, v string) tea.Cmd {
			s.query = v
			s.cur = cursor{}
			if err := s.load(a); err != nil {
				a.fail(err)
			}
			return nil
		})
	case "d":
		if m, ok := s.selected(); ok {
			a.confirmThen(fmt.Sprintf("Delete %s and its %d viewings?", m.Title, m.ViewingCount), func(a *app) tea.Cmd {
				if err := a.c.DeleteMovie(a.ctx, m.ID); err != nil {
					a.fail(err)
				} else {
					a.info(fmt.Sprintf("Deleted %s and its viewings.", m.Title))
				}
				a.reload()
				return nil
			})
		}
	}
	return nil
}

func (s *moviesScreen) view(a *app, width, height int) string {
	head := fmt.Sprintf("%d movies", s.counts.Movies)
	if s.counts.Viewings != s.counts.Movies {
		head += fmt.Sprintf(", %d viewings", s.counts.Viewings)
	}
	if s.query != "" {
		head = fmt.Sprintf("%d matching %q", len(s.movies), s.query) + styleDim.Render(" (/ to change)")
	}
	head = styleDim.Render(head) + "\n\n"

	if len(s.movies) == 0 {
		if s.query != "" {
			return head + "No matches."
		}
		return head + "Your collection is empty. Press a to add a movie."
	}

	titleW := max(width-2-5-2-24, 10)
	return head + s.cur.render(len(s.movies), height-2, func(i int, sel bool) string {
		m := s.movies[i]
		t := pad(movieTitle(m.Title, m.Year), titleW)
		if sel {
			t = styleSelected.Render(t)
		}
		meta := m.LastViewed
		if m.ViewingCount == 0 {
			meta = "not rated"
		} else if m.ViewingCount > 1 {
			meta += fmt.Sprintf(" ×%d", m.ViewingCount)
		}
		return t + "  " + styleStars.Render(collection.Stars(m.LatestRating)) + "  " + styleDim.Render(meta)
	})
}

// askRating prompts for a movie's rating and sets it in one step (see
// collection.SetRating). current is in half stars, or -1 if unrated.
func (a *app) askRating(id int64, title string, current int64) tea.Cmd {
	initial := ""
	if current >= 0 {
		initial = collection.FormatStars(current)
	}
	return a.ask(fmt.Sprintf("Rate %s (½–%d, e.g. 3.5):", title, collection.MaxStars), initial, func(a *app, v string) tea.Cmd {
		if v == "" {
			return nil
		}
		n, err := collection.ParseStars(v)
		if err != nil {
			a.fail(err)
			return nil
		}
		a.setRating(id, title, n)
		return nil
	})
}

// nudgeRating moves a movie's rating up or down half a star.
func (a *app) nudgeRating(id int64, title string, current int64, down bool) {
	n := current + 1
	if down {
		n = current - 1
	}
	if current < 0 {
		n = collection.MaxRating / 2 // start in the middle
	}
	a.setRating(id, title, min(max(n, collection.MinUserRating), collection.MaxRating))
}

func (a *app) setRating(id int64, title string, n int64) {
	if _, _, err := a.c.SetRating(a.ctx, id, n); err != nil {
		a.fail(err)
	} else {
		a.info(fmt.Sprintf("Rated %s %s", title, collection.Stars(n)))
	}
	a.reload()
}

func movieTitle(title string, year *int64) string {
	if year != nil {
		return fmt.Sprintf("%s (%d)", title, *year)
	}
	return title
}

// ---- one movie ----

type movieScreen struct {
	id       int64
	movie    localdb.Movie
	viewings []localdb.Viewing
	others   []collection.OtherReview
	cur      cursor
}

func (s *movieScreen) title() string {
	if s.movie.Title == "" {
		return "Movie"
	}
	return s.movie.Title
}

func (s *movieScreen) load(a *app) error {
	m, err := a.c.Movie(a.ctx, s.id)
	if err != nil {
		return err
	}
	s.movie = m
	if s.viewings, err = a.c.Viewings(a.ctx, s.id); err != nil {
		return err
	}
	if s.others, err = a.c.OthersReviews(a.ctx, s.movie); err != nil {
		a.fail(fmt.Errorf("finding others' reviews: %w", err))
	}
	return nil
}

func (s *movieScreen) help() string {
	var h string
	switch len(s.viewings) {
	case 0:
		h = "r rate · v add date or review"
	case 1:
		h = "r rate · +/- nudge · enter edit review · v add another viewing · x delete review"
	default:
		h = "r rate latest · +/- nudge · enter edit viewing · v add viewing · x delete viewing"
	}
	return h + " · e edit movie · D delete movie"
}

// rating is the movie's current rating: its latest viewing's, or -1.
func (s *movieScreen) rating() int64 {
	if len(s.viewings) == 0 || s.viewings[0].Rating == nil {
		return -1
	}
	return *s.viewings[0].Rating
}

func (s *movieScreen) selected() (localdb.Viewing, bool) {
	if len(s.viewings) == 0 {
		return localdb.Viewing{}, false
	}
	s.cur.clamp(len(s.viewings))
	return s.viewings[s.cur.i], true
}

func (s *movieScreen) update(a *app, msg tea.Msg) tea.Cmd {
	if s.cur.move(msg, len(s.viewings), 5) {
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "v", "a":
		return a.pushForm(newViewingForm(s.movie, nil))
	case "r":
		return a.askRating(s.movie.ID, s.movie.Title, s.rating())
	case "+", "=", "-":
		a.nudgeRating(s.movie.ID, s.movie.Title, s.rating(), k.String() == "-")
	case "enter":
		if v, ok := s.selected(); ok {
			return a.pushForm(newViewingForm(s.movie, &v))
		}
		return a.askRating(s.movie.ID, s.movie.Title, s.rating())
	case "x":
		if v, ok := s.selected(); ok {
			when := "this undated viewing"
			if v.ViewedOn != nil {
				when = "the viewing on " + *v.ViewedOn
			}
			a.confirmThen("Delete "+when+"?", func(a *app) tea.Cmd {
				if err := a.c.DeleteViewing(a.ctx, v.ID); err != nil {
					a.fail(err)
				} else {
					a.info("Viewing deleted.")
				}
				a.reload()
				return nil
			})
		}
	case "e":
		return a.pushForm(newMovieForm(&s.movie, collection.MovieFieldsFrom(s.movie)))
	case "D":
		m := s.movie
		a.confirmThen(fmt.Sprintf("Delete %s and all its viewings?", m.Title), func(a *app) tea.Cmd {
			if err := a.c.DeleteMovie(a.ctx, m.ID); err != nil {
				a.fail(err)
				return nil
			}
			a.info(fmt.Sprintf("Deleted %s and its viewings.", m.Title))
			a.pop()
			return nil
		})
	}
	return nil
}

func (s *movieScreen) view(a *app, width, height int) string {
	var b strings.Builder
	m := s.movie
	b.WriteString(styleTitle.Render(movieTitle(m.Title, m.Year)) + "\n")
	if links := movieLinks(m.ImdbID, m.TmdbID, m.WikidataID); links != "" {
		b.WriteString(links + "\n")
	}

	heading := "Your viewings"
	if len(s.viewings) <= 1 {
		heading = "Your review"
	}
	b.WriteString("\n" + styleHeading.Render(heading) + "\n")
	if len(s.viewings) == 0 {
		b.WriteString(styleDim.Render("  Not rated yet. Press r to rate it.") + "\n")
	} else {
		listH := min(len(s.viewings), max(height/3, 3))
		b.WriteString(s.cur.render(len(s.viewings), listH, func(i int, sel bool) string {
			v := s.viewings[i]
			date := pad(collection.Deref(v.ViewedOn), 10)
			if v.ViewedOn == nil {
				date = styleDim.Render(pad("undated", 10))
			}
			line := styleStars.Render(collection.Stars(v.Rating)) + "  " + date
			if v.Review != "" {
				line += "  " + styleDim.Render(truncate(firstLine(v.Review), width-26))
			}
			return line
		}) + "\n")
		if v, ok := s.selected(); ok && v.Review != "" {
			b.WriteString("\n" + indent(wrap(v.Review, width-4), "    ") + "\n")
		}
	}

	if len(s.others) > 0 {
		b.WriteString("\n" + styleHeading.Render("Others' reviews") + "\n")
		for _, o := range s.others {
			line := "  " + pad(collection.ShortID(o.Userid), 13) + "  " +
				pad(collection.Deref(o.ViewedOn), 10) + "  " + styleStars.Render(collection.Stars(o.Rating))
			b.WriteString(line + "\n")
			if o.Comment != "" {
				b.WriteString(styleDim.Render(indent(wrap(o.Comment, width-6), "      ")) + "\n")
			}
		}
	}
	return b.String()
}

// movieLinks lists a movie's ids with links.
func movieLinks(imdb, tmdb, wikidata *string) string {
	var lines []string
	if imdb != nil {
		lines = append(lines, styleDim.Render("IMDb      ")+collection.IMDBURL(*imdb))
	}
	if tmdb != nil {
		lines = append(lines, styleDim.Render("TMDB      ")+collection.TMDBURL(*tmdb))
	}
	if wikidata != nil {
		lines = append(lines, styleDim.Render("Wikidata  ")+collection.WikidataURL(*wikidata))
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	line, rest, _ := strings.Cut(s, "\n")
	if rest != "" {
		line += " …"
	}
	return line
}

// ---- history ----

type historyScreen struct {
	rows []localdb.HistoryRow
	cur  cursor
}

func (s *historyScreen) title() string { return "History" }

func (s *historyScreen) load(a *app) error {
	var err error
	s.rows, err = a.c.History(a.ctx, 500)
	return err
}

func (s *historyScreen) help() string { return "enter open movie" }

func (s *historyScreen) update(a *app, msg tea.Msg) tea.Cmd {
	if s.cur.move(msg, len(s.rows), a.height/2) {
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" && len(s.rows) > 0 {
		a.push(&movieScreen{id: s.rows[s.cur.i].MovieID})
	}
	return nil
}

func (s *historyScreen) view(a *app, width, height int) string {
	if len(s.rows) == 0 {
		return "No viewings yet."
	}
	head := styleDim.Render("Every viewing, newest first.") + "\n\n"
	titleW := max(min(40, width-2-10-2-5-4), 10)
	return head + s.cur.render(len(s.rows), height-2, func(i int, sel bool) string {
		r := s.rows[i]
		date := pad(collection.Deref(r.ViewedOn), 10)
		if r.ViewedOn == nil {
			date = styleDim.Render(pad("undated", 10))
		}
		t := pad(movieTitle(r.Title, r.Year), titleW)
		if sel {
			t = styleSelected.Render(t)
		}
		line := date + "  " + styleStars.Render(collection.Stars(r.Rating)) + "  " + t
		if r.Review != "" {
			line += "  " + styleDim.Render(truncate(firstLine(r.Review), width-titleW-25))
		}
		return line
	})
}
