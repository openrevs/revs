package tui

import (
	"net/http/httptest"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openrevs/revs"
	"github.com/openrevs/revs/tui/workspace"
)

// Headless smoke test: drives the tea.Model directly (no TTY) through
// the wizard, adding and rating a title, and pushing to a real server.

func startServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := revs.NewServer(t.TempDir())
	require.NoError(t, err)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func press(t *testing.T, a *app, keys ...string) {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, _ = a.Update(msg)
	}
}

func TestWizardAddPushFlow(t *testing.T) {
	ts := startServer(t)

	ws, err := workspace.Open(workspace.Options{DataDir: t.TempDir(), ServerFlag: ts.URL})
	require.NoError(t, err)

	a := newApp(ws)
	require.Equal(t, screenWizardKey, a.screen, "server known, key missing → key step")

	// Generate an identity; the app moves to the sync step.
	press(t, a, "g")
	require.Equal(t, screenWizardSync, a.screen)
	require.False(t, ws.NeedsKey())

	// Run the wizard's sync command synchronously and feed the result
	// back, as the tea runtime would.
	msg := wizardSyncCmd(ws)()
	_, _ = a.Update(msg)
	require.Equal(t, screenList, a.screen, "no published doc → empty draft → list")
	require.True(t, ws.HasDraft())

	// Add a title through the form.
	press(t, a, "a")
	require.Equal(t, screenTitleForm, a.screen)
	press(t, a, "Alien", "tab", "1979", "enter")
	require.Equal(t, screenList, a.screen)
	require.Len(t, ws.Titles(), 1)
	assert.Equal(t, "Alien", ws.Titles()[0].Title.Name)
	assert.True(t, ws.Dirty())

	// Open detail, rate it (keep today's default date, rating 5).
	press(t, a, "enter")
	require.Equal(t, screenDetail, a.screen)
	press(t, a, "r")
	require.Equal(t, screenReviewForm, a.screen)
	press(t, a, "tab", "5", "tab", "a classic", "enter")
	require.Equal(t, screenDetail, a.screen)
	title, err := ws.Title(ws.Titles()[0].Index)
	require.NoError(t, err)
	require.Len(t, title.Reviews, 1)
	assert.Equal(t, 5, *title.Reviews[0].Rating)
	assert.Equal(t, "a classic", *title.Reviews[0].Comment)

	// Back to the list, push.
	press(t, a, "esc", "s")
	require.True(t, a.busy)
	msg = pushCmd(ws)()
	_, _ = a.Update(msg)
	assert.False(t, a.busy)
	assert.Contains(t, a.flash, "pushed rev 1")
	assert.False(t, ws.Dirty())
	assert.Equal(t, 1, ws.BaseRev())

	// Delete-title confirm path.
	press(t, a, "d")
	require.Equal(t, screenConfirm, a.screen)
	press(t, a, "y")
	require.Equal(t, screenList, a.screen)
	assert.Empty(t, ws.Titles())
	assert.True(t, ws.Dirty())

	// Quit from the list.
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	require.NotNil(t, cmd)
}

func TestWizardImportKeyValidation(t *testing.T) {
	ts := startServer(t)
	ws, err := workspace.Open(workspace.Options{DataDir: t.TempDir(), ServerFlag: ts.URL})
	require.NoError(t, err)

	a := newApp(ws)
	press(t, a, "i")
	press(t, a, "nothex", "enter")
	require.Equal(t, screenWizardKey, a.screen, "bad key keeps the wizard on the key step")
	assert.NotEmpty(t, a.wizardErr)
	assert.True(t, ws.NeedsKey())
}

func TestFormValidation(t *testing.T) {
	ts := startServer(t)
	ws, err := workspace.Open(workspace.Options{DataDir: t.TempDir(), ServerFlag: ts.URL})
	require.NoError(t, err)
	_, err = ws.GenerateKey()
	require.NoError(t, err)
	require.NoError(t, ws.NewEmptyDraft())

	a := newApp(ws)
	require.Equal(t, screenList, a.screen)

	// Empty name is rejected.
	press(t, a, "a", "enter")
	require.Equal(t, screenTitleForm, a.screen)
	assert.NotEmpty(t, a.titleForm.err)

	// Bad year is rejected.
	press(t, a, "Alien", "tab", "19", "enter")
	require.Equal(t, screenTitleForm, a.screen)
	assert.Contains(t, a.titleForm.err, "year")

	// Fix it; esc-cancel afterwards leaves no second title.
	press(t, a, "79", "enter")
	require.Equal(t, screenList, a.screen)
	require.Len(t, ws.Titles(), 1)

	// Rating outside the user's declared bounds is rejected.
	press(t, a, "enter", "r")
	require.Equal(t, screenReviewForm, a.screen)
	press(t, a, "tab", "11", "enter")
	require.Equal(t, screenReviewForm, a.screen)
	assert.Contains(t, a.reviewForm.err, "rating")
	press(t, a, "esc")
	require.Equal(t, screenDetail, a.screen)
}
