package client

// Head records the current published document for a user: the corpus as
// of the synced epoch.
type Head struct {
	Rev  int    `json:"rev"`
	Hash string `json:"hash"`
}

// PushState tracks what the server is known to hold for a user. Rev and
// Hash describe the newest document it is known to hold: the last
// accepted push, or the latest it reported in a stale verdict. ServerRev
// records a newer revision the server reported, so the next push asserts
// past it. The documents themselves live in the author's History.
type PushState struct {
	Rev       int    `json:"rev,omitempty"`
	Hash      string `json:"hash,omitempty"`
	ServerRev int    `json:"server_rev,omitempty"`
}

// State is the client's mutable sync state. Epoch and Heads must
// always agree with each other, since patches apply to exact channel
// state, which is why Store.SaveState commits the whole value
// atomically.
type State struct {
	// Channel is the channel Epoch and Heads belong to.
	Channel string `json:"channel,omitempty"`
	// Epoch is the channel epoch the corpus is synced to, 0 before any
	// sync.
	Epoch int `json:"epoch"`
	// ServerKey is the pinned server public key (base64), set on first
	// sync. It survives channel switches and Rebootstrap.
	ServerKey string `json:"server_key,omitempty"`

	Heads map[string]Head      `json:"heads,omitempty"`
	Push  map[string]PushState `json:"push,omitempty"`
}

// Store is where a Client keeps the corpus and its sync state. Blobs
// are immutable, content-addressed by sjsonl doc hash; superseded
// documents are retained, so implementations never delete on Put. Sync
// relies on that: a compacted patch applies to a user's revision as of
// an earlier epoch, which the store still holds even after newer
// revisions arrived.
//
// SaveState must replace the previous state atomically: a crash may
// lose the newest state, but must never leave a torn one.
type Store interface {
	PutBlob(hash string, data []byte) error
	// GetBlob returns a stored blob, or an error wrapping
	// ErrBlobNotFound if there is none.
	GetBlob(hash string) ([]byte, error)

	// LoadState returns the persisted state, or (nil, nil) if none has
	// been saved yet.
	LoadState() (*State, error)
	SaveState(*State) error
}
