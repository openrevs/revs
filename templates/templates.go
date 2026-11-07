package templates

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed *.html
var tmplFS embed.FS

// embedTmpl selects the embedded templates. When false, templates are read
// from the "templates" directory on disk and reparsed on every render, which
// allows live editing during development.
var embedTmpl = false

var zone *time.Location

func init() {
	var err error
	zone, err = time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}
}

// Renderer renders page templates, optionally wrapped in a layout template
// that marks the insertion point with {{embed}}.
type Renderer struct {
	cached *template.Template
}

func New() (*Renderer, error) {
	r := &Renderer{}

	if embedTmpl {
		t, err := parseAll()
		if err != nil {
			return nil, err
		}
		r.cached = t
	}

	return r, nil
}

func funcMap() template.FuncMap {
	markdown := goldmark.New(goldmark.WithExtensions(extension.NewLinkify()))

	return template.FuncMap{
		// Placeholder; replaced per render when a layout is used.
		"embed": func() template.HTML { return "" },
		"markdown": func(s string) template.HTML {
			var buf bytes.Buffer
			if err := markdown.Convert([]byte(s), &buf); err != nil {
				panic(err)
			}

			return template.HTML(buf.String())
		},
		"date": func(d time.Time) string {
			return d.In(zone).Format("2006-01-02")
		},
		"datetime": func(d time.Time) string {
			return d.In(zone).Format("2006-01-02 3:04 PM")
		},
	}
}

// parseAll parses every template file into one set. Each file is registered
// under its basename without extension, so "login.html" renders as "login".
func parseAll() (*template.Template, error) {
	var fsys fs.FS = tmplFS
	if !embedTmpl {
		fsys = os.DirFS("templates")
	}

	names, err := fs.Glob(fsys, "*.html")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	root := template.New("").Funcs(funcMap())
	for _, name := range names {
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		tmplName := strings.TrimSuffix(name, ".html")
		if _, err := root.New(tmplName).Parse(string(content)); err != nil {
			return nil, err
		}
	}

	return root, nil
}

func (r *Renderer) Render(w io.Writer, name string, data interface{}, layout string) error {
	t := r.cached
	if t == nil {
		var err error
		t, err = parseAll()
		if err != nil {
			return err
		}
	}

	// The parsed set is never executed directly: html/template forbids
	// cloning a set once any of its templates has run, so each render
	// executes clones and the cached set stays pristine.
	pt, err := t.Clone()
	if err != nil {
		return err
	}
	lt, err := t.Clone()
	if err != nil {
		return err
	}

	var page bytes.Buffer
	if err := pt.ExecuteTemplate(&page, name, data); err != nil {
		return fmt.Errorf("render %q: %w", name, err)
	}

	if layout == "" {
		_, err := w.Write(page.Bytes())
		return err
	}

	lt = lt.Funcs(template.FuncMap{
		"embed": func() template.HTML { return template.HTML(page.String()) },
	})

	var out bytes.Buffer
	if err := lt.ExecuteTemplate(&out, layout, data); err != nil {
		return fmt.Errorf("render layout %q: %w", layout, err)
	}

	_, err = w.Write(out.Bytes())
	return err
}
