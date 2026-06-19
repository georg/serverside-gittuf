package rsl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage"
)

// AppendReferenceEntries appends one ReferenceEntry per change to the RSL and
// returns the entries it wrote, in order, each with its assigned number.
//
// It does NOT dedup: gittuf's model is single-writer, so it records exactly the
// changes it is given. A host that lets clients co-manage the RSL applies its
// own coexistence policy (drop entries the client already logged) BEFORE calling
// (see gitserver.dropClientLogged).
//
// Shape: the spec's advance-once-per-push over a storer.Transaction. It reads the
// tip once (durably, before the transaction), stages every new commit in the
// transaction chaining on the in-memory hash of the previous one (no per-entry
// tip re-read), flushes them in a SINGLE tx.Commit (the object-flush boundary),
// then advances the ref with a SINGLE CheckAndSetReference outside the
// transaction. Under the host's per-repo lock, the lone CAS cannot conflict.
//
// Fail-closed tip read (D6): a genuinely absent ref starts the chain at number 1
// with no parent; any other read error, or a tip whose number can't be parsed,
// fails the append rather than restarting the chain at 1.
func AppendReferenceEntries(ctx context.Context, st Storer, signer Signer, now func() time.Time, changes []RefChange) ([]*ReferenceEntry, error) {
	if len(changes) == 0 {
		return nil, nil
	}

	tip, number, err := readTip(st)
	if err != nil {
		return nil, err
	}

	tx := st.Begin()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	emptyTree, err := ensureEmptyTree(st, tx)
	if err != nil {
		return nil, fmt.Errorf("rsl: empty tree: %w", err)
	}

	running := tip
	out := make([]*ReferenceEntry, 0, len(changes))
	for _, ch := range changes {
		number++
		// The empty-tree hash carries the repo's hash width, which sizes a
		// deletion's zero-width targetID.
		id, err := buildSignedCommit(ctx, st, tx, signer, emptyTree, running,
			referenceEntryMessage(ch.RefName, changeTargetID(ch, emptyTree), number), now())
		if err != nil {
			return nil, err
		}
		running = id
		out = append(out, &ReferenceEntry{ID: id, RefName: ch.RefName, Target: ch.Target, Number: number})
	}

	// One durable flush of all staged objects, then advance the ref. The ref tip
	// and its CAS sit outside the object transaction (spec: object-only).
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("rsl: commit objects: %w", err)
	}
	committed = true

	newRef := plumbing.NewHashReference(plumbing.ReferenceName(Ref), running)
	var old *plumbing.Reference
	if !tip.IsZero() {
		old = plumbing.NewHashReference(plumbing.ReferenceName(Ref), tip)
	}
	if err := st.CheckAndSetReference(newRef, old); err != nil {
		if errors.Is(err, storage.ErrReferenceHasChanged) {
			return nil, ErrRefConflict
		}
		return nil, fmt.Errorf("rsl: advance ref: %w", err)
	}
	return out, nil
}
