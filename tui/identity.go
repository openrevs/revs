package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/collection/localdb"
)

// ---- identity ----

type identityScreen struct {
	id       collection.Identity
	hasKey   bool
	revealed string
}

func (s *identityScreen) title() string { return "Identity" }

func (s *identityScreen) load(a *app) error {
	s.id = a.c.Identity(a.ctx)
	s.hasKey = a.c.HasKey(a.ctx)
	return nil
}

func (s *identityScreen) help() string {
	if !s.hasKey {
		return "g generate a key · i import a key"
	}
	reveal := "r reveal private key"
	if s.revealed != "" {
		reveal = "r hide private key"
	}
	return reveal + " · g generate a new key · i import a key"
}

// replacing runs then, first confirming if a key would be replaced.
func (s *identityScreen) replacing(a *app, then func(a *app) tea.Cmd) tea.Cmd {
	if !s.hasKey {
		return then(a)
	}
	a.confirmThen("Replace your current key? Back it up first, or anything you published with it can't be updated again.", then)
	return nil
}

func (s *identityScreen) update(a *app, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "r":
		if s.revealed != "" {
			s.revealed = ""
		} else {
			s.revealed = a.c.PrivateKeyText(a.ctx)
		}
	case "g":
		return s.replacing(a, func(a *app) tea.Cmd {
			if err := a.c.GenerateKey(a.ctx); err != nil {
				a.fail(err)
				return nil
			}
			s.revealed = ""
			a.info("Generated a new key. Back up the private key somewhere safe (r reveals it).")
			a.reload()
			return nil
		})
	case "i":
		return s.replacing(a, func(a *app) tea.Cmd {
			return a.ask("Private key:", "", func(a *app, v string) tea.Cmd {
				if err := a.c.ImportKey(a.ctx, v); err != nil {
					a.fail(err)
					return nil
				}
				s.revealed = ""
				a.info("Key imported.")
				a.reload()
				return nil
			})
		})
	}
	return nil
}

func (s *identityScreen) view(a *app, width, height int) string {
	var b strings.Builder
	b.WriteString(styleDim.Render(wrap("Your key signs everything you publish. Your user id is derived from it, so keep the private key safe: with it, anyone can publish as you; without it, you can't update what you published.", width)) + "\n\n")

	switch {
	case s.id.KeyErr != "":
		b.WriteString(styleError.Render(s.id.KeyErr) + "\n")
	case s.id.UserID == "":
		b.WriteString("You don't have a key yet. Press g to generate one, or i to import one (for example from revs keygen).\n")
	default:
		kv := func(k, v string) { b.WriteString(styleDim.Render(pad(k, 13)) + v + "\n") }
		kv("User id", s.id.UserID)
		kv("Public key", s.id.PublicKey)
		if s.revealed != "" {
			kv("Private key", styleWarn.Render(s.revealed))
		} else {
			kv("Private key", styleDim.Render("hidden (r to reveal)"))
		}
	}
	return b.String()
}

// ---- import & export ----

type dataScreen struct {
	counts localdb.CountMoviesAndViewingsRow
	hasKey bool
}

func (s *dataScreen) title() string { return "Import & export" }

func (s *dataScreen) load(a *app) error {
	var err error
	s.counts, err = a.c.Counts(a.ctx)
	s.hasKey = a.c.HasKey(a.ctx)
	return err
}

func (s *dataScreen) help() string { return "e export · i import" }

func (s *dataScreen) update(a *app, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "e":
		if !s.hasKey {
			a.fail(fmt.Errorf("create or import a key before exporting: exports are signed"))
			return nil
		}
		return a.ask("Export to file or directory:", ".", func(a *app, path string) tea.Cmd {
			out, err := export(a, path)
			if err != nil {
				a.fail(err)
				return nil
			}
			a.info("Exported to " + out + ".")
			return nil
		})
	case "i":
		return a.ask("Import file:", "", func(a *app, path string) tea.Cmd {
			data, err := os.ReadFile(expandHome(path))
			if err != nil {
				a.fail(err)
				return nil
			}
			res, err := a.c.Import(a.ctx, data)
			if err != nil {
				a.fail(err)
				return nil
			}
			a.info(res.String())
			a.reload()
			return nil
		})
	}
	return nil
}

// export writes a signed export to path, or into it if it is a
// directory, and returns the file written.
func export(a *app, path string) (string, error) {
	data, rev, err := a.c.Export(a.ctx)
	if err != nil {
		return "", err
	}
	path = expandHome(path)
	if path == "" {
		path = "."
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = filepath.Join(path, collection.ExportFilename(rev))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, nil
}

func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

func (s *dataScreen) view(a *app, width, height int) string {
	var b strings.Builder
	b.WriteString(styleHeading.Render("Export") + "\n")
	b.WriteString(wrap(fmt.Sprintf("Save your collection (%d movies, %d viewings) as a signed ratingset file: the same document a push uploads.", s.counts.Movies, s.counts.Viewings), width) + "\n")
	if !s.hasKey {
		b.WriteString(styleWarn.Render("Exports are signed: create or import a key first (tab 4).") + "\n")
	}
	b.WriteString("\n" + styleHeading.Render("Import") + "\n")
	b.WriteString(wrap("Merge a ratingset file into your collection: a signed export, or unsigned JSON lines. "+
		"Nothing is deleted or overwritten. Movies match by Wikidata, IMDb, or TMDB id, otherwise by title and year; "+
		"a viewing is added unless an identical one exists.", width) + "\n")
	return b.String()
}
