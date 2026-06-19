package rsl

import (
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
)

// DedupKey is the identity of a recorded ref state: a ReferenceEntry and a
// RefChange map to the same key iff they record the same ref at the same
// target. A host that lets clients co-manage the RSL uses it to skip entries the
// client already logged.
func DedupKey(refName, targetID string) string { return refName + "\x00" + targetID }

// ChangeKey is the DedupKey of a RefChange. widthHash supplies the repo's hash
// width for a deletion's zero-width target — pass any real hash of the repo's
// object format (e.g. the RSL tip or the empty tree).
func ChangeKey(ch RefChange, widthHash plumbing.Hash) string {
	return DedupKey(ch.RefName, changeTargetID(ch, widthHash))
}

// changeTargetID renders the targetID a RefChange records in its entry message:
// the new tip's hex, or a zero-width target (sized to widthHash) for a deletion.
func changeTargetID(ch RefChange, widthHash plumbing.Hash) string {
	if ch.Delete {
		return strings.Repeat("0", len(widthHash.String()))
	}
	return ch.Target.String()
}
