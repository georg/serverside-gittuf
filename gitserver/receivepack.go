package gitserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v6/storage"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/txstore"
)

// Per-ref rejection reasons surfaced to the client in report-status.
var (
	errRefExists    = errors.New("reference already exists")
	errRefMissing   = errors.New("reference does not exist")
	errRefInvalid   = errors.New("invalid command")
	errRSLStale     = errors.New("rsl ref out of date (fetch refs/gittuf/* first)")
	errRSLProtected = errors.New("rsl ref cannot be deleted")
	errRSLMalformed = errors.New("pushed rsl tip is not a valid numbered RSL entry")
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

	// Critical section: validate + apply refs + append RSL, serialized per repo.
	mu := s.locks.get(repo)
	mu.Lock()
	cmdStatus := s.applyAndRecord(r.Context(), st, updreq.Commands)
	mu.Unlock()

	s.writeReport(w, updreq.Capabilities, nil, cmdStatus)
}

// applyAndRecord validates each command, applies the surviving ref updates, and
// appends RSL entries for the applied non-gittuf refs the client did not already
// log. It returns per-ref status for report-status. Order is refs-first then RSL
// (Q3): an RSL-append failure marks the recorded refs failed (the ref is already
// applied; a client retry re-applies the no-op and writes the entry).
func (s *Server) applyAndRecord(ctx context.Context, st txstore.Storer, commands []*packp.Command) map[plumbing.ReferenceName]error {
	cmdStatus := make(map[plumbing.ReferenceName]error, len(commands))
	var changes []rsl.RefChange
	var clientPreTip *plumbing.Hash

	for _, cmd := range commands {
		exists, err := referenceExists(st, cmd.Name)
		if err != nil {
			cmdStatus[cmd.Name] = err
			continue
		}
		switch cmd.Action() {
		case packp.Create:
			if exists {
				cmdStatus[cmd.Name] = errRefExists
				continue
			}
		case packp.Update, packp.Delete:
			if !exists {
				cmdStatus[cmd.Name] = errRefMissing
				continue
			}
		default:
			cmdStatus[cmd.Name] = errRefInvalid
			continue
		}

		// Coexistence: the client pushed the RSL ref itself. Validate before
		// applying (no rollback available), then capture its pre-push tip as the
		// dedup boundary. ShouldRecord(rsl.Ref) is false, so it is never recorded.
		if cmd.Name.String() == rsl.Ref {
			if err := validateClientRSL(st, cmd); err != nil {
				cmdStatus[cmd.Name] = err
				continue
			}
			if err := applyCommand(st, cmd); err != nil {
				cmdStatus[cmd.Name] = err
				continue
			}
			clientPreTip = &cmd.Old
			cmdStatus[cmd.Name] = nil
			continue
		}

		if err := applyCommand(st, cmd); err != nil {
			cmdStatus[cmd.Name] = err
			continue
		}
		cmdStatus[cmd.Name] = nil
		if rsl.ShouldRecord(cmd.Name.String()) {
			changes = append(changes, refChangeFrom(cmd))
		}
	}

	if len(changes) == 0 {
		return cmdStatus
	}

	survivors := changes
	if clientPreTip != nil {
		var err error
		survivors, err = dropClientLogged(st, changes, *clientPreTip)
		if err != nil {
			s.failChanges(cmdStatus, changes, fmt.Errorf("rsl dedup: %w", err))
			return cmdStatus
		}
	}

	if _, err := rsl.AppendReferenceEntries(ctx, st, s.signer, s.now, survivors); err != nil {
		// Refs already applied; surface the failure so the client retries (heals).
		s.logger.Error("rsl append failed after refs applied", "err", err)
		s.failChanges(cmdStatus, changes, fmt.Errorf("rsl record failed: %w", err))
	}
	return cmdStatus
}

// failChanges marks every recorded ref's status as err (used when the RSL append
// or dedup fails after the refs were applied).
func (s *Server) failChanges(cmdStatus map[plumbing.ReferenceName]error, changes []rsl.RefChange, err error) {
	for _, ch := range changes {
		cmdStatus[plumbing.ReferenceName(ch.RefName)] = err
	}
}

// validateClientRSL guards a client's push of the RSL ref against corruption,
// since we have no cross-store rollback: the command's old must match the
// current RSL tip (CAS precheck), it must not be a deletion, and the proposed
// new tip — whose objects are already ingested — must parse as a numbered RSL
// entry. On any failure the caller rejects just this command and records normally.
func validateClientRSL(st rsl.Storer, cmd *packp.Command) error {
	current := plumbing.ZeroHash
	if ref, err := st.Reference(plumbing.ReferenceName(rsl.Ref)); err == nil {
		current = ref.Hash()
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return err
	}
	if cmd.Old != current {
		return errRSLStale
	}
	if cmd.Action() == packp.Delete {
		return errRSLProtected
	}
	if _, err := rsl.EntryNumber(st, cmd.New); err != nil {
		return errRSLMalformed
	}
	return nil
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

func refChangeFrom(cmd *packp.Command) rsl.RefChange {
	if cmd.Action() == packp.Delete {
		return rsl.RefChange{RefName: cmd.Name.String(), Delete: true}
	}
	return rsl.RefChange{RefName: cmd.Name.String(), Target: cmd.New}
}
