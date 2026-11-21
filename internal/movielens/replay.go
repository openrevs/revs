package movielens

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
	"github.com/tidwall/gjson"
)

// Interval is a step of calendar time, as AddDate takes it.
type Interval struct{ Years, Months, Days int }

// ParseInterval reads an interval such as "1y", "6m", "2w" or "30d".
func ParseInterval(s string) (Interval, error) {
	var n int
	var unit string
	if _, err := fmt.Sscanf(s, "%d%s", &n, &unit); err != nil || n < 1 {
		return Interval{}, fmt.Errorf("interval %q is not a count and a unit (y, m, w or d)", s)
	}
	switch unit {
	case "y":
		return Interval{Years: n}, nil
	case "m":
		return Interval{Months: n}, nil
	case "w":
		return Interval{Days: 7 * n}, nil
	case "d":
		return Interval{Days: n}, nil
	}
	return Interval{}, fmt.Errorf("interval %q has an unknown unit (want y, m, w or d)", s)
}

func (iv Interval) after(t time.Time) time.Time { return t.AddDate(iv.Years, iv.Months, iv.Days) }

// ReplayOptions control Replay.
type ReplayOptions struct {
	Selection
	Salt string // for the authors' keys; see Key

	// The replay steps from From to To, Every apart: at each step,
	// every author whose ratings changed in the step pushes their
	// document as it stands at the step's end. Zero From and To are
	// the first and last ratings of the selected users; a zero Every
	// is a year.
	From, To time.Time
	Every    Interval

	// URL is the server to push to and sync from, reached with
	// HTTPClient (http.DefaultClient if nil).
	URL        string
	HTTPClient *http.Client
	// Cut, if set, is called after each step's pushes, to publish an
	// epoch. Without it, the subscriber sees whatever the server has
	// cut on its own by then.
	Cut func(context.Context) error

	// Dir holds the client databases: the authors' histories and push
	// state, and the subscriber's corpus. Required.
	Dir string

	// Workers is how many authors push at once; 0 means GOMAXPROCS.
	Workers int

	// Step, if set, is called with each step's stats as it finishes.
	Step func(StepStats)
}

// StepStats measures one step of a replay.
type StepStats struct {
	Step  int
	Until time.Time // the step covers ratings made before this

	Authors int   // authors with a document so far
	Ratings int   // ratings in their documents
	Corpus  int64 // bytes of their latest documents

	Pushed      int   // authors who pushed a new revision this step
	New         int   // of them, authors pushing for the first time
	Patches     int   // uploads sent as sjsonl patches
	Full        int   // uploads sent as full documents
	UploadBytes int64 // bytes uploaded
	DocBytes    int64 // bytes of the pushed documents, as full documents
	PushTime    time.Duration

	CutTime time.Duration // zero without ReplayOptions.Cut

	// What a subscriber, syncing after every step, fetched.
	Epoch        int
	SyncRequests int
	SyncBytes    int64
	SyncTime     time.Duration
	Bootstrapped bool
	Changed      int // users whose document the subscriber saw change
}

