package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openrevs/revs/internal/datastarjs"
	"github.com/openrevs/revs/internal/httplog"
	"github.com/starfederation/datastar-go/datastar"
)

// The admin UI is a small web app for the server's operator, served on
// its own address (Options.AdminListen) so it can be kept off the
// public one. It is protected by a single password (Options.
// AdminPassword); failed logins are rate limited. For now it shows the
// server's state (see Stats), kept current over a Datastar stream.

const (
	// adminSessionTTL is how long an admin login lasts.
	adminSessionTTL = 12 * time.Hour
	// adminLoginBurst failed logins are allowed at once; after that, one
	// more every adminLoginEvery. The limit is global rather than per
	// client address: behind a proxy every client has the same address,
	// and a per-address limit lets distributed guessing through.
	adminLoginBurst = 5
	adminLoginEvery = 12 * time.Second
	// adminRefresh is how often an open dashboard is brought up to date.
	adminRefresh = 5 * time.Second

	adminCookie = "revs_admin"
)

//go:embed admin
var adminFS embed.FS

type admin struct {
	s *Server
	// password is the SHA-256 of the admin password, or nil if there
	// is none, in which case nobody can log in.
	password []byte
	pages    *template.Template
	refresh  time.Duration
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	// tokens and filled are the failed-login bucket: tokens left as of
	// filled.
	tokens float64
	filled time.Time
}

func newAdmin(s *Server, password string) (*admin, error) {
	t, err := template.New("admin").Funcs(adminFuncs).ParseFS(adminFS, "admin/*.html")
	if err != nil {
		return nil, err
	}
	a := &admin{
		s:        s,
		pages:    t,
		refresh:  adminRefresh,
		now:      time.Now,
		sessions: map[string]time.Time{},
		tokens:   adminLoginBurst,
	}
	if password != "" {
		sum := sha256.Sum256([]byte(password))
		a.password = sum[:]
	}
	return a, nil
}

// AdminHandler returns the admin UI's HTTP handler, for applications
// that serve it themselves; ListenAndServe serves it on AdminListen.
// Without an AdminPassword, nobody can log in.
func (s *Server) AdminHandler() http.Handler {
	a := s.admin
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	})

	r.Handle("/static/datastar.js", datastarjs.Handler())
	r.Get("/static/admin.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, adminFS, "admin/admin.css")
	})
	r.Get("/login", a.loginPage)
	r.Post("/login", a.login)
	r.Post("/logout", a.logout)
	r.Get("/", a.dashboard)
	r.Get("/updates", a.updates)

	var h http.Handler = http.NewCrossOriginProtection().Handler(r)
	if s.logRequests {
		h = httplog.Middleware(nil, h)
	}
	return h
}

// adminView is what the admin page templates receive.
type adminView struct {
	Title string
	Error string
	Stats *Stats
	Now   time.Time
}

func (a *admin) render(w http.ResponseWriter, status int, name string, v adminView) {
	var buf bytes.Buffer
	if err := a.pages.ExecuteTemplate(&buf, name, v); err != nil {
		slog.Error("admin: rendering page", "page", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (a *admin) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.session(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, http.StatusOK, "login.html", adminView{Title: "Log in"})
}

func (a *admin) login(w http.ResponseWriter, r *http.Request) {
	if a.password == nil {
		a.render(w, http.StatusForbidden, "login.html", adminView{Title: "Log in",
			Error: "The admin UI has no password set, so nobody can log in."})
		return
	}

	// The password is only checked if the limit allows another failure,
	// so guessing can't go faster than the limit even with the right
	// password among the guesses.
	if wait := a.loginWait(); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		a.render(w, http.StatusTooManyRequests, "login.html", adminView{Title: "Log in",
			Error: fmt.Sprintf("Too many failed attempts. Try again in %d seconds.", secs)})
		return
	}

	sum := sha256.Sum256([]byte(r.PostFormValue("password")))
	if subtle.ConstantTimeCompare(sum[:], a.password) != 1 {
		a.loginFailed()
		slog.Warn("admin: failed login", "remote", r.RemoteAddr)
		a.render(w, http.StatusUnauthorized, "login.html", adminView{Title: "Log in", Error: "Wrong password."})
		return
	}

	token := a.newSession()
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(adminSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
	slog.Info("admin: logged in", "remote", r.RemoteAddr)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *admin) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(adminCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *admin) dashboard(w http.ResponseWriter, r *http.Request) {
	if !a.session(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	st, err := a.s.Stats(r.Context())
	if err != nil {
		slog.Error("admin: stats", "error", err)
		http.Error(w, "could not read the server's state", http.StatusInternalServerError)
		return
	}
	a.render(w, http.StatusOK, "dashboard.html", adminView{Title: "Server", Stats: &st, Now: a.now()})
}

// updates streams the dashboard's stats to Datastar, patching them in
// whenever they change, until the client goes away or the session
// ends.
func (a *admin) updates(w http.ResponseWriter, r *http.Request) {
	if !a.session(r) {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}

	sse := datastar.NewSSE(w, r)
	ticker := time.NewTicker(a.refresh)
	defer ticker.Stop()

	var last string
	for {
		if !a.session(r) {
			sse.Redirect("/login")
			return
		}
		st, err := a.s.Stats(r.Context())
		if err != nil {
			if r.Context().Err() == nil {
				slog.Error("admin: stats", "error", err)
			}
			return
		}
		var buf bytes.Buffer
		if err := a.pages.ExecuteTemplate(&buf, "stats", adminView{Stats: &st, Now: a.now()}); err != nil {
			slog.Error("admin: rendering stats", "error", err)
			return
		}
		if html := buf.String(); html != last {
			if err := sse.PatchElements(html); err != nil {
				return
			}
			last = html
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// session reports whether r carries a live admin session.
func (a *admin) session(r *http.Request) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	if ok && !a.now().Before(exp) {
		delete(a.sessions, c.Value)
		return false
	}
	return ok
}

func (a *admin) newSession() string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for t, exp := range a.sessions {
		if !now.Before(exp) {
			delete(a.sessions, t)
		}
	}
	a.sessions[token] = now.Add(adminSessionTTL)
	return token
}

// refill brings the failed-login bucket up to date. a.mu must be held.
func (a *admin) refill() {
	now := a.now()
	if !a.filled.IsZero() {
		a.tokens = min(adminLoginBurst, a.tokens+float64(now.Sub(a.filled))/float64(adminLoginEvery))
	}
	a.filled = now
}

// loginWait returns how long until another failed login is allowed,
// or 0 if one is now.
func (a *admin) loginWait() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refill()
	if a.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - a.tokens) * float64(adminLoginEvery))
}

func (a *admin) loginFailed() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refill()
	a.tokens = max(0, a.tokens-1)
}

var adminFuncs = template.FuncMap{
	"ago":   ago,
	"bytes": humanBytes,
	"utc":   func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") },
	"sub":   func(a, b int) int { return a - b },
}

// ago describes how long before now t was, coarsely.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	default:
		return plural(int(d/(24*time.Hour)), "day") + " ago"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
