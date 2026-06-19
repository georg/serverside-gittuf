package rsl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/signer"
)

func testSigner(t *testing.T) (rsl.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sgn, err := signer.NewSSHSigner(priv)
	require.NoError(t, err)
	s, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return sgn, s.PublicKey()
}

func fixedClock() func() time.Time {
	when := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return when }
}

func TestAppend_NumberingChainingMessageAndSignature(t *testing.T) {
	st := memory.NewStorage()
	sgn, pub := testSigner(t)
	ctx := context.Background()
	now := fixedClock()

	mainA := plumbing.NewHash("1111111111111111111111111111111111111111")
	mainB := plumbing.NewHash("2222222222222222222222222222222222222222")

	e1, err := rsl.AppendReferenceEntries(ctx, st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: mainA}})
	require.NoError(t, err)
	require.Len(t, e1, 1)
	assert.Equal(t, uint64(1), e1[0].Number)

	e2, err := rsl.AppendReferenceEntries(ctx, st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: mainB}})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), e2[0].Number)

	e3, err := rsl.AppendReferenceEntries(ctx, st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/feature", Delete: true}})
	require.NoError(t, err)
	assert.Equal(t, uint64(3), e3[0].Number)

	// The tip is the third entry.
	tip, err := rsl.GetLatestEntry(st)
	require.NoError(t, err)
	assert.Equal(t, e3[0].ID, tip.GetID())

	// Each entry's signature verifies against the signer's public key.
	for _, e := range []*rsl.ReferenceEntry{e1[0], e2[0], e3[0]} {
		require.NoError(t, rsl.VerifyEntrySignature(st, e.ID, pub), "entry %d", e.Number)
	}

	// Exact on-wire message bytes for entry 1 (create main -> A).
	c1, err := object.GetCommit(st, e1[0].ID)
	require.NoError(t, err)
	want1 := fmt.Sprintf("RSL Reference Entry\n\nref: %s\ntargetID: %s\nnumber: %d",
		"refs/heads/main", mainA.String(), 1)
	assert.Equal(t, want1, c1.Message)
	assert.NotEmpty(t, c1.Signature) // go-git v6 stores the gpgsig in Signature

	// A deletion records the zero-width target (40 zeros for sha1).
	c3, err := object.GetCommit(st, e3[0].ID)
	require.NoError(t, err)
	assert.Contains(t, c3.Message, "targetID: "+strings.Repeat("0", 40))

	// Linear parent chain: 3 -> 2 -> 1 -> root.
	require.Equal(t, []plumbing.Hash{e2[0].ID}, c3.ParentHashes)
	c2, err := object.GetCommit(st, e2[0].ID)
	require.NoError(t, err)
	require.Equal(t, []plumbing.Hash{e1[0].ID}, c2.ParentHashes)
	require.Empty(t, c1.ParentHashes)

	// Every entry is an empty-tree commit.
	assert.Equal(t, c1.TreeHash, c3.TreeHash)
}

func TestAppend_AdvanceOncePerPush(t *testing.T) {
	st := memory.NewStorage()
	sgn, _ := testSigner(t)
	ctx := context.Background()

	a := plumbing.NewHash("1111111111111111111111111111111111111111")
	b := plumbing.NewHash("2222222222222222222222222222222222222222")

	// One push with two ref changes => entries 1 and 2, chained, single tip.
	entries, err := rsl.AppendReferenceEntries(ctx, st, sgn, fixedClock(), []rsl.RefChange{
		{RefName: "refs/heads/main", Target: a},
		{RefName: "refs/heads/feature", Target: b},
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, uint64(1), entries[0].Number)
	assert.Equal(t, uint64(2), entries[1].Number)

	tip, err := rsl.GetLatestEntry(st)
	require.NoError(t, err)
	assert.Equal(t, entries[1].ID, tip.GetID())

	c2, err := object.GetCommit(st, entries[1].ID)
	require.NoError(t, err)
	assert.Equal(t, []plumbing.Hash{entries[0].ID}, c2.ParentHashes)
}

func TestAppend_FailsClosedOnUnnumberedTip(t *testing.T) {
	st := memory.NewStorage()
	sgn, _ := testSigner(t)
	ctx := context.Background()
	ident := object.Signature{Name: "x", Email: "x@x", When: fixedClock()()}

	// Park the RSL ref on a commit with no "number:" line.
	to := st.NewEncodedObject()
	require.NoError(t, (&object.Tree{}).Encode(to))
	tree, err := st.SetEncodedObject(to)
	require.NoError(t, err)
	c := &object.Commit{Author: ident, Committer: ident, Message: "not an rsl entry", TreeHash: tree}
	co := st.NewEncodedObject()
	require.NoError(t, c.Encode(co))
	h, err := st.SetEncodedObject(co)
	require.NoError(t, err)
	require.NoError(t, st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(rsl.Ref), h)))

	// Fail-closed (D6): an unparseable tip must error, not restart numbering at 1.
	_, err = rsl.AppendReferenceEntries(ctx, st, sgn, fixedClock(),
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: h}})
	require.Error(t, err)
}
