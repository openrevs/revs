// Package ui is the personal web UI behind `revs ui`: a local,
// single-user site over a collection.Collection (see that package for
// where data lives).
//
// Pages are server-rendered HTML forms; the only JavaScript is a few
// inline confirm() prompts.
package ui

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/internal/httplog"
)

// DefaultListen is the default address for `revs ui`.
const DefaultListen = "127.0.0.1:1325"

type Options struct {
	DataDir     string
	Listen      string
	OpenBrowser bool
	// WikidataURL overrides the Wikidata API endpoint (for tests).
	WikidataURL string
	// HTTPClient is used to talk to servers (for tests).
	HTTPClient *http.Client
	// LogRequests logs every HTTP request served (method, path, status,
	// size, duration); never bodies.
	LogRequests bool
}

type App struct {
	c     *collection.Collection
	pages pages
}

// New opens the collection in the data directory.
func New(opts Options) (*App, error) {
	c, err := collection.Open(collection.Options{DataDir: opts.DataDir, WikidataURL: opts.WikidataURL, HTTPClient: opts.HTTPClient})
	if err != nil {
		return nil, err
	}
	pages, err := parsePages()
	if err != nil {
		c.Close()
		return nil, err
	}
	return &App{c: c, pages: pages}, nil
}

func (a *App) Close() error { return a.c.Close() }

// Run serves the UI until ctx is canceled.
func Run(ctx context.Context, opts Options) error {
	app, err := New(opts)
	if err != nil {
		return err
	}
	defer app.Close()

	listen := opts.Listen
	if listen == "" {
		listen = DefaultListen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	handler := app.Handler(isLoopback(ln.Addr()))
	if opts.LogRequests {
		handler = httplog.Middleware(nil, handler)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	addr := ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() || addr.IP.IsLoopback() {
		host = "localhost"
	}
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(host, strconv.Itoa(addr.Port)))
	fmt.Printf("revs ui: serving %s (data in %s)\n", uiURL, opts.DataDir)

	if opts.OpenBrowser {
		if err := openBrowser(uiURL); err != nil {
			log.Printf("revs ui: could not open a browser (%v); visit %s", err, uiURL)
		}
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