// Replay plays the selected users' ratings into a server over time,
// as their authors would have pushed them, through the real client:
// each author's successive documents are committed to a history and
// pushed, as patches when that's smaller. After each step's pushes it
// cuts an epoch (with Cut) and syncs a subscriber, measuring each part.
//
// The selected users' ratings are held in memory, about 24 bytes a
// rating; sample a large dataset to replay part of it.
func Replay(ctx context.Context, ds *Dataset, o ReplayOptions) ([]StepStats, error) {
	if o.URL == "" || o.Dir == "" {
		return nil, errors.New("replay needs a server URL and a directory")
	}
	if o.Every == (Interval{}) {
		o.Every = Interval{Years: 1}
	}
	if o.Workers <= 0 {
		o.Workers = runtime.GOMAXPROCS(0)
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	users, first, last, err := loadUsers(ds, o.Selection)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, errors.New("no users selected")
	}
	if o.From.IsZero() {
		o.From = time.Unix(first, 0).UTC().Truncate(24 * time.Hour)
	}
	if o.To.IsZero() {
		o.To = time.Unix(last+1, 0).UTC()
	}
	var steps []time.Time
	for t := o.Every.after(o.From); t.Before(o.To); t = o.Every.after(t) {
		steps = append(steps, t)
	}
	steps = append(steps, o.To)

	// The authors' side: one History for all of them, and a client per
	// author, made at their first push, as each author has their own
	// app. (A client keeps one state for all the authors it pushes for
	// and saves the whole of it after each push, so sharing clients
	// between authors makes pushing cost grow with their number.)
	authorsDB, err := sqlitestore.Open(filepath.Join(o.Dir, "authors.db"))
	if err != nil {
		return nil, err
	}
	defer authorsDB.Close()
	hist := client.NewHistory(authorsDB, client.WithHistoryLimit(2))
	var up uploads
	authorHTTP := &http.Client{Transport: &up, Timeout: hc.Timeout}
	up.next = transport(hc)
	pushers := make([]*client.Client, len(users)) // each only used by one worker
	pusher := func(j int) *client.Client {
		if pushers[j] == nil {
			pushers[j] = client.New(o.URL, authorsDB.Feed(fmt.Sprintf("author-%d", users[j].ID)),
				client.WithHTTPClient(authorHTTP), client.WithHistory(hist))
		}
		return pushers[j]
	}

	subDB, err := sqlitestore.Open(filepath.Join(o.Dir, "subscriber.db"))
	if err != nil {
		return nil, err
	}
	defer subDB.Close()
	var down downloads
	down.next = transport(hc)
	sub := client.New(o.URL, subDB.Feed("subscriber"),
		client.WithHTTPClient(&http.Client{Transport: &down, Timeout: hc.Timeout}))

	// Per user: ratings and bytes in their latest document.
	type state struct {
		n    int
		size int64
	}
	states := make([]state, len(users))

	var out []StepStats
	for i, until := range steps {
		st := StepStats{Step: i + 1, Until: until}
		up.reset()

		// Push, in parallel.
		start := time.Now()
		var (
			mu       sync.Mutex
			firstErr error
			wg       sync.WaitGroup
		)
		pctx, cancel := context.WithCancel(ctx)
		for w := range o.Workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := w; j < len(users); j += o.Workers {
					if pctx.Err() != nil {
						return
					}
					u := users[j]
					content, n, _, err := ds.Catalog.Content(AuthorID(o.Salt, u.ID), u, until.Unix()-1)
					if err == nil && (n == 0 || n == states[j].n) {
						continue // nothing yet, or nothing new
					}
					var size int64
					if err == nil {
						var doc *client.Document
						if _, err = pusher(j).Push(pctx, Key(o.Salt, u.ID), content); err == nil {
							doc, err = hist.Latest(AuthorID(o.Salt, u.ID))
						}
						if doc != nil {
							size = int64(len(doc.Data))
						}
					}
					mu.Lock()
					if err != nil {
						if firstErr == nil {
							firstErr = fmt.Errorf("user %d: %w", u.ID, err)
							cancel()
						}
					} else {
						st.Pushed++
						if states[j].n == 0 {
							st.New++
						}
						st.DocBytes += size
						states[j] = state{n: n, size: size}
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		cancel()
		if firstErr == nil {
			firstErr = ctx.Err()
		}
		if firstErr != nil {
			return out, firstErr
		}
		st.PushTime = time.Since(start)
		st.Patches, st.Full, st.UploadBytes = up.counts()

		for _, s := range states {
			if s.n > 0 {
				st.Authors++
				st.Ratings += s.n
				st.Corpus += s.size
			}
		}

		if o.Cut != nil {
			start = time.Now()
			if err := o.Cut(ctx); err != nil {
				return out, fmt.Errorf("step %d: cut: %w", st.Step, err)
			}
			st.CutTime = time.Since(start)
		}

		down.reset()
		start = time.Now()
		res, err := sub.Sync(ctx)
		if err != nil {
			return out, fmt.Errorf("step %d: sync: %w", st.Step, err)
		}
		st.SyncTime = time.Since(start)
		st.SyncRequests, st.SyncBytes = down.counts()
		st.Epoch = res.Epoch
		st.Bootstrapped = res.Bootstrapped
		st.Changed = len(res.Changed)

		out = append(out, st)
		if o.Step != nil {
			o.Step(st)
		}
	}
	return out, nil
}

// loadUsers reads the selected users and the range of their ratings'
// times.
func loadUsers(ds *Dataset, sel Selection) (users []User, first, last int64, err error) {
	errLimit := errors.New("limit reached")
	err = ds.Users(func(u User) error {
		if !sel.sampled(u.ID) {
			return nil
		}
		if sel.Limit > 0 && len(users) == sel.Limit {
			return errLimit
		}
		for _, r := range u.Ratings {
			if first == 0 || r.Time < first {
				first = r.Time
			}
			last = max(last, r.Time)
		}
		users = append(users, u)
		return nil
	})
	if errors.Is(err, errLimit) {
		err = nil
	}
	return users, first, last, err
}

func transport(hc *http.Client) http.RoundTripper {
	if hc.Transport != nil {
		return hc.Transport
	}
	return http.DefaultTransport
}

// uploads counts the uploads made through it, by kind.
type uploads struct {
	next                  http.RoundTripper
	patches, full, nbytes atomic.Int64
}

func (u *uploads) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/upload") && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		u.nbytes.Add(int64(len(body)))
		ctl, _, _ := bytes.Cut(body, []byte("\n"))
		if gjson.GetBytes(ctl, "sjsonl").String() == "patch" {
			u.patches.Add(1)
		} else {
			u.full.Add(1)
		}
	}
	return u.next.RoundTrip(req)
}

func (u *uploads) reset() { u.patches.Store(0); u.full.Store(0); u.nbytes.Store(0) }

func (u *uploads) counts() (patches, full int, nbytes int64) {
	return int(u.patches.Load()), int(u.full.Load()), u.nbytes.Load()
}

// downloads counts the requests made through it and the bytes of
// their response bodies.
type downloads struct {
	next     http.RoundTripper
	requests atomic.Int64
	nbytes   atomic.Int64
}

func (d *downloads) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := d.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	d.requests.Add(1)
	resp.Body = &countingBody{ReadCloser: resp.Body, n: &d.nbytes}
	return resp, nil
}

func (d *downloads) reset() { d.requests.Store(0); d.nbytes.Store(0) }

func (d *downloads) counts() (int, int64) { return int(d.requests.Load()), d.nbytes.Load() }

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	return n, err
}
