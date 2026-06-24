package gitserver

import (
	"bufio"
	"errors"
	"io"
	"net/http"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v6/storage"
)

// Per-ref rejection reasons surfaced to the client in report-status. The
// gittuf/RSL-specific reasons live in receivepack_hooks.go.
var (
	errRefExists  = errors.New("reference already exists")
	errRefMissing = errors.New("reference does not exist")
	errRefInvalid = errors.New("invalid command")
)

// handleReceivePack serves git-receive-pack. We own the ref-commit step (Q2):
// go-git ingests the packfile, then we validate + apply the refs and append the
// RSL ourselves, emitting report-status only afterward (Q3, order-2) so the
// client's "ok" implies the RSL entry is durable. Coexistence (Q5/Q6): a client
// may push the RSL ref with its own entries; we validate-then-apply it and dedup
// our entries against it.
func (s *Server) handleReceivePack(w http.ResponseWriter, r *http.Request, repo string) {
	st, err := s.openStorer(repo, true)
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

	rd := bufio.NewReader(body)
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	// A leading flush means the client has nothing to send.
	if l, _, err := pktline.PeekLine(rd); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	} else if l == pktline.Flush {
		w.WriteHeader(http.StatusOK)
		return
	}

	updreq := &packp.UpdateRequests{}
	if err := updreq.Decode(rd); err != nil {
		http.Error(w, "malformed update request", http.StatusBadRequest)
		return
	}
	if updreq.Capabilities.Supports(capability.PushOptions) {
		var opts packp.PushOptions
		_ = opts.Decode(rd) // no hooks; decoded only to consume the stream
	}

	// Ingest the packfile (unlocked: objects are content-addressed/immutable).
	needPack := false
	for _, c := range updreq.Commands {
		if c.Action() != packp.Delete {
			needPack = true
			break
		}
	}
	if needPack {
		if err := packfile.UpdateObjectStorage(st, rd); err != nil {
			s.writeReport(w, updreq.Capabilities, err, nil)
			return
		}
	}

	// The pack is in place; hand the ref updates to the post-receive hook, which
	// serializes per repo and runs the validate/apply/record pipeline.
	cmdStatus := s.newRSLHooks().postReceive(r.Context(), st, repo, updreq.Commands)

	s.writeReport(w, updreq.Capabilities, nil, cmdStatus)
}

// writeReport encodes the receive-pack report-status, muxing it over sideband
// when the client negotiated it (mirroring go-git's transport.ReceivePack). It
// is written only after applyAndRecord returns, so "ok" implies refs + RSL are
// durable.
func (s *Server) writeReport(w http.ResponseWriter, caps capability.List, unpackErr error, cmdStatus map[plumbing.ReferenceName]error) {
	w.WriteHeader(http.StatusOK)
	if !caps.Supports(capability.ReportStatus) {
		return
	}

	var out io.Writer = w
	useSideband := false
	if !caps.Supports(capability.NoProgress) {
		switch {
		case caps.Supports(capability.Sideband64k):
			out = sideband.NewMuxer(sideband.Sideband64k, w)
			useSideband = true
		case caps.Supports(capability.Sideband):
			out = sideband.NewMuxer(sideband.Sideband, w)
			useSideband = true
		}
	}

	rs := &packp.ReportStatus{UnpackStatus: "ok"}
	if unpackErr != nil {
		rs.UnpackStatus = unpackErr.Error()
	}
	for ref, err := range cmdStatus {
		status := "ok"
		if err != nil {
			status = err.Error()
		}
		rs.CommandStatuses = append(rs.CommandStatuses, &packp.CommandStatus{
			ReferenceName: ref,
			Status:        status,
		})
	}
	if err := rs.Encode(out); err != nil {
		s.logger.Error("encode report-status failed", "err", err)
		return
	}
	if useSideband {
		_ = pktline.WriteFlush(w)
	}
}

func referenceExists(st storage.Storer, n plumbing.ReferenceName) (bool, error) {
	_, err := st.Reference(n)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return false, nil
	}
	return err == nil, err
}

func applyCommand(st storage.Storer, cmd *packp.Command) error {
	if cmd.Action() == packp.Delete {
		return st.RemoveReference(cmd.Name)
	}
	return st.SetReference(plumbing.NewHashReference(cmd.Name, cmd.New))
}
