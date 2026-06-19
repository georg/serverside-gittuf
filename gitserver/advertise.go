package gitserver

import (
	"fmt"
	"net/http"

	"github.com/go-git/go-git/v6/plumbing/transport"
)

// handleInfoRefs serves the smart-HTTP reference-discovery phase for both
// services. We force protocol v0/v1 (ignore the Git-Protocol header) so go-git's
// fully-implemented advertisement path is used; clients fall back automatically.
func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, repo string) {
	service := r.URL.Query().Get("service")
	if service != transport.UploadPackService && service != transport.ReceivePackService {
		http.Error(w, "dumb http protocol is not supported", http.StatusForbidden)
		return
	}

	// Auto-init only on the push advertisement; a fetch/ls-remote of a missing
	// repo is a 404 rather than silently creating an empty repo.
	create := service == transport.ReceivePackService
	st, err := s.openStorer(repo, create)
	if err != nil {
		s.httpError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// AdvertiseRefs with smart=true emits the full smart-HTTP advertisement,
	// including the "# service=<svc>" banner pkt-line and trailing flush.
	if err := transport.AdvertiseRefs(r.Context(), st, w, service, true); err != nil {
		s.logger.Error("advertise refs failed", "repo", repo, "service", service, "err", err)
	}
}
