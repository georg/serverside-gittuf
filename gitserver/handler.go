package gitserver

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/transport"
)

// Handler returns the http.Handler serving the git smart-HTTP endpoints:
//
//	GET  /{repo}/info/refs?service=git-upload-pack|git-receive-pack
//	POST /{repo}/git-upload-pack
//	POST /{repo}/git-receive-pack
//
// {repo} may contain slashes (e.g. org/name). Wrap the returned handler with
// TLS/auth/a reverse proxy as needed — the RSL logic is independent of it.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/info/refs"):
		s.handleInfoRefs(w, r, strings.TrimSuffix(path, "/info/refs"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/"+transport.UploadPackService):
		s.handleUploadPack(w, r, strings.TrimSuffix(path, "/"+transport.UploadPackService))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/"+transport.ReceivePackService):
		s.handleReceivePack(w, r, strings.TrimSuffix(path, "/"+transport.ReceivePackService))
	default:
		http.NotFound(w, r)
	}
}

// httpError maps an internal error to an HTTP status. Repo-not-found and
// invalid-name are client errors; everything else is 500.
func (s *Server) httpError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errRepoNotFound):
		http.Error(w, "repository not found", http.StatusNotFound)
	case errors.Is(err, errInvalidRepo):
		http.Error(w, "invalid repository name", http.StatusBadRequest)
	default:
		s.logger.Error("git request failed", "path", r.URL.Path, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// requestBody returns the request body, transparently gunzipped when the client
// set Content-Encoding: gzip (git does this for larger requests).
func requestBody(r *http.Request) (io.ReadCloser, error) {
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		return zr, nil
	}
	return r.Body, nil
}
