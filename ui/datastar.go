package ui

import (
	"bytes"
	"net/http"

	"github.com/starfederation/datastar-go/datastar"
)

// Interactivity uses Datastar (https://data-star.dev): /static/datastar.js
// is the vendored client bundle in internal/datastarjs, loaded on every
// page.
//
// Pages stay server-rendered templates, and every form and button is
// still plain HTML that works without JavaScript. To make one live,
// give it a Datastar action, for example
//
//	<form method="post" action="/x" data-on:submit="@post('/x', {contentType: 'form'})">
//
// The handler then checks isDatastar: a Datastar request gets fragments
// to patch into the page (a.patch); a plain one gets the usual redirect.
// A fragment is a named template (a {{define}} in the page's file or in
// layout.html) whose top element has an id: Datastar morphs it into the
// element on the page with the same id, keeping focus and scroll.

// isDatastar reports whether r was sent by Datastar.
func isDatastar(r *http.Request) bool {
	return r.Header.Get("Datastar-Request") == "true"
}

// fragment is a named template to render with data and patch into the
// page.
type fragment struct {
	name string
	data any
}

func frag(name string, data any) fragment { return fragment{name, data} }

// patch answers a Datastar request: it renders each fragment from
// page's template set and patches them into the page, along with the
// flash area (cleared when fl is nil).
func (a *App) patch(w http.ResponseWriter, r *http.Request, page string, fl *flash, frags ...fragment) error {
	t, ok := a.pages[page]
	if !ok {
		t = a.pages["error.html"]
	}
	// Render everything before answering, so a template error can still
	// become an ordinary error.
	var html []string
	for _, f := range append(frags, frag("flash", fl)) {
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, f.name, f.data); err != nil {
			return err
		}
		html = append(html, buf.String())
	}

	sse := datastar.NewSSE(w, r)
	for i, h := range html {
		var opts []datastar.PatchElementOption
		if i == len(html)-1 {
			// Replace the message rather than morph it, so the same
			// message twice shows (and fades) again.
			opts = append(opts, datastar.WithModeReplace())
		}
		if err := sse.PatchElements(h, opts...); err != nil {
			return err
		}
	}
	return nil
}

// patchFlash answers a Datastar request with only a message.
func (a *App) patchFlash(w http.ResponseWriter, r *http.Request, kind, msg string) error {
	return a.patch(w, r, "error.html", &flash{Kind: kind, Msg: msg})
}

// done finishes a change: a Datastar request gets frags patched in
// with the message; a plain one is redirected to target with it, and
// so is a Datastar one when there is nothing to patch.
func (a *App) done(w http.ResponseWriter, r *http.Request, target, page, kind, msg string, frags ...fragment) error {
	if !isDatastar(r) {
		redirect(w, r, target, kind, msg)
		return nil
	}
	if len(frags) == 0 {
		if msg != "" {
			setFlash(w, kind, msg)
		}
		return datastar.NewSSE(w, r).Redirect(target)
	}
	return a.patch(w, r, page, &flash{Kind: kind, Msg: msg}, frags...)
}
