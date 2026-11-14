package revs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/domain"
)

// DefaultPullInterval is how often a server pulls from its upstreams.
const DefaultPullInterval = 15 * time.Minute

// upstreamTimeout bounds a single HTTP request to an upstream. Snapshot
// chunks can be hundreds of megabytes, so it is generous.
const upstreamTimeout = 10 * time.Minute

// upstream is another server this one pulls from. A server pulls the
// way a client does: it syncs the upstream's published corpus into a
// feed of its own database, verifying the upstream's manifests and
// every author signature, and then accepts the documents that are newer
// than what it holds, recording the upstream as their source.
type upstream struct {
	url    string
	client *client.Client

	// reconcile makes the next pull consider every document the feed
	// holds rather than only those the sync changed. It is set at
	// startup and after a failed ingest, so documents synced but not yet
	// accepted (a crash, a database error) are picked up again.
	reconcile bool
}

// AddUpstream configures the server to pull from another server's
// published corpus. Call it before the server starts.
func (s *Server) AddUpstream(rawURL string) error {
	u, err := normalizeUpstreamURL(rawURL)
	if err != nil {
		return err
	}
	for _, up := range s.upstreams {
		if up.url == u {
			return fmt.Errorf("upstream %s is listed twice", u)
		}
	}

	cl := client.New(u, s.corpus.upstreamFeed(u),
		client.WithHTTPClient(&http.Client{Timeout: upstreamTimeout}))
	s.upstreams = append(s.upstreams, &upstream{url: u, client: cl, reconcile: true})

	return nil
}

func normalizeUpstreamURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("upstream %q is not a server URL like https://revs.example.com", raw)
	}
	return raw, nil
}

// Pull pulls once from every upstream, in turn. A failing upstream is
// logged and doesn't stop the others; the returned error joins them all.
func (s *Server) Pull(ctx context.Context) error {
	s.pullMu.Lock()
	defer s.pullMu.Unlock()

	var errs []error
	for _, up := range s.upstreams {
		if err := s.pullUpstream(ctx, up); err != nil {
			slog.Error("pull failed", "upstream", up.url, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", up.url, err))
		}
	}

	return errors.Join(errs...)
}

func (s *Server) pullUpstream(ctx context.Context, up *upstream) error {
	res, err := up.client.Sync(ctx)

	var diverged *client.DivergedError
	if errors.As(err, &diverged) {
		// The upstream's chain no longer matches what we pulled (it was
		// restored from a backup, say). Nobody is around to recover by
		// hand, so rebuild the feed from its newest snapshot. Documents
		// already accepted are unaffected: they're individually signed.
		slog.Warn("upstream diverged; rebootstrapping", "upstream", up.url, "error", err)
		res, err = up.client.Rebootstrap(ctx)
	}
	// A KeyChangedError lands here too: the upstream is signing with a
	// different key than the one pinned, which needs an operator.
	if err != nil {
		return err
	}

	ids := res.Changed
	if up.reconcile {
		ids = nil
		for doc, err := range up.client.Documents() {
			if err != nil {
				return err
			}
			ids = append(ids, doc.UserID)
		}
	}

	accepted, err := s.ingestUpstream(ctx, up, ids)
	if err != nil {
		up.reconcile = true
		return err
	}
	up.reconcile = false

	if res.Epoch != res.FromEpoch || accepted > 0 {
		attrs := []any{"upstream", up.url, "from_epoch", res.FromEpoch, "epoch", res.Epoch,
			"changed", len(res.Changed), "accepted", accepted}
		if res.Bootstrapped {
			attrs = append(attrs, "bootstrap", res.BootstrapReason)
		}
		slog.Info("pulled upstream", attrs...)
	}

	return nil
}

// ingestUpstream accepts the upstream feed's documents for the given
// users where they are newer than what this server holds. A document
// failing the server's own checks is logged and skipped; it isn't
// retried until the upstream publishes a new revision.
func (s *Server) ingestUpstream(ctx context.Context, up *upstream, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	latestDocs, err := s.dal.LatestDocuments(ctx)
	if err != nil {
		return 0, err
	}
	latest := make(map[string]*domain.Document, len(latestDocs))
	for _, d := range latestDocs {
		latest[d.Userid] = d
	}

	accepted := 0
	for _, id := range ids {
		doc, err := up.client.Document(id)
		if errors.Is(err, client.ErrNotFound) {
			continue // dropped from the upstream's corpus
		}
		if err != nil {
			return accepted, err
		}

		held := latest[id]
		if held != nil && doc.Rev <= held.Rev {
			continue
		}

		status, resp, _, _ := s.acceptDocument(ctx, doc.Data, held, up.url)
		switch {
		case status == http.StatusOK:
			accepted++
		case status == http.StatusConflict:
			// An upload or another upstream got there first.
		case status >= 500:
			return accepted, fmt.Errorf("accepting %s rev %d: %s", id, doc.Rev, resp.Reason)
		default:
			slog.Warn("rejected upstream document", "upstream", up.url,
				"user", id, "rev", doc.Rev, "reason", resp.Reason)
		}
	}

	return accepted, nil
}

// startPuller pulls from every upstream right away and then on a fixed
// interval.
func (s *Server) startPuller(ctx context.Context, interval time.Duration) {
	if len(s.upstreams) == 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			s.Pull(ctx) // failures are logged per upstream

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
