package rsl

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// readTip returns the current RSL tip and its entry number, fail-closed (D6): a
// genuinely absent ref is (ZeroHash, 0); any other failure is an error.
func readTip(st Storer) (plumbing.Hash, uint64, error) {
	ref, err := st.Reference(plumbing.ReferenceName(Ref))
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return plumbing.ZeroHash, 0, nil
		}
		return plumbing.ZeroHash, 0, fmt.Errorf("rsl: read ref: %w", err)
	}
	n, err := EntryNumber(st, ref.Hash())
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("rsl: tip %s (fail-closed): %w", ref.Hash(), err)
	}
	return ref.Hash(), n, nil
}

// EntryNumber loads the commit at h and returns its parsed RSL entry number.
// It fails closed: a commit that cannot be read or carries no number line is an
// error, never treated as "first entry". Hosts use it to validate a client's
// proposed RSL tip before adopting it.
func EntryNumber(st Storer, h plumbing.Hash) (uint64, error) {
	c, err := loadCommit(st, h)
	if err != nil {
		return 0, err
	}
	n, ok, err := ParseNumber(c.Message)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("rsl: commit %s has no entry number", h)
	}
	return n, nil
}

// entryFromCommit builds the typed Entry for a commit by its id + message.
func entryFromCommit(id plumbing.Hash, c *object.Commit) (Entry, error) {
	e, err := parseEntry(c.Message)
	if err != nil {
		return nil, fmt.Errorf("rsl: parse entry %s: %w", id, err)
	}
	if e.kind == kindReference {
		th, ok := plumbing.FromHex(e.targetID)
		if !ok {
			return nil, fmt.Errorf("rsl: invalid targetID %q in %s", e.targetID, id)
		}
		return &ReferenceEntry{ID: id, RefName: e.refName, Target: th, Number: e.number}, nil
	}
	return &otherEntry{id: id, number: e.number}, nil
}

// GetLatestEntry returns the entry at the RSL tip.
func GetLatestEntry(st Storer) (Entry, error) {
	ref, err := st.Reference(plumbing.ReferenceName(Ref))
	if err != nil {
		return nil, fmt.Errorf("rsl: get reference: %w", err)
	}
	c, err := loadCommit(st, ref.Hash())
	if err != nil {
		return nil, err
	}
	return entryFromCommit(ref.Hash(), c)
}

// GetParentForEntry walks one step toward the root; ErrEntryNotFound at the root.
func GetParentForEntry(st Storer, entry Entry) (Entry, error) {
	c, err := loadCommit(st, entry.GetID())
	if err != nil {
		return nil, err
	}
	if len(c.ParentHashes) == 0 {
		return nil, ErrEntryNotFound
	}
	parent := c.ParentHashes[0]
	pc, err := loadCommit(st, parent)
	if err != nil {
		return nil, err
	}
	return entryFromCommit(parent, pc)
}

// GetLatestReferenceEntryForRef returns the most recent ReferenceEntry for
// refName, or ErrEntryNotFound if none exists.
func GetLatestReferenceEntryForRef(st Storer, refName string) (*ReferenceEntry, error) {
	e, err := GetLatestEntry(st)
	if err != nil {
		return nil, err
	}
	for {
		if re, ok := e.(*ReferenceEntry); ok && re.RefName == refName {
			return re, nil
		}
		e, err = GetParentForEntry(st, e)
		if err != nil {
			return nil, err
		}
	}
}
