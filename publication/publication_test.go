package publication

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testUID = "244eac011af63a9080fb074eeab519f04e2de2686035ae4911982d376d32beb8"

func TestParseEntryName(t *testing.T) {
	en, err := ParseEntryName(testUID + "-432.sjsonl")
	require.NoError(t, err)
	require.Equal(t, EntryName{UserID: testUID, Rev: 432}, en)

	en, err = ParseEntryName(testUID + "-19-23.sjsonl")
	require.NoError(t, err)
	require.Equal(t, EntryName{UserID: testUID, BaseRev: 19, Rev: 23}, en)

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
		"removed.json",
	} {
		_, err := ParseEntryName(name)
		require.Error(t, err, name)
	}
}

// Names round-trip through String and ParseEntryName.
func TestEntryNameString(t *testing.T) {
	for _, en := range []EntryName{{UserID: testUID, Rev: 1}, {UserID: testUID, BaseRev: 19, Rev: 23}} {
		got, err := ParseEntryName(en.String())
		require.NoError(t, err)
		require.Equal(t, en, got)
	}
	require.Equal(t, testUID+"-19-23.sjsonl", EntryName{UserID: testUID, BaseRev: 19, Rev: 23}.String())
}

// The file names the spec gives (specs/publishing-v2.md).
func TestFileNames(t *testing.T) {
	require.Equal(t, "snapshot-7_2.tar.gz", PartName(SnapshotBase(7), 2))
	require.Equal(t, "patch-0-1_1.tar.gz", PartName(PatchBase(0, 1), 1))
	require.Equal(t, "snapshot-manifest-7.sjsonl.gz", SnapshotManifestName(7))
	require.Equal(t, "patch-manifest-3-5.sjsonl.gz", PatchManifestName(3, 5))
}
