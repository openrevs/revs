package server

import (
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openrevs/revs/ratingset"
	"github.com/stretchr/testify/require"
)

func newMemServer(t *testing.T, opts Options) *Server {
	t.Helper()
	db, err := OpenMemoryDB()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	s, err := New(Config{DB: db, Key: key, Publications: &MemPublications{}, Options: opts})
	require.NoError(t, err)
	return s
}

// TestRunCutsAndStops checks that Run cuts epochs on its own and
// returns once its context is done.
func TestRunCutsAndStops(t *testing.T) {
	s := newMemServer(t, Options{CutInterval: 10 * time.Millisecond})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	resp := upload(t, ts, signedDoc(t, ratingset.NewUser(), 1, "Alien"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		latest, err := s.dal.LatestChannelEpoch(context.Background(), ChannelMain)
		return err == nil && latest > 0
	}, 5*time.Second, 10*time.Millisecond, "no epoch cut")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestServeShutsDown checks the HTTP half of ListenAndServe: it serves
// the handler and shuts down cleanly when the context is done.
func TestServeShutsDown(t *testing.T) {
	s := newMemServer(t, Options{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, ln, s.Handler(), s.Run) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/v1/index.json")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-errc:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}
