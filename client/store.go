package client

// Head records the current published document for a user: the corpus as
// of the synced epoch.
type Head struct {
	Rev  int    `json:"rev"`
	Hash string `json:"hash"`
}

// PushState tracks the last document this client pushed for a user. Rev
// and Hash describe the last upload the server accepted; ServerRev
// records a newer revision the server reported in a stale verdict, so
// the next push can assert past it.
type PushState struct {
	Rev       int    `json:"rev,omitempty"`
	Hash      string `json:"hash,omitempty"`
	ServerRev int    `json:"server_rev,omitempty"`
}

// State is the client's mutable sync state. Epoch, ManifestHash, and
// Heads must always agree with each other — patches chain on exact
// published state — which is why Store.SaveState commits the whole
// value atomically.
type State struct {
	// Epoch is the last fully applied epoch, 0 before any sync.
	Epoch int `json:"epoch"`
	// ManifestHash is the patch-chain hash at Epoch: the doc hash of
	// epoch N's patch manifest, which epoch N+1's manifest must name
	// as its predecessor.
	ManifestHash string `json:"manifest_hash,omitempty"`
	// ServerKey is the pinned server public key, hex encoded.
	ServerKey string `json:"server_key,omitempty"`

	Heads map[string]Head      `json:"heads,omitempty"`
	Push  map[string]PushState `json:"push,omitempty"`
}

// Store is where a Client keeps the corpus and its sync state. Blobs
// are immutable, content-addressed by sjsonl doc hash; superseded
// documents are retained (history is evidence, and future multi-feed
// consumers need it), so implementations never delete on Put.
//
// SaveState must replace the previous state atomically: a crash may
// lose the newest state, but must never leave a torn one.
type Store interface {
	PutBlob(hash string, data []byte) error
	GetBlob(hash string) ([]byte, error)

	// LoadState returns the persisted state, or (nil, nil) if none has
	// been saved yet.
	LoadState() (*State, error)
	SaveState(*State) error
}
