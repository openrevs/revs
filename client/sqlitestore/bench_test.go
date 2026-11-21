package sqlitestore_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/client/sqlitestore"
)

// BenchmarkUpdatePush saves one push state in a feed holding a corpus
// of 20,000 heads: the cost of recording a push.
func BenchmarkUpdatePush(b *testing.B) {
	db, err := sqlitestore.Open(filepath.Join(b.TempDir(), "c.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	s := db.Feed("f")
	heads := map[string]*client.Head{}
	for i := range 20000 {
		heads[fmt.Sprintf("u%d", i)] = &client.Head{Rev: 1, Hash: fmt.Sprintf("%064x", i)}
	}
	if err := s.UpdateState(client.StateChange{Channel: "main", Epoch: 1, Heads: heads}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := range b.N {
		err := s.UpdateState(client.StateChange{Channel: "main", Epoch: 1,
			Push: map[string]client.PushState{"me": {Rev: i + 1, Hash: "ab"}}})
		if err != nil {
			b.Fatal(err)
		}
	}
}
