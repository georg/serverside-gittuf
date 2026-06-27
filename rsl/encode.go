package rsl

import (
	"context"
	"fmt"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// The RSL committer identity. Cosmetic only: gittuf verifies the SSH signature
// against the authorized principal, not the author/committer string.
const (
	commitAuthorName  = "serverside-gittuf"
	commitAuthorEmail = "rsl@serverside-gittuf.local"
)

// buildSignedCommit builds an empty-tree commit (parent omitted when zero),
// SSH-signs its signature-free payload into the gpgsig header exactly as gittuf
// expects, stages it in the transaction, and returns the new commit hash.
//
// It sets commit.Signature (-> gpgsig) — in go-git v6 the field is Signature,
// not PGPSignature — and never SignatureSHA256 (-> gpgsig-sha256), which gittuf
// verification ignores; true even for sha256 repositories.
func buildSignedCommit(ctx context.Context, st Storer, tx storer.Transaction, signer Signer, emptyTree, parent plumbing.Hash, message string, when time.Time) (plumbing.Hash, error) {
	ident := object.Signature{Name: commitAuthorName, Email: commitAuthorEmail, When: when}
	c := &object.Commit{
		Author:    ident,
		Committer: ident,
		Message:   message,
		TreeHash:  emptyTree,
	}
	if !parent.IsZero() {
		c.ParentHashes = []plumbing.Hash{parent}
	}

	// Derive the canonical signature-free payload via go-git's codec, sign it,
	// and place the armored SSHSIG in the gpgsig header.
	payload, err := c.EncodeWithoutSignature()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("rsl: encode payload: %w", err)
	}

	sig, err := signer.Sign(ctx, payload)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("rsl: sign commit: %w", err)
	}
	c.Signature = sig

	obj := st.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("rsl: encode commit: %w", err)
	}
	h, err := tx.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("rsl: stage commit: %w", err)
	}
	return h, nil
}

// ensureEmptyTree stages the repo's empty tree (idempotent, content-addressed)
// and returns its hash at the storer's configured object format.
func ensureEmptyTree(st Storer, tx storer.Transaction) (plumbing.Hash, error) {
	obj := st.NewEncodedObject()
	if err := (&object.Tree{}).Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("rsl: encode empty tree: %w", err)
	}
	h, err := tx.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("rsl: stage empty tree: %w", err)
	}
	return h, nil
}

// loadCommit reads and decodes the commit object at h.
func loadCommit(st Storer, h plumbing.Hash) (*object.Commit, error) {
	obj, err := st.EncodedObject(plumbing.CommitObject, h)
	if err != nil {
		return nil, fmt.Errorf("rsl: read object %s: %w", h, err)
	}
	c := &object.Commit{}
	if err := c.Decode(obj); err != nil {
		return nil, fmt.Errorf("rsl: decode commit %s: %w", h, err)
	}
	return c, nil
}
