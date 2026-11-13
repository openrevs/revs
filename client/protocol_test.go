package client

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testUID = "244eac011af63a9080fb074eeab519f04e2de2686035ae4911982d376d32beb8"

func makeArchive(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		if h.Format == tar.FormatUnknown {
			h.Format = tar.FormatUSTAR
		}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		h.Mode = 0644
		h.ModTime = time.Unix(1700000000, 0)
		data := []byte(strings.Repeat("x", int(h.Size)))
		require.NoError(t, tw.WriteHeader(h))
		if h.Typeflag == tar.TypeReg {
			_, err := tw.Write(data)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return buf.Bytes()
}

func TestReadArchive(t *testing.T) {
	name := testUID + "-1.sjsonl"

	var got []string
	err := readArchive(bytes.NewReader(makeArchive(t,
		&tar.Header{Name: name, Size: 3},
		&tar.Header{Name: removedFile, Size: 2},
	)), func(name string, r io.Reader) error {
		got = append(got, name)
		return nil // unread entries are skipped
	})
	require.NoError(t, err)
	require.Equal(t, []string{name, removedFile}, got)

	bad := map[string]*tar.Header{
		"directory":     {Name: "dir/", Typeflag: tar.TypeDir},
		"symlink":       {Name: name, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"nested path":   {Name: "a/" + name, Size: 1},
		"absolute path": {Name: "/" + name, Size: 1},
		"dot-dot":       {Name: "..", Size: 1},
		"pax format":    {Name: name, Size: 1, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "x"}},
		"gnu format":    {Name: name, Size: 1, Format: tar.FormatGNU},
	}
	for what, h := range bad {
		t.Run(what, func(t *testing.T) {
			err := readArchive(bytes.NewReader(makeArchive(t, h)), func(string, io.Reader) error { return nil })
			require.Error(t, err)
		})
	}
}

func TestParseEntryName(t *testing.T) {
	en, err := parseEntryName(testUID + "-432.sjsonl")
	require.NoError(t, err)
	require.Equal(t, entryName{userID: testUID, rev: 432}, en)

	en, err = parseEntryName(testUID + "-19-23.sjsonl")
	require.NoError(t, err)
	require.Equal(t, entryName{userID: testUID, isPatch: true, baseRev: 19, rev: 23}, en)

	for _, name := range []string{
		testUID + "-1.json",
		testUID + ".sjsonl",
		testUID + "-0.sjsonl",
		testUID + "-01.sjsonl",
		testUID + "-x.sjsonl",
		testUID + "-5-5.sjsonl",
		testUID + "-6-5.sjsonl",
		testUID + "-1-2-3.sjsonl",
		strings.ToUpper(testUID) + "-1.sjsonl",
		"-1.sjsonl",
		"../" + testUID + "-1.sjsonl",
		removedFile,
	} {
		_, err := parseEntryName(name)
		require.Error(t, err, name)
	}
}
