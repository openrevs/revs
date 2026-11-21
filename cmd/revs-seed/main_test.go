package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/openrevs/revs/ratingset"
	"github.com/openrevs/revs/revstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dataset(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"movies.csv":  "movieId,title,genres\n1,Toy Story (1995),Comedy\n2,\"Matrix, The (1999)\",Action\n",
		"links.csv":   "movieId,imdbId,tmdbId\n1,0114709,862\n2,0133093,603\n",
		"ratings.csv": "userId,movieId,rating,timestamp\n1,1,4.0,946684800\n1,2,5.0,978307200\n2,2,3.5,978307200\n",
	}
	for name, data := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644))
	}
	return dir
}

func TestExport(t *testing.T) {
	ds := dataset(t)
	out := filepath.Join(t.TempDir(), "out")
	var stderr bytes.Buffer
	err := run(context.Background(), []string{"export", "-o", out, "-until", "2000-01-01", "-revision", "2", ds}, &stderr)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "done: 1 users, 1 ratings")
	assert.Contains(t, stderr.String(), "1 users with no ratings to include")

	doc, err := os.ReadFile(filepath.Join(out, "1.sjsonl"))
	require.NoError(t, err)
	author, _, err := ratingset.ParseRatingset(doc)
	require.NoError(t, err)
	assert.Equal(t, 2, author.Revision)
	assert.Contains(t, string(doc), `"title":"Toy Story"`)
	assert.NotContains(t, string(doc), "Matrix", "rated after -until")
	assert.NoFileExists(t, filepath.Join(out, "2.sjsonl"))
	assert.FileExists(t, filepath.Join(out, "index.csv"))
	assert.FileExists(t, filepath.Join(out, "export.json"))

	// Same salt, same author; another salt, another.
	out2 := t.TempDir()
	require.NoError(t, run(context.Background(), []string{"export", "-q", "-o", out2, ds}, &stderr))
	out3 := t.TempDir()
	require.NoError(t, run(context.Background(), []string{"export", "-q", "-salt", "other", "-o", out3, ds}, &stderr))
	a2, _, err := ratingset.ParseRatingset(must(os.ReadFile(filepath.Join(out2, "1.sjsonl"))))
	require.NoError(t, err)
	a3, _, err := ratingset.ParseRatingset(must(os.ReadFile(filepath.Join(out3, "1.sjsonl"))))
	require.NoError(t, err)
	assert.Equal(t, author.ID, a2.ID)
	assert.NotEqual(t, author.ID, a3.ID)
}

func TestUsageErrors(t *testing.T) {
	ds := dataset(t)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"-h"}, ""},
		{[]string{"frob"}, "unknown command"},
		{[]string{"export", ds}, "needs -o"},
		{[]string{"export", "-o", t.TempDir()}, "one DATASET"},
		{[]string{"export", "-o", t.TempDir(), "-sample", "0", ds}, "-sample"},
		{[]string{"export", "-o", t.TempDir(), "-until", "2000-13-01", ds}, "-until"},
		{[]string{"export", "-o", t.TempDir(), "-revision", "0", ds}, "revision"},
		{[]string{"export", "-o", t.TempDir(), t.TempDir()}, "movies.csv"},
	} {
		var stderr bytes.Buffer
		err := run(context.Background(), tt.args, &stderr)
		if tt.want == "" {
			assert.ErrorIs(t, err, flag.ErrHelp, tt.args)
			assert.Contains(t, stderr.String(), "Usage: revs-seed")
			continue
		}
		assert.ErrorContains(t, err, tt.want, tt.args)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestExportUploads checks that a server accepts exported documents as
// they are, and publishes them to clients.
func TestExportUploads(t *testing.T) {
	ds := dataset(t)
	out := t.TempDir()
	require.NoError(t, run(context.Background(), []string{"export", "-q", "-o", out, ds}, io.Discard))

	net := revstest.NewNetwork(t)
	srv := net.NewServer()
	idx := must(os.Open(filepath.Join(out, "index.csv")))
	defer idx.Close()
	recs := must(csv.NewReader(idx).ReadAll())[1:]
	require.Len(t, recs, 2)
	for _, rec := range recs {
		doc := must(os.ReadFile(filepath.Join(out, rec[0]+".sjsonl")))
		resp, err := net.HTTPClient().Post(srv.URL+"/upload", "application/jsonl", bytes.NewReader(doc))
		require.NoError(t, err)
		body := must(io.ReadAll(resp.Body))
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "user %s: %s", rec[0], body)
	}
	srv.Cut()

	c := net.NewClient(srv)
	_, err := c.Sync(context.Background())
	require.NoError(t, err)
	for _, rec := range recs {
		d, err := c.Document(rec[1])
		require.NoError(t, err, "user %s", rec[0])
		assert.Equal(t, 1, d.Rev)
		assert.Equal(t, must(os.ReadFile(filepath.Join(out, rec[0]+".sjsonl"))), d.Data)
	}
}
