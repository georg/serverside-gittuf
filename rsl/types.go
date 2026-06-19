package rsl

import (
	"context"
	"errors"

	"github.com/go-git/go-git/v6/plumbing"
)

// Storer is the subset of go-git v6's storage.Storer the RSL needs. A
// *git.Repository's Storer satisfies it; so does any host storer
// (go-git's filesystem/memory storage, entiredb's content-addressed storer, …).
//
// This is the seam from the gittuf-storer-interface-spec: object read/write +
// ref read/CAS, nothing about the RSL format or crypto.
type Storer interface {
	// objects
	NewEncodedObject() plumbing.EncodedObject
	SetEncodedObject(plumbing.EncodedObject) (plumbing.Hash, error)
	EncodedObject(plumbing.ObjectType, plumbing.Hash) (plumbing.EncodedObject, error)
	// refs
	Reference(plumbing.ReferenceName) (*plumbing.Reference, error)
	CheckAndSetReference(newRef, old *plumbing.Reference) error
}

// Signer signs the canonical, signature-free bytes of an RSL commit and returns
// the armored signature (SSHSIG, namespace "git", SHA-512). Key custody is the
// caller's. This is the one thing a Storer cannot carry, injected separately.
type Signer interface {
	Sign(ctx context.Context, payload []byte) ([]byte, error)
	KeyID() string
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
