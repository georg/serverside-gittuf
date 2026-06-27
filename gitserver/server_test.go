package gitserver_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	objectsigner "github.com/go-git/x/plugin/objectsigner/ssh"
	objectverifier "github.com/go-git/x/plugin/objectverifier/ssh"

	"github.com/georg/serverside-gittuf/gitserver"
	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/txstore"
)

// verifyEntrySig checks the RSL entry commit's SSH signature against pub using
// go-git's object verification (the registered/explicit ObjectVerifier), the
// in-process analog of gittuf's signature check.
func verifyEntrySig(t *testing.T, s storage.Storer, id plumbing.Hash, pub ssh.PublicKey) {
	t.Helper()
	c, err := object.GetCommit(s, id)
	require.NoError(t, err)
	vfr, err := objectverifier.FromKey(pub)
	require.NoError(t, err)
	_, err = c.Verify(context.Background(), object.WithVerifier(vfr))
	require.NoError(t, err)
}

func testSigner(t *testing.T) (rsl.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	sgn, err := objectsigner.FromKey(s)
	require.NoError(t, err)
	return sgn, s.PublicKey()
}

// emptyTreeCommit creates an empty-tree commit in st and returns its hash.
func emptyTreeCommit(t *testing.T, st storage.Storer, msg string, parent plumbing.Hash) plumbing.Hash {
	t.Helper()
	to := st.NewEncodedObject()
	require.NoError(t, (&object.Tree{}).Encode(to))
	tree, err := st.SetEncodedObject(to)
	require.NoError(t, err)
	ident := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1700000000, 0).UTC()}
	c := &object.Commit{Author: ident, Committer: ident, Message: msg, TreeHash: tree}
	if !parent.IsZero() {
		c.ParentHashes = []plumbing.Hash{parent}
	}
	co := st.NewEncodedObject()
	require.NoError(t, c.Encode(co))
	h, err := st.SetEncodedObject(co)
	require.NoError(t, err)
	return h
}

// serverRepo opens a fresh storer over the server's on-disk repo for assertions,
// adapted to the transactional Storer the rsl read API consumes.
func serverRepo(t *testing.T, dataDir, repo string) txstore.Storer {
	t.Helper()
	return txstore.New(filesystem.NewStorage(osfs.New(filepath.Join(dataDir, repo)), cache.NewObjectLRUDefault()))
}

func newClientRepo(t *testing.T, url string) (*git.Repository, txstore.Storer) {
	t.Helper()
	st := memory.NewStorage()
	repo, err := git.Init(st)
	require.NoError(t, err)
	_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	require.NoError(t, err)
	// memory.Storage already implements storer.Transactioner, so txstore.New
	// returns it unwrapped; this just exposes Begin() through the static type.
	return repo, txstore.New(st)
}

