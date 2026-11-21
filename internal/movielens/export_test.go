package movielens

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ratingset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeDataset writes the synthetic dataset to a directory, with
// ratings for extra more users (ids 100 up, each rating movie 1 at
// time 1e9 + id).
func writeDataset(t *testing.T, extra int) string {
	t.Helper()
	dir := t.TempDir()
	ratings := ratingsCSV
	for i := range extra {
		ratings += fmt.Sprintf("%d,1,4.0,%d\n", 100+i, 1000000000+100+i)
	}
	for name, data := range map[string]string{"movies.csv": moviesCSV, "links.csv": linksCSV, "ratings.csv": ratings} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644))
	}
	return dir
}

func readIndex(t *testing.T, dir string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, IndexFile))
	require.NoError(t, err)
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	return recs
}

func TestExport(t *testing.T) {
	ds, err := Open(writeDataset(t, 50))
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "out")

	var calls int
	stats, err := ds.Export(context.Background(), out, ExportOptions{
		Salt: "test", Revision: 3, Keys: true, Workers: 4,
		Progress: func(ExportStats) { calls++ },
	})
	require.NoError(t, err)
	assert.Equal(t, 52, stats.Users)
	assert.Equal(t, 3+1+50, stats.Ratings)
	assert.Equal(t, 1, stats.Skipped)
	assert.Zero(t, stats.Empty)
	assert.Equal(t, 52, calls)

	idx := readIndex(t, out)
	require.Len(t, idx, 53)
	assert.Equal(t, []string{"userId", "authorId", "revision", "ratings", "bytes", "privateKey"}, idx[0])
	assert.Equal(t, "1", idx[1][0])
	assert.Equal(t, "2", idx[2][0])
	assert.Equal(t, "100", idx[3][0], "index is in user order")

	var total int64
	for _, rec := range idx[1:] {
		uid, _ := strconv.Atoi(rec[0])
		doc, err := os.ReadFile(filepath.Join(out, rec[0]+".sjsonl"))
		require.NoError(t, err)
		author, _, err := ratingset.ParseRatingset(doc)
		require.NoError(t, err)
		assert.Equal(t, AuthorID("test", uid), author.ID)
		assert.Equal(t, rec[1], author.ID)
		assert.Equal(t, 3, author.Revision)
		assert.Equal(t, strconv.Itoa(len(doc)), rec[4])
		assert.Equal(t, keys.EncodePrivate(Key("test", uid)), rec[5])
		total += int64(len(doc))
	}
	assert.Equal(t, stats.Bytes, total)

	var meta ExportMeta
	b, err := os.ReadFile(filepath.Join(out, MetaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &meta))
	assert.Equal(t, "test", meta.Salt)
	assert.Equal(t, 3, meta.Revision)
	assert.Nil(t, meta.Until)
	assert.Equal(t, stats, meta.Stats)

	// Not into a directory that has something in it.
	_, err = ds.Export(context.Background(), out, ExportOptions{Salt: "test"})
	assert.ErrorContains(t, err, "not empty")
}

func TestExportSelection(t *testing.T) {
	ds, err := Open(writeDataset(t, 2000))
	require.NoError(t, err)
	users := func(o ExportOptions) []string {
		out := t.TempDir()
		o.Salt = "test"
		_, err := ds.Export(context.Background(), out, o)
		require.NoError(t, err)
		var ids []string
		for _, rec := range readIndex(t, out)[1:] {
			ids = append(ids, rec[0])
		}
		return ids
	}

	// Without keys, there's no key column.
	out := t.TempDir()
	_, err = ds.Export(context.Background(), out, ExportOptions{Selection: Selection{Limit: 1}})
	require.NoError(t, err)
	assert.Equal(t, []string{"userId", "authorId", "revision", "ratings", "bytes"}, readIndex(t, out)[0])

	assert.Equal(t, []string{"1", "2", "100"}, users(ExportOptions{Selection: Selection{Limit: 3}}))

	tenth := users(ExportOptions{Selection: Selection{Sample: 0.1}})
	assert.InDelta(t, 200, len(tenth), 60)
	assert.Equal(t, tenth, users(ExportOptions{Selection: Selection{Sample: 0.1}}), "sampling is repeatable")
	half := users(ExportOptions{Selection: Selection{Sample: 0.5}})
	assert.Subset(t, half, tenth, "a smaller sample is within a larger one")
	assert.Equal(t, tenth[:5], users(ExportOptions{Selection: Selection{Sample: 0.1, Limit: 5}}))

	// Users with nothing before Until get no document.
	out = t.TempDir()
	stats, err := ds.Export(context.Background(), out, ExportOptions{Until: 944000000})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Users) // user 1, with Toy Story only
	assert.Equal(t, 2001, stats.Empty)
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Len(t, entries, 3) // 1.sjsonl, the index and the metadata
	b, err := os.ReadFile(filepath.Join(out, MetaFile))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"until": "1999-11-30T22:13:20Z"`)
}

func TestExportErrors(t *testing.T) {
	ds, err := Open(writeDataset(t, 0))
	require.NoError(t, err)

	_, err = ds.Export(context.Background(), t.TempDir(), ExportOptions{Revision: -1})
	assert.ErrorContains(t, err, "revision")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ds.Export(ctx, t.TempDir(), ExportOptions{})
	assert.ErrorIs(t, err, context.Canceled)

	// A bad ratings file stops the export.
	bad := writeDataset(t, 0)
	require.NoError(t, os.WriteFile(filepath.Join(bad, "ratings.csv"),
		[]byte(strings.Replace(ratingsCSV, "4.5", "4.7", 1)), 0o644))
	ds, err = Open(bad)
	require.NoError(t, err)
	_, err = ds.Export(context.Background(), t.TempDir(), ExportOptions{})
	assert.ErrorContains(t, err, "half star")

	// links.csv is optional; movies.csv isn't.
	dir := writeDataset(t, 0)
	require.NoError(t, os.Remove(filepath.Join(dir, "links.csv")))
	ds, err = Open(dir)
	require.NoError(t, err)
	assert.Nil(t, ds.Catalog[1].Refs)
	require.NoError(t, os.Remove(filepath.Join(dir, "movies.csv")))
	_, err = Open(dir)
	assert.Error(t, err)
}
