package gitserver

import (
	"net/http"

	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/utils/ioutil"
)

// handleUploadPack serves git-upload-pack (fetch/clone). The RSL ref is a normal
// ref, so it is advertised and served like any other — this is the "pull the RSL
// ref to verify" path. No lock: object reads are immutable and ref reads are
// per-file atomic. We don't auto-init: fetching a missing repo is a 404.
func (s *Server) handleUploadPack(w http.ResponseWriter, r *http.Request, repo string) {
	st, err := s.openStorer(repo, false)
	if err != nil {
		s.httpError(w, r, err)
		return
	}
	body, err := requestBody(r)
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	if err := transport.UploadPack(r.Context(), st, body, ioutil.WriteNopCloser(w), &transport.UploadPackRequest{
		StatelessRPC: true,
	}); err != nil {
		// Headers/body may already be partially written; just log.
		s.logger.Error("upload-pack failed", "repo", repo, "err", err)
	}
}
