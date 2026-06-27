package rsl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os/exec"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"golang.org/x/crypto/ssh"

	objectsigner "github.com/go-git/x/plugin/objectsigner/ssh"
	objectverifier "github.com/go-git/x/plugin/objectverifier/ssh"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/txstore"
)

// benchSignedRSL writes one signed RSL entry to an on-disk repo and returns the
// repo, the entry commit id, and a WithVerifier option for the signing key.
func benchSignedRSL(b *testing.B) (dir string, repo *git.Repository, id plumbing.Hash, withVerifier object.VerifyOption) {
	b.Helper()
	dir = b.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		b.Fatal(err)
	}
	st := txstore.New(repo.Storer)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	raw, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		b.Fatal(err)
	}
	sgn, err := objectsigner.FromKey(raw)
	if err != nil {
		b.Fatal(err)
	}

	now := func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	entries, err := rsl.AppendReferenceEntries(context.Background(), st, sgn, now,
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}})
	if err != nil {
		b.Fatal(err)
	}

	vfr, err := objectverifier.FromKey(raw.PublicKey())
	if err != nil {
		b.Fatal(err)
	}
	return dir, repo, entries[0].ID, object.WithVerifier(vfr)
}

// BenchmarkVerifyInProcess measures the verification logic introduced here:
// go-git reproduces the signature-free payload and checks the SSHSIG entirely
// in-process. Each iteration loads the commit by id and verifies it.
func BenchmarkVerifyInProcess(b *testing.B) {
	_, repo, id, withVerifier := benchSignedRSL(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		c, err := repo.CommitObject(id)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := c.Verify(ctx, withVerifier); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerifyViaGitBinary measures the gittuf-style path. gittuf's
// gitinterface.VerifySignature forks `git cat-file -t <id>` (ensureIsCommit)
// before doing the same in-process SSHSIG check. gittuf cannot be imported here
// (it pins an incompatible go-git), so this reproduces its git-binary step and
// reuses the identical in-process verify. It is conservative: gittuf also
// re-opens the repository on every call, which this does not. The fork is the
// per-verify cost the in-process path removes.
func BenchmarkVerifyViaGitBinary(b *testing.B) {
	dir, repo, id, withVerifier := benchSignedRSL(b)
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git binary not available")
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := exec.Command("git", "-C", dir, "cat-file", "-t", id.String()).Run(); err != nil {
			b.Fatal(err)
		}
		c, err := repo.CommitObject(id)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := c.Verify(ctx, withVerifier); err != nil {
			b.Fatal(err)
		}
	}
}
