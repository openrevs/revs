package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/revstest"
)

// harness drives an app with key presses, running commands to
// completion the way the Bubble Tea runtime would (minus timers).
type harness struct {
	t *testing.T
	a *app
}

func newHarness(t *testing.T, dir string) *harness {
	t.Helper()
	// Wikidata unreachable: searches use the built-in list only.
	wd := httptest.NewServer(http.NotFoundHandler())
	wd.Close()
	c, err := collection.Open(collection.Options{DataDir: dir, WikidataURL: wd.URL})
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	return harnessFor(t, c)
}

// newNetHarness drives a new collection on network.
func newNetHarness(t *testing.T, network *revstest.Network) *harness {
	t.Helper()
	return harnessFor(t, network.NewCollection())
}

func harnessFor(t *testing.T, c *collection.Collection) *harness {
	t.Helper()
	h := &harness{t: t, a: newApp(c)}
	h.a.syncOps = true
	h.send(tea.WindowSizeMsg{Width: 100, Height: 40})
	return h
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.a.Update(msg)
	h.drain(cmd)
}

// drain runs cmd and feeds back the messages the app acts on. Commands
// that don't return at once are cursor blinks, which would tick
// forever, and are dropped; operations run synchronously (syncOps).
func (h *harness) drain(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	c := make(chan tea.Msg, 1)
	go func() { c <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-c:
	case <-time.After(20 * time.Millisecond):
		return
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			h.drain(c)
		}
	case doneMsg:
		h.send(msg)
	}
}

