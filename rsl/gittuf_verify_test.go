package rsl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	objectsigner "github.com/go-git/x/plugin/objectsigner/ssh"
	objectverifier "github.com/go-git/x/plugin/objectverifier/ssh"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/txstore"
)

// TestGittufCrossVerify_SHA1 writes an RSL with our Storer-model writer to an
// on-disk sha1 repo, then opens that same repo with gittuf's own pkg/gitinterface
// (go-git v5) and verifies, entry by entry, the SSH signatures we produced and
// the 1..N number chain — gittuf's real verification code, not a reimplementation,
// validating our output across the go-git v6 -> v5 boundary.
//
// sha1 only: gittuf's go-git v5 fixes its hash algorithm at compile time and
// cannot open sha256 objects. The byte-identical signing path is shared across
// both widths, so sha256 compatibility follows from the unit + E2E coverage.
func TestGittufCrossVerify_SHA1(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available; gittuf cross-verify needs the git CLI")
	}

	// On-disk, non-bare sha1 repo: gittuf opens via DetectDotGit. The filesystem
	// storer is adapted to the transactional Storer the append consumes.
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	st := txstore.New(repo.Storer)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	raw, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	sgn, err := objectsigner.FromKey(raw)
	require.NoError(t, err)
	vfr, err := objectverifier.FromKey(raw.PublicKey())
	require.NoError(t, err)

	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }

	mainA := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	mainB := plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	// Three pushes -> a 3-entry chain (1,2,3): create, update, delete.
	_, err = rsl.AppendReferenceEntries(ctx, st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: mainA}})
	require.NoError(t, err)
	_, err = rsl.AppendReferenceEntries(ctx, st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: mainB}})
	require.NoError(t, err)
	_, err = rsl.AppendReferenceEntries(ctx, st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/feature", Delete: true}})
	require.NoError(t, err)

	emptyTree := "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // TODO: go-git should probably expose this and the same for SHA256.
	ref := plumbing.ReferenceName(rsl.Ref)
	require.NoError(t, ref.Validate())

	tip, err := repo.Reference(ref, true)
	require.NoError(t, err)

	type seenEntry struct {
		ref, target string
		number      uint64
	}
	var got []seenEntry
	for cur := tip.Hash(); !cur.IsZero(); {
		commit, err := repo.CommitObject(cur)
		require.NoError(t, err, "commit not found")

		_, err = commit.Verify(context.Background(), object.WithVerifier(vfr))
		require.NoError(t, err, "the SSH signature on RSL commit %s must verify", cur)

		assert.Equal(t, emptyTree, commit.TreeHash.String(), "RSL entry must be an empty-tree commit")

		require.True(t, strings.HasPrefix(commit.Message, rsl.ReferenceEntryHeader), "message must be a ReferenceEntry: %q", commit.Message)
		ref, target, number := parseCrossVerifyEntry(t, commit.Message)
		got = append(got, seenEntry{ref: ref, target: target, number: number})

		if len(commit.ParentHashes) == 0 {
			break // root entry (number 1) has no parent
		}
		require.Len(t, commit.ParentHashes, 1, "the RSL is a linear chain")
		cur = commit.ParentHashes[0]
	}

	// Newest first: 3=delete feature, 2=update main->b, 1=create main->a.
	require.Len(t, got, 3)
	zero := strings.Repeat("0", 40)
	assert.Equal(t, seenEntry{ref: "refs/heads/feature", target: zero, number: 3}, got[0])
	assert.Equal(t, seenEntry{ref: "refs/heads/main", target: mainB.String(), number: 2}, got[1])
	assert.Equal(t, seenEntry{ref: "refs/heads/main", target: mainA.String(), number: 1}, got[2])
}

// parseCrossVerifyEntry pulls ref/targetID/number out of an RSL ReferenceEntry
// message as it appears on the wire (gittuf-read), tolerant of trailing newline.
func parseCrossVerifyEntry(t *testing.T, message string) (ref, target string, number uint64) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ref":
			ref = strings.TrimSpace(val)
		case "targetID":
			target = strings.TrimSpace(val)
		case "number":
			_, err := fmt.Sscanf(strings.TrimSpace(val), "%d", &number)
			require.NoError(t, err)
		}
	}
	require.NotEmpty(t, ref)
	require.NotEmpty(t, target)
	return ref, target, number
}
