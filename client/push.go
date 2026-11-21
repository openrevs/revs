package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	jsonlines "github.com/openrevs/sjsonl-go"
)

// PushResult summarizes one Push call. Pushed is false when the server
// already held the version, so nothing was uploaded; Rev and Hash
// describe the version either way.
type PushResult struct {
	Pushed bool
	Rev    int
	Hash   string
}

// uploadVerdict is the server's response to an upload. On a conflict,
// Rev and Hash describe the server's latest document for the user.
type uploadVerdict struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Rev    int    `json:"rev"`
	Hash   string `json:"hash"`
}

// statusBaseMismatch is the server's verdict when a patch upload's base
// is not the latest document it holds for the user.
const statusBaseMismatch = "base_mismatch"

// maxUploadAttempts bounds Push's uploads: a patch against the version
// the server is believed to hold, a patch against the version it then
// reports, and a full document.
const maxUploadAttempts = 3

// Push commits content to the author's History (see History.Commit,
// which owns revision assignment) and uploads the resulting version.
//
// The upload is a patch against the version this server is believed to
// hold, when the History (or this server's pulled corpus) has that
// version and the patch is smaller. Servers can pull from each other,
// so that belief may be out of date; if the server reports a different
// base, Push patches against that one instead, or falls back to the
// full document.
//
// A StaleError means the server holds a newer revision than the version
// — typically a push from another device. It is surfaced rather than
// retried because pushing over it may stomp that device's data; the
// server's revision is recorded, so once the application has resolved
// the conflict (e.g. merged content), the next Push asserts past it.
func (c *Client) Push(ctx context.Context, key ed25519.PrivateKey, content []byte) (*PushResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.history == nil {
		return nil, errors.New("client: Push needs a History (WithHistory) or a Store that implements HistoryStore")
	}
	h := c.history

	if err := c.ensureState(); err != nil {
		return nil, err
	}

	uid := userIDFromKey(key.Public().(ed25519.PublicKey))
	ps := c.state.Push[uid]
	head := c.state.Heads[uid]

	// Fold what this server is known to hold into the History, so the
	// version asserts past it, and pulled content that is unchanged is
	// recognized as such.
	if head.Rev > 0 {
		data, err := c.store.GetBlob(head.Hash)
		if err != nil {
			return nil, fmt.Errorf("client: loading head document %s: %w", head.Hash, err)
		}
		if err := h.adopt(&Document{UserID: uid, Rev: head.Rev, Hash: head.Hash, Data: data}); err != nil {
			return nil, err
		}
	}
	for _, known := range []Head{head, {Rev: ps.Rev, Hash: ps.Hash}, {Rev: ps.ServerRev}} {
		if err := h.NoteRev(uid, known.Rev, known.Hash); err != nil {
			return nil, err
		}
	}

	v, err := h.Commit(key, content)
	if err != nil {
		return nil, err
	}

	// The newest document this server is believed to hold.
	held := Head{Rev: ps.Rev, Hash: ps.Hash}
	if head.Rev > held.Rev {
		held = head
	}

	done := func(pushed bool) (*PushResult, error) {
		c.setPush(uid, PushState{Rev: v.Rev, Hash: v.Hash})
		if err := c.save(); err != nil {
			return nil, err
		}
		return &PushResult{Pushed: pushed, Rev: v.Rev, Hash: v.Hash}, nil
	}

	if held.Hash == v.Hash {
		return done(false)
	}

	for range maxUploadAttempts {
		body := v.Data
		if patch := c.patchFrom(uid, held.Hash, v.Data); patch != nil {
			body = patch
		}

		verdict, status, err := c.upload(ctx, body)
		if err != nil {
			return nil, err
		}

		switch {
		case status == http.StatusOK:
			return done(true)

		case status == http.StatusConflict && verdict.Hash == v.Hash:
			// The server already holds this version, e.g. pulled from
			// another server.
			return done(false)

		case status == http.StatusConflict && verdict.Status == statusBaseMismatch && verdict.Rev < v.Rev:
			// The server holds an older version than we thought (or
			// none): patch against that one, or send the full document.
			held = Head{Rev: verdict.Rev, Hash: verdict.Hash}
			continue

		case status == http.StatusConflict:
			// Record the fact of the newer revision — the content
			// decision stays with the application.
			serverRev := max(verdict.Rev, v.Rev)
			c.setPush(uid, PushState{Rev: verdict.Rev, Hash: verdict.Hash, ServerRev: serverRev})
			if err := c.save(); err != nil {
				return nil, err
			}
			if err := h.NoteRev(uid, serverRev, ""); err != nil {
				return nil, err
			}
			return nil, &StaleError{UserID: uid, LatestRev: serverRev, Reason: verdict.Reason}

		case status == http.StatusBadRequest:
			return nil, &RejectedError{Reason: verdict.Reason}

		default:
			return nil, fmt.Errorf("client: upload failed with status %d: %s", status, verdict.Reason)
		}
	}

	return nil, fmt.Errorf("client: upload of revision %d not settled after %d attempts", v.Rev, maxUploadAttempts)
}

// patchFrom returns a patch from the user's document with hash base to
// doc, or nil if a full upload is the better choice: no base, the base
// isn't held (in the History, or in this server's pulled corpus), or the
// patch isn't smaller. Callers hold c.mu.
func (c *Client) patchFrom(uid, base string, doc []byte) []byte {
	if base == "" {
		return nil
	}

	var baseDoc []byte
	if v, err := c.history.Version(uid, base); err == nil {
		baseDoc = v.Data
	} else if data, err := c.store.GetBlob(base); err == nil {
		baseDoc = data
	} else {
		return nil
	}

	patch, err := jsonlines.Diff(baseDoc, doc)
	if err != nil || len(patch) >= len(doc) {
		return nil
	}

	return patch
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
