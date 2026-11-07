package dmrs

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/kalafut/dmrs/domain"
	"github.com/kalafut/dmrs/templates"
)

const LoginPath = "/login"
const RegisterPath = "/register"

const sessionUserKey = "user_id"

type Client struct {
	dal      domain.DAL
	db       *DB
	settings domain.Settings
	router   chi.Router
	sessions *scs.SessionManager
	tmpl     *templates.Renderer
}

func NewClient(dataDir string) (*Client, error) {
	db, err := GetDB(dataDir)
	if err != nil {
		panic(err)
	}

	tmpl, err := templates.New()
	if err != nil {
		return nil, err
	}

	sessions := scs.New()
	sessions.Store = newSessionStore(db.DB)
	sessions.Lifetime = 7 * 24 * time.Hour

	client := &Client{
		dal:      domain.NewDAL(db.DB),
		db:       db,
		router:   chi.NewRouter(),
		sessions: sessions,
		tmpl:     tmpl,
	}

	if err := client.loadSettings(); err != nil {
		return nil, err
	}

	return client, nil
}

func (c *Client) loadSettings() error {
	return c.dal.AppData(context.Background(), settingsKey, &c.settings)
}

const DefaultClientListen = ":1324"

func Start(dataDir, listen string, demo bool) {
	client, err := NewClient(dataDir)
	if err != nil {
		panic(err)
	}
	client.initRoutes()

	if listen == "" {
		listen = DefaultClientListen
	}
	log.Fatal(http.ListenAndServe(listen, client.sessions.LoadAndSave(client.router)))
}

// handlerFunc is an http handler that may return an error, which is rendered
// as a 500.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (c *Client) h(f handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f(w, r); err != nil {
			log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (c *Client) render(w http.ResponseWriter, name string, data interface{}) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return c.tmpl.Render(w, name, data, baseLayout)
}

type ctxKey int

const userCtxKey ctxKey = iota

// currentUser returns the logged-in user placed in the request context by
// userLoaderMiddleware.
func currentUser(r *http.Request) *domain.User {
	return r.Context().Value(userCtxKey).(*domain.User)
}

func (c *Client) userLoaderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == LoginPath || r.URL.Path == RegisterPath {
			next.ServeHTTP(w, r)
			return
		}

		userID := c.sessions.GetInt(r.Context(), sessionUserKey)
		if userID == 0 {
			http.Redirect(w, r, LoginPath, http.StatusSeeOther)
			return
		}

		user, err := c.dal.User(r.Context(), userID)
		if err != nil {
			// Stale session (e.g. the user no longer exists); start over.
			_ = c.sessions.Destroy(r.Context())
			http.Redirect(w, r, LoginPath, http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
