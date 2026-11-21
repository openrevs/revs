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
	"slices"
	"strings"
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
	err := run(context.Background(), []string{"export", "-o", out, "-until", "2000-01-01", "-revision", "2", ds}, io.Discard, &stderr)
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
	require.NoError(t, run(context.Background(), []string{"export", "-q", "-o", out2, ds}, io.Discard, &stderr))
	out3 := t.TempDir()
	require.NoError(t, run(context.Background(), []string{"export", "-q", "-salt", "other", "-o", out3, ds}, io.Discard, &stderr))
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
		err := run(context.Background(), tt.args, io.Discard, &stderr)
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
	require.NoError(t, run(context.Background(), []string{"export", "-q", "-o", out, ds}, io.Discard, io.Discard))

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

func TestReplay(t *testing.T) {
	ds := dataset(t)
	dir := filepath.Join(t.TempDir(), "work")
	csvPath := filepath.Join(t.TempDir(), "steps.csv")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"replay", "-dir", dir, "-csv", csvPath,
		"-from", "1999-06-01", "-to", "2001-06-01", "-every", "6m", "-compact-every", "none", ds}, &stdout, &stderr)
	require.NoError(t, err, stderr.String())

	// Steps end 1999-12-01, 2000-06-01, 2000-12-01 and 2001-06-01. User
	// 1 rated on 2000-01-01 and 2001-01-01, user 2 on 2001-01-01. The
	// documents are too small for a patch to be worth it. The server
	// cuts no epoch when nothing has changed.
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(t, lines, 5, stdout.String())
	assert.Contains(t, lines[0], "patches")
	assert.Contains(t, stderr.String(), "done: 4 steps")
	assert.Contains(t, stderr.String(), "0 patches and 3 documents")

	f := must(os.Open(csvPath))
	defer f.Close()
	recs := must(csv.NewReader(f).ReadAll())
	require.Len(t, recs, 5)
	assert.Equal(t, csvHeader, recs[0])
	col := func(name string) []string {
		i := slices.Index(csvHeader, name)
		var out []string
		for _, r := range recs[1:] {
			out = append(out, r[i])
		}
		return out
	}
	assert.Equal(t, []string{"0", "1", "1", "2"}, col("authors"))
	assert.Equal(t, []string{"0", "1", "0", "2"}, col("pushed"))
	assert.Equal(t, []string{"0", "1", "0", "2"}, col("full"))
	assert.Equal(t, []string{"0", "1", "1", "2"}, col("epoch"))
	assert.DirExists(t, filepath.Join(dir, "server"), "-dir keeps the data")

	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"replay"}, "one DATASET"},
		{[]string{"replay", "-every", "1h", ds}, "-every"},
		{[]string{"replay", "-from", "2001-01-01", "-to", "2000-01-01", ds}, "before"},
		{[]string{"replay", "-to", "yesterday", ds}, "-to"},
		{[]string{"replay", "-compact-every", "24,x", ds}, "-compact-every"},
		{[]string{"replay", "-server", "http://x.test", "-snapshot-every", "5", ds}, "in-process"},
		{[]string{"replay", "-dir", dir, ds}, "not empty"},
	} {
		err := run(context.Background(), tt.args, io.Discard, io.Discard)
		assert.ErrorContains(t, err, tt.want, tt.args)
	}
}
