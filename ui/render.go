package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openrevs/revs/collection"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// pages maps a page template file name to its parsed template set: the
// shared layout plus that page's "content" block.
type pages map[string]*template.Template

func parsePages() (pages, error) {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	p := pages{}
	for _, name := range names {
		base := strings.TrimPrefix(name, "templates/")
		if base == "layout.html" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", name)
		if err != nil {
			return nil, err
		}
		p[base] = t
	}
	return p, nil
}

var funcs = template.FuncMap{
	"stars":   collection.Stars,
	"deref":   derefAny,
	"short":   collection.ShortID,
	"imdbURL": func(id string) string { return collection.IMDBURL(url.PathEscape(id)) },
	"tmdbURL": func(id string) string { return collection.TMDBURL(url.PathEscape(id)) },
	"wdURL":   func(id string) string { return collection.WikidataURL(url.PathEscape(id)) },
	"today":   func() string { return time.Now().Format(collection.DateLayout) },
	"starbar": starSteps,
	"timeAgo": collection.TimeAgo,
	"dict":    dict,
}

// dict builds a map from alternating keys and values, for passing
// several values to a template.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// derefAny is deref for templates, which can't instantiate generics.
func derefAny(v any) any {
	switch p := v.(type) {
	case *string:
		return collection.Deref(p)
	case *int64:
		if p == nil {
			return ""
		}
		return *p
	}
	return v
}

// view is what every page template receives.
type view struct {
	Title  string
	Nav    string // active nav section
	Flash  *flash
	UserID string
	Data   any
}

type flash struct {
	Kind string // "ok" or "error"
	Msg  string
}

const flashCookie = "revs_flash"

func setFlash(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    url.QueryEscape(kind + ":" + msg),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func popFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1})
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return nil
	}
	kind, msg, _ := strings.Cut(v, ":")
	return &flash{Kind: kind, Msg: msg}
}

// redirect sends the browser to target with a flash message.
func redirect(w http.ResponseWriter, r *http.Request, target, kind, msg string) {
	if msg != "" {
		setFlash(w, kind, msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) render(w http.ResponseWriter, r *http.Request, status int, page string, v view) {
	t, ok := a.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	if v.Flash == nil {
		v.Flash = popFlash(w, r)
	}
	v.UserID = a.c.UserID(r.Context())

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", v); err != nil {
		log.Printf("rendering %s: %v", page, err)
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// starStep is one half-star button of a clickable star bar: the left
// or right half of a star, lit when the rating reaches it.
type starStep struct {
	Value, Label string
	Left, On     bool
}

// starSteps lays out a star bar for rating (a *int64 or int64 in half
// stars; nil or negative is unrated).
func starSteps(rating any) []starStep {
	var n int64 = -1
	switch v := rating.(type) {
	case *int64:
		if v != nil {
			n = *v
		}
	case int64:
		n = v
	case string: // a form's value, e.g. "3.5" or "none"
		if p, err := collection.ParseStars(v); err == nil {
			n = p
		}
	}
	steps := make([]starStep, 0, collection.MaxRating)
	for i := int64(1); i <= collection.MaxRating; i++ {
		label := collection.FormatStars(i)
		label = strings.Replace(label, ".5", "½", 1)
		label = strings.TrimPrefix(label, "0")
		if i == 2 {
			label += " star"
		} else {
			label += " stars"
		}
		steps = append(steps, starStep{Value: collection.FormatStars(i), Label: label, Left: i%2 == 1, On: i <= n})
	}
	return steps
}
