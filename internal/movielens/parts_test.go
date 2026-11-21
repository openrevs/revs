package movielens

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadUserParts(t *testing.T) {
	read := func(parts ...string) ([]User, error) {
		var ps []Part
		for i, p := range parts {
			ps = append(ps, Part{Name: "part" + string(rune('1'+i)), R: strings.NewReader(p)})
		}
		var us []User
		err := ReadUserParts(ps, func(u User) error { us = append(us, u); return nil })
		return us, err
	}

	// The same ratings as one file and split three ways, with and
	// without repeated headers: user 1 runs from the first part into
	// the second.
	whole, err := read(ratingsCSV)
	require.NoError(t, err)
	lines := strings.SplitAfter(ratingsCSV, "\n")
	for _, parts := range [][]string{
		{lines[0] + lines[1] + lines[2], lines[3] + lines[4] + lines[5], ratingsHeader + "\n"},
		{lines[0] + lines[1] + lines[2], ratingsHeader + "\n" + lines[3] + lines[4], ratingsHeader + "\n" + lines[5]},
		{lines[0], lines[1] + lines[2] + lines[3], "", lines[4] + lines[5]},
	} {
		us, err := read(parts...)
		require.NoError(t, err, parts)
		assert.Equal(t, whole, us, parts)
	}

	_, err = read("1,2,3.0,4\n", ratingsCSV)
	assert.ErrorContains(t, err, "part1: header")
	_, err = read("", ratingsCSV)
	assert.ErrorContains(t, err, "part1: empty")
	_, err = read(ratingsHeader+"\n1,2,3.0,4\n", "2,2,3.0,4\nx,1,1.0,1\n")
	assert.ErrorContains(t, err, "part2:2: userId")
	_, err = read(ratingsHeader+"\n1,2,3.0,4\n2,2,3.0,4\n", ratingsHeader+"\n1,3,3.0,4\n")
	assert.ErrorContains(t, err, "part2:2: user 1's ratings are not together")
	_, err = read()
	assert.Error(t, err)
}

func TestFindRatings(t *testing.T) {
	dir := func(names ...string) string {
		d := t.TempDir()
		for _, n := range names {
			require.NoError(t, os.WriteFile(filepath.Join(d, n), nil, 0o644))
		}
		return d
	}
	for _, tt := range []struct {
		files []string
		want  []string
		err   string
	}{
		{files: []string{"ratings.csv"}, want: []string{"ratings.csv"}},
		{files: []string{"ratings-1.csv"}, want: []string{"ratings-1.csv"}},
		{files: []string{"ratings-2.csv", "ratings-10.csv", "ratings-1.csv", "ratings-3.csv", "ratings-4.csv",
			"ratings-5.csv", "ratings-6.csv", "ratings-7.csv", "ratings-8.csv", "ratings-9.csv"},
			want: []string{"ratings-1.csv", "ratings-2.csv", "ratings-3.csv", "ratings-4.csv", "ratings-5.csv",
				"ratings-6.csv", "ratings-7.csv", "ratings-8.csv", "ratings-9.csv", "ratings-10.csv"}},
		// The parts win over a whole file left beside them.
		{files: []string{"ratings.csv", "ratings-1.csv", "ratings-2.csv"}, want: []string{"ratings-1.csv", "ratings-2.csv"}},
		// split's zero-padded numbers.
		{files: []string{"ratings-02.csv", "ratings-01.csv", "ratings-10.csv", "ratings-03.csv", "ratings-04.csv",
			"ratings-05.csv", "ratings-06.csv", "ratings-07.csv", "ratings-08.csv", "ratings-09.csv"},
			want: []string{"ratings-01.csv", "ratings-02.csv", "ratings-03.csv", "ratings-04.csv", "ratings-05.csv",
				"ratings-06.csv", "ratings-07.csv", "ratings-08.csv", "ratings-09.csv", "ratings-10.csv"}},
		{files: []string{"ratings-1.csv", "ratings-3.csv"}, err: "no part 2"},
		{files: []string{"ratings-2.csv"}, err: "no part 1"},
		{files: []string{"ratings-1.csv", "ratings-01.csv"}, err: "both ratings part 1"},
		{files: []string{"ratings-0.csv", "ratings-1.csv"}, err: "ratings-0.csv: ratings parts are numbered from"},
		{files: []string{"movies.csv"}, err: "neither ratings-1.csv nor ratings.csv"},
	} {
		got, err := FindRatings(dir(tt.files...))
		if tt.err != "" {
			assert.ErrorContains(t, err, tt.err, tt.files)
			continue
		}
		require.NoError(t, err, tt.files)
		assert.Equal(t, tt.want, got, tt.files)
	}
}

// A dataset with its ratings split exports the same documents as with
// them whole.
func TestSplitDataset(t *testing.T) {
	whole := writeDataset(t, 30)
	ratings, err := os.ReadFile(filepath.Join(whole, "ratings.csv"))
	require.NoError(t, err)
	lines := strings.SplitAfter(string(ratings), "\n")

	split := t.TempDir()
	for _, name := range []string{"movies.csv", "links.csv"} {
		data, err := os.ReadFile(filepath.Join(whole, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(split, name), data, 0o644))
	}
	// Three parts, cut inside user 1's ratings; only the first has the
	// header, as split -l leaves them.
	for i, part := range []string{
		strings.Join(lines[:3], ""), strings.Join(lines[3:20], ""), strings.Join(lines[20:], ""),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(split, "ratings-"+string(rune('1'+i))+".csv"), []byte(part), 0o644))
	}

	export := func(dir string) string {
		ds, err := Open(dir)
		require.NoError(t, err)
		out := t.TempDir()
		_, err = ds.Export(context.Background(), out, ExportOptions{Salt: "test"})
		require.NoError(t, err)
		return out
	}
	ds, err := Open(split)
	require.NoError(t, err)
	assert.Equal(t, []string{"ratings-1.csv", "ratings-2.csv", "ratings-3.csv"}, ds.RatingsFiles)

	a, b := export(whole), export(split)
	ents, err := os.ReadDir(a)
	require.NoError(t, err)
	require.Len(t, ents, 32+2)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".sjsonl") {
			continue
		}
		want, err := os.ReadFile(filepath.Join(a, e.Name()))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(b, e.Name()))
		require.NoError(t, err, e.Name())
		assert.Equal(t, want, got, e.Name())
	}

	// A part split mid-line is refused, not misread.
	require.NoError(t, os.WriteFile(filepath.Join(split, "ratings-1.csv"),
		[]byte(strings.Join(lines[:3], "")+"1,4,"), 0o644))
	ds, err = Open(split)
	require.NoError(t, err)
	err = ds.Users(func(User) error { return nil })
	assert.ErrorContains(t, err, "ratings-1.csv: doesn't end with a newline")
}