// keys types each argument: a named key ("enter", "esc", "ctrl+s",
// "tab") or literal text.
func (h *harness) keys(ks ...string) {
	h.t.Helper()
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "down": tea.KeyDown,
		"up": tea.KeyUp, "ctrl+s": tea.KeyCtrlS, "ctrl+n": tea.KeyCtrlN, "ctrl+f": tea.KeyCtrlF,
	}
	for _, k := range ks {
		if t, ok := named[k]; ok {
			h.send(tea.KeyMsg{Type: t})
			continue
		}
		for _, r := range k {
			h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
}

func (h *harness) screen() string { return h.a.View() }

func TestMoviesAndViewings(t *testing.T) {
	h := newHarness(t, t.TempDir())
	ctx := context.Background()
	c := h.a.c

	require.Contains(t, h.screen(), "Your collection is empty")

	// Add from the built-in list.
	h.keys("a", "pulp fiction", "enter")
	require.Contains(t, h.screen(), "Pulp Fiction (1994)")
	h.keys("enter")
	require.Contains(t, h.a.flash, "Added Pulp Fiction")
	movies, err := c.AllMovies(ctx)
	require.NoError(t, err)
	require.Len(t, movies, 1)
	require.Equal(t, "Q104123", *movies[0].WikidataID)

	// Adding asks for a rating straight away.
	require.Contains(t, h.screen(), "Rate Pulp Fiction")
	h.keys("4.5", "enter")
	require.Contains(t, h.a.flash, "Rated Pulp Fiction ★★★★½")
	vs, err := c.Viewings(ctx, movies[0].ID)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	require.Equal(t, int64(9), *vs[0].Rating)

	// The same film again leads to the existing entry.
	h.keys("esc", "a", "pulp fiction", "enter", "enter")
	require.Contains(t, h.a.flash, "already in your collection")

	// Add another viewing: date, rating, review.
	h.keys("v", "tab", "3.5", "tab", "Royale with cheese.", "ctrl+s")
	require.Contains(t, h.a.flash, "Viewing added")
	vs, err = c.Viewings(ctx, movies[0].ID)
	require.NoError(t, err)
	require.Len(t, vs, 2)
	require.Equal(t, int64(7), *vs[0].Rating, "stored in half stars")
	require.Equal(t, "Royale with cheese.", vs[0].Review)
	require.Contains(t, h.screen(), "★★★½☆")

	// A bad rating is refused with a message.
	h.keys("v", "tab", "9", "ctrl+s")
	require.Contains(t, h.screen(), "Rating must be ½–5 stars")
	h.keys("esc")

	// Edit the viewing.
	h.keys("enter", "tab")
	for range 3 {
		h.send(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	h.keys("5", "ctrl+s")
	vs, err = c.Viewings(ctx, movies[0].ID)
	require.NoError(t, err)
	require.Equal(t, int64(10), *vs[0].Rating)

	// Enter a movie by hand.
	h.keys("1", "a", "My Home Movie", "ctrl+n")
	h.keys("tab", "2001", "ctrl+s")
	require.Contains(t, h.a.flash, "Added My Home Movie")
	h.keys("esc") // skip rating it
	require.Nil(t, h.a.prompt)

	// History lists the viewing.
	h.keys("2")
	require.Contains(t, h.screen(), "Pulp Fiction")

	// Filter and delete.
	h.keys("1", "/", "home", "enter")
	require.Contains(t, h.screen(), "1 matching")
	h.keys("d", "y")
	movies, err = c.AllMovies(ctx)
	require.NoError(t, err)
	require.Len(t, movies, 1)
}

func TestIdentityAndPublishing(t *testing.T) {
	network := revstest.NewNetwork(t)
	sts := network.NewServer()
	ctx := context.Background()

	alice, bob := newNetHarness(t, network), newNetHarness(t, network)
	for _, h := range []*harness{alice, bob} {
		h.keys("4", "g")
		require.NotEmpty(t, h.a.c.UserID(ctx))
		h.keys("3", "a")
		h.a.prompt.input.SetValue(sts.URL)
		h.keys("enter")
		require.Contains(t, h.a.flash, "Server added")
		h.keys("1", "a", "alien", "enter", "enter", "5", "enter")
	}

	// Generating again needs confirmation.
	id := alice.a.c.UserID(ctx)
	alice.keys("4", "g", "n")
	require.Equal(t, id, alice.a.c.UserID(ctx))

	for _, h := range []*harness{alice, bob} {
		h.keys("3", "enter", "p")
		require.Contains(t, h.a.flash, "Pushed revision 1")
	}
	sts.Cut()

	alice.keys("u")
	require.Contains(t, alice.a.flash, "2 users changed")
	require.Contains(t, alice.screen(), "you")

	// Bob's review shows on Alice's movie.
	alice.keys("1", "enter")
	require.Contains(t, alice.screen(), "Others' reviews")
	require.Contains(t, alice.screen(), collection.ShortID(bob.a.c.UserID(ctx)))

	// A second device with Alice's key merges her published copy as it
	// pushes.
	alice2 := newNetHarness(t, network)
	alice2.keys("4", "i")
	alice2.a.prompt.input.SetValue(alice.a.c.PrivateKeyText(ctx))
	alice2.keys("enter", "3", "a")
	alice2.a.prompt.input.SetValue(sts.URL)
	alice2.keys("enter", "p")
	require.Contains(t, alice2.a.flash, "Merged your collection from the server (revision 1) first. Imported 1 new movies")
	require.Contains(t, alice2.a.flash, "Pushed revision 2")

	// A copy the server hasn't published yet is a conflict, which
	// pressing p again resolves by pushing anyway, once confirmed.
	_, _, err := alice.a.c.AddMovie(ctx, collection.MovieInput{Title: "Aliens"})
	require.NoError(t, err)
	alice.keys("3", "enter", "p")
	require.Contains(t, alice.screen(), "newer revision (2)")
	alice.keys("p", "y")
	require.Contains(t, alice.a.flash, "Pushed revision 3")

	// Export and import.
	dir := t.TempDir()
	alice.keys("5", "e")
	alice.a.prompt.input.SetValue(dir)
	alice.keys("enter")
	require.Contains(t, alice.a.flash, "Exported to "+dir)
	path := strings.TrimSuffix(strings.TrimPrefix(alice.a.flash, "Exported to "), ".")

	fresh := newHarness(t, t.TempDir())
	fresh.keys("5", "i")
	fresh.a.prompt.input.SetValue(path)
	fresh.keys("enter")
	require.Contains(t, fresh.a.flash, "Imported 2 new movies")
}

// Edits made by another process on the same data directory show up
// when the app next checks the collection's version.
func TestWatch(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	ctx := context.Background()

	other, err := collection.Open(collection.Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { other.Close() })

	poll := func() {
		v, err := h.a.c.DataVersion(ctx)
		require.NoError(t, err)
		h.send(versionMsg{v: v})
	}

	m, _, err := other.AddMovie(ctx, collection.MovieInput{Title: "Brazil"})
	require.NoError(t, err)
	require.NotContains(t, h.screen(), "Brazil")
	poll()
	require.Contains(t, h.screen(), "Brazil")

	// Deleted elsewhere while shown: back to the list, with a note.
	h.keys("enter")
	require.NoError(t, other.DeleteMovie(ctx, m.ID))
	poll()
	require.Contains(t, h.a.flash, "Brazil was deleted")
	require.Len(t, h.a.stack, 1)
}

// From the movie list, r rates the selected movie and +/- nudge it,
// without opening it.
func TestRateFromList(t *testing.T) {
	h := newHarness(t, t.TempDir())
	ctx := context.Background()
	c := h.a.c
	m, _, err := c.AddMovie(ctx, collection.MovieInput{Title: "Heat"})
	require.NoError(t, err)
	h.a.reload()

	h.keys("+")
	vs, err := c.Viewings(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, vs, 1, "rating an unrated movie adds a viewing")
	require.Equal(t, int64(5), *vs[0].Rating, "starting in the middle")

	h.keys("+", "+", "-")
	vs, err = c.Viewings(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	require.Equal(t, int64(6), *vs[0].Rating)

	// - stops at ½ star.
	for range 8 {
		h.keys("-")
	}
	vs, err = c.Viewings(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), *vs[0].Rating)
	h.keys("+", "+", "+", "+", "+")

	h.keys("r")
	require.Equal(t, "3", h.a.prompt.input.Value())
	h.a.prompt.input.SetValue("")
	h.keys("4.5", "enter")
	vs, err = c.Viewings(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, int64(9), *vs[0].Rating)
	require.Contains(t, h.screen(), "★★★★½")

	h.keys("r")
	h.a.prompt.input.SetValue("")
	h.keys("7", "enter")
	require.Contains(t, h.screen(), "Rating must be")
}
