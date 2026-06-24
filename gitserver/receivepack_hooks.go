package gitserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/txstore"
)

// This file implements serverside-gittuf's server-side hooks for git-receive-pack.
// They follow git's hook naming so the transport and generic ref-application code
// in receivepack.go stays free of host (gittuf/RSL) policy:
//
//   - pre-receive validates the proposed ref updates before any of them is
//     applied (e.g. a client co-pushing the RSL ref).
//   - reference-transaction records the applied refs in the RSL once the ref
//     updates have committed.

// Per-ref rejection reasons specific to the RSL ref, surfaced to the client in
// report-status.
var (
	errRSLStale     = errors.New("rsl ref out of date (fetch refs/gittuf/* first)")
	errRSLProtected = errors.New("rsl ref cannot be deleted")
	errRSLMalformed = errors.New("pushed rsl tip is not a valid numbered RSL entry")
)

// rslHooks holds the gittuf/RSL receive-pack hooks for a single push.
// clientPreTip, set by preReceive when a client co-pushes the RSL ref, is the
// dedup boundary used by referenceTransaction.
type rslHooks struct {
	signer rsl.Signer
	now    func() time.Time
	logger *slog.Logger
	locks  *keyedMutex

	clientPreTip *plumbing.Hash
}

func (s *Server) newRSLHooks() *rslHooks {
	return &rslHooks{signer: s.signer, now: s.now, logger: s.logger, locks: s.locks}
}

// postReceive is the receive-pack entry point the transport calls once the
// packfile is ingested (named after git's post-receive hook). It serializes per
// repo — the critical section the RSL append requires — and runs the
// validate/apply/record pipeline. The packfile ingest is deliberately left
// unlocked by the caller, since objects are content-addressed and immutable.
func (h *rslHooks) postReceive(ctx context.Context, st txstore.Storer, repo string, commands []*packp.Command) map[plumbing.ReferenceName]error {
	mu := h.locks.get(repo)
	mu.Lock()
	defer mu.Unlock()
	return h.applyAndRecord(ctx, st, commands)
}

// applyAndRecord runs the receive-pack hooks around the generic ref application
// (referenceExists/applyCommand live in receivepack.go): pre-receive validates
// the commands, the surviving ones are applied, then reference-transaction
// records the applied refs. It returns per-ref status for report-status. Order
// is refs-first then RSL (Q3): a reference-transaction failure marks the
// recorded refs failed (the ref is already applied; a client retry re-applies
// the no-op and writes the entry).
func (h *rslHooks) applyAndRecord(ctx context.Context, st txstore.Storer, commands []*packp.Command) map[plumbing.ReferenceName]error {
	cmdStatus := make(map[plumbing.ReferenceName]error, len(commands))

	// pre-receive: validate the commands before any ref is applied.
	rejections := h.preReceive(st, commands)

	applied := make([]*packp.Command, 0, len(commands))
	for _, cmd := range commands {
		if err := rejections[cmd.Name]; err != nil {
			cmdStatus[cmd.Name] = err
			continue
		}
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

		if err := applyCommand(st, cmd); err != nil {
			cmdStatus[cmd.Name] = err
			continue
		}
		cmdStatus[cmd.Name] = nil
		applied = append(applied, cmd)
	}

	// reference-transaction (committed): record the applied refs.
	maps.Copy(cmdStatus, h.referenceTransaction(ctx, st, applied))
	return cmdStatus
}

// preReceive runs before any ref is applied (git's pre-receive hook). It rejects
// a client-pushed RSL ref that would corrupt the log and records its pre-push
// tip as the dedup boundary for referenceTransaction. The returned map holds a
// rejection per refused ref; refs absent from it are cleared to apply.
func (h *rslHooks) preReceive(st txstore.Storer, commands []*packp.Command) map[plumbing.ReferenceName]error {
	var rejections map[plumbing.ReferenceName]error
	for _, cmd := range commands {
		if cmd.Name.String() != rsl.Ref {
			continue
		}
		// Coexistence: the client pushed the RSL ref itself. Validate before it
		// is applied (no cross-store rollback), then capture its pre-push tip as
		// the dedup boundary. ShouldRecord(rsl.Ref) is false, so we never record it.
		if err := validateClientRSL(st, cmd); err != nil {
			if rejections == nil {
				rejections = make(map[plumbing.ReferenceName]error)
			}
			rejections[cmd.Name] = err
			continue
		}
		h.clientPreTip = &cmd.Old
	}
	return rejections
}

// referenceTransaction runs after the surviving ref updates have committed (the
// "committed" state of git's reference-transaction hook). It records the applied
// non-gittuf refs in the RSL, deduping against any entries a client logged in
// the same push. On dedup or append failure it returns a per-ref error for every
// ref it tried to record: the refs are already applied, so surfacing the failure
// lets the client retry (the no-op re-apply then writes the entry).
func (h *rslHooks) referenceTransaction(ctx context.Context, st txstore.Storer, applied []*packp.Command) map[plumbing.ReferenceName]error {
	var changes []rsl.RefChange
	for _, cmd := range applied {
		if rsl.ShouldRecord(cmd.Name.String()) {
			changes = append(changes, refChangeFrom(cmd))
		}
	}
	if len(changes) == 0 {
		return nil
	}

	survivors := changes
	if h.clientPreTip != nil {
		var err error
		survivors, err = dropClientLogged(st, changes, *h.clientPreTip)
		if err != nil {
			return failures(changes, fmt.Errorf("rsl dedup: %w", err))
		}
	}

	if _, err := rsl.AppendReferenceEntries(ctx, st, h.signer, h.now, survivors); err != nil {
		h.logger.Error("rsl append failed after refs applied", "err", err)
		return failures(changes, fmt.Errorf("rsl record failed: %w", err))
	}
	return nil
}

// failures builds a per-ref status map marking every recorded ref failed with
// err.
func failures(changes []rsl.RefChange, err error) map[plumbing.ReferenceName]error {
	out := make(map[plumbing.ReferenceName]error, len(changes))
	for _, ch := range changes {
		out[plumbing.ReferenceName(ch.RefName)] = err
	}
	return out
}

// validateClientRSL guards a client's push of the RSL ref against corruption,
// since we have no cross-store rollback: the command's old must match the
// current RSL tip (CAS precheck), it must not be a deletion, and the proposed
// new tip — whose objects are already ingested — must parse as a numbered RSL
// entry. On any failure pre-receive rejects just this command.
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

func refChangeFrom(cmd *packp.Command) rsl.RefChange {
	if cmd.Action() == packp.Delete {
		return rsl.RefChange{RefName: cmd.Name.String(), Delete: true}
	}
	return rsl.RefChange{RefName: cmd.Name.String(), Target: cmd.New}
}
