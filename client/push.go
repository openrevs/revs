package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	jsonlines "github.com/kalafut/sjsonl"
)

// PushResult summarizes one Push call. Pushed is false when the content
// was unchanged from what the server already holds, so no upload was
// made; Rev and Hash then describe that existing revision.
type PushResult struct {
	Pushed bool
	Rev    int
	Hash   string
}

// uploadVerdict is the server's response to an upload.
type uploadVerdict struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Rev    int    `json:"rev"`
	Hash   string `json:"hash"`
}

// Push signs content with key and uploads it, owning the protocol's
// revision discipline: the revision field is asserted by this call (any
// value already in the content is overwritten), bumped only past the
// newest revision this client knows for the user, and not bumped at all
// — no upload happens — when the content is unchanged.
//
// content is unsigned JSONL data lines, exactly one of which carries the
// user's "id" (which must match the id derived from key).
//
// A StaleError means the server holds a newer revision than we knew —
// typically a push from another device. It is surfaced rather than
// retried because pushing over it may stomp that device's data; the
// server's revision is recorded, so once the application has resolved
// the conflict (e.g. merged content), the next Push asserts past it.
func (c *Client) Push(ctx context.Context, key ed25519.PrivateKey, content []byte) (*PushResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureState(); err != nil {
		return nil, err
	}

	uid := userIDFromKey(key.Public().(ed25519.PublicKey))

	lines, idLine, contentID, err := parseContent(content)
	if err != nil {
		return nil, err
	}
	if contentID != uid {
		return nil, fmt.Errorf("client: content id %q does not match signing key (user %s)", contentID, uid)
	}

	ps := c.state.Push[uid]
	head := c.state.Heads[uid]
	base := max(ps.Rev, ps.ServerRev, head.Rev)

	if base > 0 {
		// Changed means changed with the revision held constant: sign a
		// candidate at the base revision and compare hashes against the
		// last push and the pulled head. Equal to either → the server
		// already holds this content; an upload would be an empty bump.
		_, candHash, err := signWithRev(lines, idLine, base, key)
		if err != nil {
			return nil, err
		}
		if (ps.Rev == base && ps.Hash == candHash) || (head.Rev == base && head.Hash == candHash) {
			c.state.Push[uid] = PushState{Rev: base, Hash: candHash}
			if err := c.store.SaveState(c.state); err != nil {
				return nil, err
			}
			return &PushResult{Pushed: false, Rev: base, Hash: candHash}, nil
		}
	}

	rev := base + 1
	signed, hash, err := signWithRev(lines, idLine, rev, key)
	if err != nil {
		return nil, err
	}

	verdict, status, err := c.upload(ctx, signed)
	if err != nil {
		return nil, err
	}

	switch status {
	case http.StatusOK:
		c.state.Push[uid] = PushState{Rev: rev, Hash: hash}
		if err := c.store.SaveState(c.state); err != nil {
			return nil, err
		}
		return &PushResult{Pushed: true, Rev: rev, Hash: hash}, nil

	case http.StatusConflict:
		// Record the fact of the newer revision — the content decision
		// stays with the application.
		serverRev := max(verdict.Rev, rev)
		c.state.Push[uid] = PushState{Rev: ps.Rev, Hash: ps.Hash, ServerRev: serverRev}
		if err := c.store.SaveState(c.state); err != nil {
			return nil, err
		}
		return nil, &StaleError{UserID: uid, LatestRev: serverRev, Reason: verdict.Reason}

	case http.StatusBadRequest:
		return nil, &RejectedError{Reason: verdict.Reason}

	default:
		return nil, fmt.Errorf("client: upload failed with status %d: %s", status, verdict.Reason)
	}
}

func (c *Client) upload(ctx context.Context, doc []byte) (*uploadVerdict, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/upload", bytes.NewReader(doc))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/jsonl")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	var verdict uploadVerdict
	if err := json.Unmarshal(body, &verdict); err != nil {
		verdict.Reason = string(body)
	}

	return &verdict, resp.StatusCode, nil
}

// parseContent splits unsigned JSONL content and locates the single
// data line carrying the user's "id".
func parseContent(content []byte) (lines [][]byte, idLine int, id string, err error) {
	lines = splitLines(content)
	idLine = -1

	for i, line := range lines {
		var probe struct {
			ID *string `json:"id"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, 0, "", fmt.Errorf("client: content line %d: %w", i+1, err)
		}
		if probe.ID == nil {
			continue
		}
		if idLine >= 0 {
			return nil, 0, "", fmt.Errorf("client: content has multiple id lines (%d and %d)", idLine+1, i+1)
		}
		idLine = i
		id = *probe.ID
	}

	if idLine < 0 {
		return nil, 0, "", fmt.Errorf("client: content has no id line")
	}

	return lines, idLine, id, nil
}

// signWithRev sets the asserted revision on the id line, signs the
// content, and returns the signed document and its doc hash.
func signWithRev(lines [][]byte, idLine, rev int, key ed25519.PrivateKey) ([]byte, string, error) {
	var m map[string]any
	if err := json.Unmarshal(lines[idLine], &m); err != nil {
		return nil, "", err
	}
	m["revision"] = rev
	revised, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	for i, line := range lines {
		if i == idLine {
			buf.Write(revised)
		} else {
			buf.Write(line)
		}
		buf.WriteByte('\n')
	}

	signed, info, err := jsonlines.Sign(buf.Bytes(), key)
	if err != nil {
		return nil, "", fmt.Errorf("client: signing content: %w", err)
	}

	return signed, info.Hash, nil
}
