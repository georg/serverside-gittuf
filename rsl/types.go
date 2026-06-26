package rsl

import (
	"context"
	"errors"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// Storer is the subset of go-git v6's storage.Storer the RSL needs, in the
// Transactioner shape from the spec (§3 + batching): object writes go through a
// storer.Transaction — Begin → tx.SetEncodedObject × N → tx.Commit is one durable
// object flush — while durable reads, ref read, and the ref CAS stay on the
// storer. Any storer that implements storer.Transactioner satisfies it (go-git's
// in-memory storage does); a non-transactional storer such as the filesystem
// backend is adapted by txstore.New. Nothing here knows the RSL format or crypto.
type Storer interface {
	// object construction + durable reads
	NewEncodedObject() plumbing.EncodedObject
	EncodedObject(plumbing.ObjectType, plumbing.Hash) (plumbing.EncodedObject, error)
	// object writes are staged in a transaction and flushed on Commit
	Begin() storer.Transaction
	// refs
	Reference(plumbing.ReferenceName) (*plumbing.Reference, error)
	CheckAndSetReference(newRef, old *plumbing.Reference) error
}

// Signer signs the canonical, signature-free bytes of an RSL commit and returns
// the armored signature (SSHSIG, namespace "git", SHA-512). Key custody is the
// caller's. This is the one thing a Storer cannot carry, injected separately.
//
// The interface is compatible with ssh.Signer (go-git/x/plugin/objectsigner),
// so an ssh.Signer can be used wherever a Signer is expected.
type Signer interface {
	Sign(ctx context.Context, message io.Reader) ([]byte, error)
}

// RefChange is one ref mutation to record. Target is the new tip; for a deletion
// set Delete (Target is then ignored and a zero-width targetID is recorded).
type RefChange struct {
	RefName string
	Target  plumbing.Hash
	Delete  bool
}

// Entry is any RSL entry.
type Entry interface {
	GetID() plumbing.Hash
	GetNumber() uint64
}

// ReferenceEntry is a recorded reference entry; ID is its commit hash.
type ReferenceEntry struct {
	ID      plumbing.Hash
	RefName string
	Target  plumbing.Hash
	Number  uint64
}

func (e *ReferenceEntry) GetID() plumbing.Hash { return e.ID }
func (e *ReferenceEntry) GetNumber() uint64    { return e.Number }

// otherEntry is any non-reference entry (annotation/propagation) — the reader
// surfaces its ID and number for chain walking, nothing more.
type otherEntry struct {
	id     plumbing.Hash
	number uint64
}

func (e *otherEntry) GetID() plumbing.Hash { return e.id }
func (e *otherEntry) GetNumber() uint64    { return e.number }

var (
	// ErrRefConflict is returned by AppendReferenceEntries when the RSL ref no
	// longer matches the expected old tip (a concurrent writer advanced it).
	// It maps go-git's storage.ErrReferenceHasChanged.
	ErrRefConflict = errors.New("rsl: reference moved under append")

	// ErrEntryNotFound is returned by GetParentForEntry at the root of the chain.
	ErrEntryNotFound = errors.New("rsl: entry not found")
)
