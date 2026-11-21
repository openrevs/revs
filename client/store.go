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
// state, which is why Store.UpdateState applies each change atomically.
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

// StateChange is a change to a client's State: its new channel, epoch
// and server key, and just the heads and push states that changed, so
// that saving a change costs what changed, not the size of the corpus.
type StateChange struct {
	Channel   string
	Epoch     int
	ServerKey string

	// ResetHeads drops every head before Heads is applied.
	ResetHeads bool
	// Heads holds the users whose head changed: their new head, or nil
	// if they no longer have one.
	Heads map[string]*Head
	// Push holds the users whose push state changed, and their new one.
	Push map[string]PushState
}

// Apply makes the change to s, as a Store applies it to the state it
// persists. A store that keeps the whole state as one value can
// implement UpdateState as load, Apply, save.
func (s *State) Apply(c StateChange) {
	s.Channel, s.Epoch, s.ServerKey = c.Channel, c.Epoch, c.ServerKey
	if c.ResetHeads || s.Heads == nil {
		s.Heads = map[string]Head{}
	}
	for id, h := range c.Heads {
		if h == nil {
			delete(s.Heads, id)
		} else {
			s.Heads[id] = *h
		}
	}
	if s.Push == nil {
		s.Push = map[string]PushState{}
	}
	for id, p := range c.Push {
		s.Push[id] = p
	}
}

// Store is where a Client keeps the corpus and its sync state. Blobs
// are immutable, content-addressed by sjsonl doc hash; superseded
// documents are retained, so implementations never delete on Put. Sync
// relies on that: a compacted patch applies to a user's revision as of
// an earlier epoch, which the store still holds even after newer
// revisions arrived.
type Store interface {
	PutBlob(hash string, data []byte) error
	// GetBlob returns a stored blob, or an error wrapping
	// ErrBlobNotFound if there is none.
	GetBlob(hash string) ([]byte, error)

	// LoadState returns the persisted state, or (nil, nil) if none has
	// been saved yet.
	LoadState() (*State, error)
	// UpdateState applies a change to the persisted state (an empty
	// State if none has been saved yet), as State.Apply does. It must
	// apply it atomically: a crash may lose the newest change, but must
	// never leave part of one applied.
	UpdateState(StateChange) error
}