func TestPushRecordsRSLAndIsFetchable(t *testing.T) {
	dataDir := t.TempDir()
	sgn, pub := testSigner(t)
	ts := httptest.NewServer(gitserver.New(dataDir, sgn).Handler())
	defer ts.Close()
	url := ts.URL + "/myrepo"

	repo, st := newClientRepo(t, url)
	c1 := emptyTreeCommit(t, st, "first", plumbing.ZeroHash)
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", c1)))
	require.NoError(t, repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/main:refs/heads/main"},
	}))

	// Server recorded an RSL entry for the pushed ref.
	srv := serverRepo(t, dataDir, "myrepo")
	entry, err := rsl.GetLatestReferenceEntryForRef(srv, "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.Number)
	assert.Equal(t, c1, entry.Target)
	verifyEntrySig(t, srv, entry.ID, pub)

	// The RSL ref is fetchable — the "pull to verify" path.
	drepo, dst := newClientRepo(t, url)
	require.NoError(t, drepo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/gittuf/*:refs/gittuf/*"},
	}))
	ref, err := dst.Reference(plumbing.ReferenceName(rsl.Ref))
	require.NoError(t, err)
	assert.Equal(t, entry.ID, ref.Hash())

	// A second push increments and parent-chains.
	c2 := emptyTreeCommit(t, st, "second", c1)
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", c2)))
	require.NoError(t, repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/main:refs/heads/main"},
	}))
	entry2, err := rsl.GetLatestReferenceEntryForRef(serverRepo(t, dataDir, "myrepo"), "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), entry2.Number)
	assert.Equal(t, c2, entry2.Target)
}

func TestPushDeletingRefRecordsZeroOIDEntry(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sgn, pub := testSigner(t)
	ts := httptest.NewServer(gitserver.New(dataDir, sgn).Handler())
	defer ts.Close()
	url := ts.URL + "/myrepo"

	repo, st := newClientRepo(t, url)
	c1 := emptyTreeCommit(t, st, "first", plumbing.ZeroHash)
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", c1)))
	require.NoError(t, repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/main:refs/heads/main"},
	}))

	// Delete the ref on the server (empty source side of the refspec).
	require.NoError(t, repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{":refs/heads/main"},
	}))

	srv := serverRepo(t, dataDir, "myrepo")

	// The ref is gone from the server.
	_, err := srv.Reference("refs/heads/main")
	assert.ErrorIs(t, err, plumbing.ErrReferenceNotFound)

	// The deletion produced a new RSL entry for the ref pointing at the zero-OID,
	// chained after the create entry (number 2).
	entry, err := rsl.GetLatestReferenceEntryForRef(srv, "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), entry.Number)
	assert.Equal(t, plumbing.ZeroHash, entry.Target, "a deletion records the zero-OID target")
	verifyEntrySig(t, srv, entry.ID, pub)
}

func TestCoexistence_ClientRSLDeduped(t *testing.T) {
	dataDir := t.TempDir()
	sgn, _ := testSigner(t)
	ts := httptest.NewServer(gitserver.New(dataDir, sgn).Handler())
	defer ts.Close()
	url := ts.URL + "/myrepo"

	repo, st := newClientRepo(t, url)
	c1 := emptyTreeCommit(t, st, "first", plumbing.ZeroHash)
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", c1)))

	// The client builds its OWN RSL entry for main (signed by its own key) and
	// pushes both the branch and the RSL ref in one push.
	clientSigner, _ := testSigner(t)
	clientEntries, err := rsl.AppendReferenceEntries(context.Background(), st, clientSigner,
		func() time.Time { return time.Unix(1700000001, 0).UTC() },
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: c1}})
	require.NoError(t, err)
	require.Len(t, clientEntries, 1)

	require.NoError(t, repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/gittuf/reference-state-log:refs/gittuf/reference-state-log",
		},
	}))

	// The server adopted the client's RSL and added NO duplicate: the tip is the
	// client's entry, and walking the log yields exactly one entry.
	srv := serverRepo(t, dataDir, "myrepo")
	tip, err := rsl.GetLatestEntry(srv)
	require.NoError(t, err)
	assert.Equal(t, clientEntries[0].ID, tip.GetID(), "tip must be the client's entry, no server duplicate")
	_, err = rsl.GetParentForEntry(srv, tip)
	assert.ErrorIs(t, err, rsl.ErrEntryNotFound, "the client's entry is the only one in the log")

	// The branch itself was applied.
	mainRef, err := srv.Reference("refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, c1, mainRef.Hash())
}

func TestCoexistence_MalformedClientRSLRejected(t *testing.T) {
	dataDir := t.TempDir()
	sgn, pub := testSigner(t)
	ts := httptest.NewServer(gitserver.New(dataDir, sgn).Handler())
	defer ts.Close()
	url := ts.URL + "/myrepo"

	repo, st := newClientRepo(t, url)
	c1 := emptyTreeCommit(t, st, "first", plumbing.ZeroHash)
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", c1)))

	// A malformed "RSL" tip: a commit with no number line. The server must reject
	// the RSL-ref command and record main itself instead.
	bad := emptyTreeCommit(t, st, "not an rsl entry", plumbing.ZeroHash)
	require.NoError(t, st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(rsl.Ref), bad)))

	// The push returns an error because the RSL-ref command is rejected; main
	// still applies and records server-side. Tolerate the error and assert state.
	_ = repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/gittuf/reference-state-log:refs/gittuf/reference-state-log",
		},
	})

	srv := serverRepo(t, dataDir, "myrepo")
	// The RSL tip is a SERVER-written entry (verifies with the server key), not
	// the client's malformed commit.
	entry, err := rsl.GetLatestReferenceEntryForRef(srv, "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.Number)
	assert.Equal(t, c1, entry.Target)
	assert.NotEqual(t, bad, entry.ID, "server must not adopt the malformed client RSL")
	verifyEntrySig(t, srv, entry.ID, pub)
}
