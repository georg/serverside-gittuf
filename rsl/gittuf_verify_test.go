package rsl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/gittuf/gittuf/pkg/gitinterface"
	"github.com/secure-systems-lab/go-securesystemslib/signerverifier"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/signer"
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
	sgn, err := signer.NewSSHSigner(priv)
	require.NoError(t, err)
	raw, err := ssh.NewSignerFromKey(priv)
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

	gtRepo, err := gitinterface.LoadRepository(dir)
	require.NoError(t, err)
	key := sslibKeyFromSigner(raw)

	emptyTree, err := gtRepo.EmptyTree()
	require.NoError(t, err)
	tip, err := gtRepo.GetReference(rsl.Ref)
	require.NoError(t, err)

	type seenEntry struct {
		ref, target string
		number      uint64
	}
	var got []seenEntry
	for cur := tip; !cur.IsZero(); {
		require.NoError(t, gtRepo.VerifySignature(ctx, cur, key),
			"gittuf must accept the SSH signature on RSL commit %s", cur)

		treeID, err := gtRepo.GetCommitTreeID(cur)
		require.NoError(t, err)
		assert.Equal(t, emptyTree.String(), treeID.String(), "RSL entry must be an empty-tree commit")

		msg, err := gtRepo.GetCommitMessage(cur)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(msg, rsl.ReferenceEntryHeader), "message must be a ReferenceEntry: %q", msg)
		ref, target, number := parseCrossVerifyEntry(t, msg)
		got = append(got, seenEntry{ref: ref, target: target, number: number})

		parents, err := gtRepo.GetCommitParentIDs(cur)
		require.NoError(t, err)
		if len(parents) == 0 {
			break
		}
		require.Len(t, parents, 1, "the RSL is a linear chain")
		cur = parents[0]
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

// sslibKeyFromSigner builds the gittuf SSLibKey for an SSH public key, matching
// gittuf's internal newSSHKey: KeyType "ssh", Scheme = the key's type, and the
// base64-encoded SSH wire-format public key.
func sslibKeyFromSigner(s ssh.Signer) *signerverifier.SSLibKey {
	pub := s.PublicKey()
	return &signerverifier.SSLibKey{
		KeyID:   ssh.FingerprintSHA256(pub),
		KeyType: "ssh",
		Scheme:  pub.Type(),
		KeyVal:  signerverifier.KeyVal{Public: base64.StdEncoding.EncodeToString(pub.Marshal())},
	}
}
