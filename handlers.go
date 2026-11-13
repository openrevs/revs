package revs

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openrevs/revs/domain"
	"github.com/openrevs/revs/util"
	jsonlines "github.com/openrevs/sjsonl-go"
)

// maxDocumentSize bounds the size of an accepted ratingset. It also caps
// the upload body, but is checked again after any patch is applied, so a
// series of small patches can't grow a document without limit.
const maxDocumentSize = 1 << 20

// uploadResponse is the upload verdict. On a conflict (stale or
// base_mismatch), Rev and Hash describe the user's latest accepted
// document, so a client can tell whether the server already holds its
// version, or patch against the one it does hold.
type uploadResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Rev    int    `json:"rev,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

// uploadPost handles document uploads synchronously: verify, check rev
// monotonicity, persist, and return a final verdict.
func (s *Server) uploadPost(w http.ResponseWriter, req *http.Request) {
	var userID string
	var rev int
	respond := func(status int, resp uploadResponse) {
		logUpload(req, status, userID, rev, resp)
		writeJSON(w, status, resp)
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxDocumentSize))
	if err != nil {
		respond(http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: err.Error()})
		return
	}

	// A patch upload must be based on the latest accepted document. The
	// result is the full signed document, which goes through exactly the
	// same checks as a full upload.
	var latest *domain.Document
	if ctl, isPatch, err := patchControl(body); isPatch {
		if err != nil {
			respond(http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: err.Error()})
			return
		}
		latest, err = s.dal.LatestDocument(req.Context(), ctl.userID)
		if err != nil {
			respond(http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
			return
		}
		if latest == nil || latest.Hash != ctl.Base {
			resp := uploadResponse{Status: "base_mismatch", Reason: "patch base is not the latest accepted document"}
			if latest != nil {
				resp.Rev, resp.Hash = latest.Rev, latest.Hash
			}
			respond(http.StatusConflict, resp)
			return
		}

		base, err := s.docs.Get(latest.Hash)
		if err != nil {
			respond(http.StatusInternalServerError, uploadResponse{Status: "error", Reason: fmt.Sprintf("loading base document: %v", err)})
			return
		}
		body, _, err = jsonlines.Patch(base, body)
		if err != nil {
			respond(http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: fmt.Sprintf("applying patch: %v", err)})
			return
		}
	}

	if len(body) > maxDocumentSize {
		respond(http.StatusBadRequest, uploadResponse{
			Status: "invalid",
			Reason: fmt.Sprintf("document is %d bytes, exceeding the %d byte limit", len(body), maxDocumentSize),
		})
		return
	}

	user, doc, err := ParseRatingset(body)
	if err != nil {
		respond(http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: err.Error()})
		return
	}
	userID, rev = user.ID, user.Revision

	if user.Revision < 1 {
		respond(http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: "revision must be at least 1"})
		return
	}

	if latest == nil {
		latest, err = s.dal.LatestDocument(req.Context(), user.ID)
		if err != nil {
			respond(http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
			return
		}
	}

	if latest != nil && user.Revision <= latest.Rev {
		respond(http.StatusConflict, uploadResponse{
			Status: "stale",
			Reason: "revision is not greater than the latest accepted revision",
			Rev:    latest.Rev,
			Hash:   latest.Hash,
		})
		return
	}

	// Store the canonical encoding so the blob's bytes always match the
	// doc hash that keys them.
	canonical := doc.JSON()
	info := doc.Info()

	if err := s.docs.Put(info.Hash, canonical); err != nil {
		respond(http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
		return
	}

	if err := s.dal.AddDocument(req.Context(), &domain.Document{
		Userid:       user.ID,
		Rev:          user.Revision,
		Hash:         info.Hash,
		Size:         len(canonical),
		ReceivedTime: util.Now(),
		Direct:       true,
	}); err != nil {
		// A concurrent upload of the same rev loses the (userid, rev)
		// uniqueness race; report it as stale rather than a server error.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			respond(http.StatusConflict, uploadResponse{Status: "stale", Reason: "revision already accepted"})
			return
		}
		respond(http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
		return
	}

	respond(http.StatusOK, uploadResponse{Status: "accepted", Rev: user.Revision, Hash: info.Hash})
}

// logUpload records the outcome of an upload: Info for accepted, Warn for
// client-side rejections, Error for server failures.
func logUpload(req *http.Request, status int, userID string, rev int, resp uploadResponse) {
	level := slog.LevelInfo
	switch {
	case status >= 500:
		level = slog.LevelError
	case status >= 400:
		level = slog.LevelWarn
	}

	attrs := []any{"status", resp.Status, "remote", req.RemoteAddr}
	if userID != "" {
		attrs = append(attrs, "user", userID, "rev", rev)
	}
	if resp.Hash != "" {
		attrs = append(attrs, "hash", resp.Hash)
	}
	if resp.Reason != "" {
		attrs = append(attrs, "reason", resp.Reason)
	}
	if resp.Status == "stale" && resp.Rev != 0 {
		attrs = append(attrs, "latest_rev", resp.Rev)
	}

	slog.Log(req.Context(), level, "upload", attrs...)
}

// uploadPatch is what the upload handler needs from a patch's sjsonl
// control line before applying it.
type uploadPatch struct {
	Base   string
	userID string
}

// patchControl reports whether body is an sjsonl patch and, if so,
// returns its base hash and the user id derived from its key. The key
// is unverified here: it only selects which user's latest document the
// patch must apply to. Applying the patch verifies the signature over
// the result, and ParseRatingset then checks the user line against it.
func patchControl(body []byte) (uploadPatch, bool, error) {
	first, _, _ := bytes.Cut(body, []byte("\n"))

	var ctl struct {
		SJSONL string `json:"sjsonl"`
		Key    string `json:"key"`
		Base   string `json:"base"`
	}
	if err := json.Unmarshal(first, &ctl); err != nil || ctl.SJSONL != "patch" {
		return uploadPatch{}, false, nil
	}

	key, err := base64.StdEncoding.DecodeString(ctl.Key)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return uploadPatch{}, true, errors.New("patch must be signed by a valid ed25519 key")
	}

	return uploadPatch{Base: ctl.Base, userID: userID(ed25519.PublicKey(key))}, true, nil
}
