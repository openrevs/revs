// Package datastarjs holds the vendored Datastar client bundle
// (https://data-star.dev), served by both the local web UI and the
// server's admin UI. Its version is on the bundle's first line.
package datastarjs

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

//go:embed datastar.js
var bundle []byte

// Handler serves the bundle as JavaScript.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		http.ServeContent(w, r, "datastar.js", time.Time{}, bytes.NewReader(bundle))
	})
}
