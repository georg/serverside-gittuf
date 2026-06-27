package gitserver

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage"

	"github.com/georg/serverside-gittuf/rsl"
)

// maxClientDedupScan bounds how many client-pushed RSL commits dropClientLogged
// walks. A co-managing client controls how many entries it adds per push, so
// without a cap one push could force an unbounded walk under the per-repo lock.
// Exceeding it fails the push closed rather than silently skipping dedup.
const maxClientDedupScan = 10000

// dropClientLogged removes changes a co-managing client already recorded in this
// push: it walks the RSL from the current tip back to boundary (the client's
// pre-push tip, exclusive) or the root, reading each commit exactly once and
// collecting each ReferenceEntry's (ref, target), then filters changes against
// that set. Ported from entiredb/rslstore/writer.go: coexistence is host policy
// (the spec), so it lives here, not in the rsl package.
func dropClientLogged(st storage.Storer, changes []rsl.RefChange, boundary plumbing.Hash) ([]rsl.RefChange, error) {
	ref, err := st.Reference(plumbing.ReferenceName(rsl.Ref))
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return changes, nil // no RSL yet — nothing client-logged
		}
		return nil, fmt.Errorf("read rsl ref: %w", err)
	}

	seen := map[string]struct{}{}
	for cur, walked := ref.Hash(), 0; !cur.IsZero(); walked++ {
		if !boundary.IsZero() && cur.Equal(boundary) {
			break // reached the client's pre-push tip (exclusive)
		}
		if walked >= maxClientDedupScan {
			return nil, fmt.Errorf("client rsl dedup scan exceeded %d entries (fail-closed)", maxClientDedupScan)
		}
		c, err := object.GetCommit(st, cur)
		if err != nil {
			return nil, fmt.Errorf("walk rsl commit %s: %w", cur, err)
		}
		refName, target, ok, err := rsl.ParseReferenceEntry(c.Message)
		if err != nil {
			return nil, fmt.Errorf("parse rsl commit %s (fail-closed): %w", cur, err)
		}
		if ok {
			seen[rsl.DedupKey(refName, target)] = struct{}{}
		}
		if len(c.ParentHashes) == 0 {
			break // root
		}
		cur = c.ParentHashes[0]
	}

	out := make([]rsl.RefChange, 0, len(changes))
	for _, ch := range changes {
		if _, dup := seen[rsl.ChangeKey(ch, ref.Hash())]; dup {
			continue
		}
		out = append(out, ch)
	}
	return out, nil
}
