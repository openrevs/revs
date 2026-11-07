package dmrs

import (
	"io"
	"net/http"
	"strings"

	"github.com/kalafut/dmrs/domain"
	"github.com/kalafut/dmrs/util"
)

// maxUploadSize bounds the size of an uploaded ratingset.
const maxUploadSize = 10 << 20

type uploadResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Rev    int    `json:"rev,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

// uploadPost handles document uploads synchronously: verify, check rev
// monotonicity, persist, and return a final verdict.
func (s *Server) uploadPost(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxUploadSize))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: err.Error()})
		return
	}

	user, doc, err := ParseRatingset(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: err.Error()})
		return
	}

	if user.Revision < 1 {
		writeJSON(w, http.StatusBadRequest, uploadResponse{Status: "invalid", Reason: "revision must be at least 1"})
		return
	}

	latest, err := s.dal.LatestDocument(req.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
		return
	}

	if latest != nil && user.Revision <= latest.Rev {
		writeJSON(w, http.StatusConflict, uploadResponse{
			Status: "stale",
			Reason: "revision is not greater than the latest accepted revision",
			Rev:    latest.Rev,
		})
		return
	}

	// Store the canonical encoding so the blob's bytes always match the
	// doc hash that keys them.
	canonical := doc.JSON()
	info := doc.Info()

	if err := s.docs.Put(info.Hash, canonical); err != nil {
		writeJSON(w, http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
		return
	}

	if err := s.dal.AddDocument(req.Context(), &domain.Document{
		Userid:       user.ID,
		Rev:          user.Revision,
		Hash:         info.Hash,
		Size:         len(canonical),
		ReceivedTime: util.Now(),
	}); err != nil {
		// A concurrent upload of the same rev loses the (userid, rev)
		// uniqueness race; report it as stale rather than a server error.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			writeJSON(w, http.StatusConflict, uploadResponse{Status: "stale", Reason: "revision already accepted"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, uploadResponse{Status: "error", Reason: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, uploadResponse{Status: "accepted", Rev: user.Revision, Hash: info.Hash})
}
