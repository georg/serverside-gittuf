package rsl_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	objectsigner "github.com/go-git/x/plugin/objectsigner/ssh"
	objectverifier "github.com/go-git/x/plugin/objectverifier/ssh"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/georg/serverside-gittuf/txstore"
	"github.com/georg/serverside-gittuf/util"
)

// TestGittufCLIInterop exercises full round-trip interoperability against the
// real gittuf CLI: an RSL entry gittuf records must verify under this code's
// in-process verifier, and an RSL entry this code records must pass gittuf's
// own policy verification (verify-ref). Both writers sign with the same SSH key,
// authorized in a gittuf policy for refs/heads/main.
func TestGittufCLIInterop(t *testing.T) {
	for _, bin := range []string{"git", "gittuf"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	priv, pub, err := util.LoadOrGenerate(keyPath) // writes keyPath + keyPath.pub
	require.NoError(t, err)
	pubPath := keyPath + ".pub"

	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		// Isolate from the developer's global git/signing config and enable
		// gittuf's dev-only flags (--create-rsl-entry, local policy apply).
		cmd.Env = append(os.Environ(), "GITTUF_DEV=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %s\n%s", name, strings.Join(args, " "), out)
		return string(out)
	}
	runGit := func(args ...string) string { return run("git", args...) }
	runGittuf := func(args ...string) string { return run("gittuf", args...) }

	runGit("init", "-q", "-b", "main", ".")
	runGit("config", "user.name", "tester")
	runGit("config", "user.email", "tester@example.test")
	runGit("config", "gpg.format", "ssh")
	runGit("config", "user.signingkey", pubPath)
	runGit("commit", "-q", "--allow-empty", "-m", "initial")

	// gittuf root of trust + a policy authorizing our key for refs/heads/main.
	runGittuf("trust", "init", "-k", keyPath, "--create-rsl-entry")
	runGittuf("trust", "add-policy-key", "-k", keyPath, "--policy-key", pubPath, "--create-rsl-entry")
	runGittuf("policy", "init", "-k", keyPath, "--policy-name", "targets", "--create-rsl-entry")
	runGittuf("policy", "add-key", "-k", keyPath, "--public-key", pubPath, "--create-rsl-entry")
	runGittuf("policy", "apply", "--local-only")
	runGittuf("policy", "add-rule", "-k", keyPath, "--rule-name", "protect-main",
		"--rule-pattern", "git:refs/heads/main", "--authorize", ssh.FingerprintSHA256(pub), "--create-rsl-entry")
	runGittuf("policy", "apply", "--local-only")

	// gittuf records an RSL entry for main and verifies its own log (baseline).
	runGit("commit", "-q", "--allow-empty", "-m", "c1")
	runGittuf("rsl", "record", "--local-only", "main")
	runGittuf("verify-ref", "main")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	st := txstore.New(repo.Storer)
	ctx := context.Background()

	vfr, err := objectverifier.FromKey(pub)
	require.NoError(t, err)

	// gittuf -> this code: the entry gittuf recorded verifies under our verifier.
	gtEntry, err := rsl.GetLatestReferenceEntryForRef(st, "refs/heads/main")
	require.NoError(t, err)
	gtCommit, err := repo.CommitObject(gtEntry.ID)
	require.NoError(t, err)
	_, err = gtCommit.Verify(ctx, object.WithVerifier(vfr))
	require.NoError(t, err, "gittuf-created RSL entry must verify under this code")

	// this code -> gittuf: we record an RSL entry for main, gittuf must verify it.
	runGit("commit", "-q", "--allow-empty", "-m", "c2")
	head := plumbing.NewHash(strings.TrimSpace(runGit("rev-parse", "HEAD")))
	sshSigner, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	sgn, err := objectsigner.FromKey(sshSigner)
	require.NoError(t, err)

	ours, err := rsl.AppendReferenceEntries(ctx, st, sgn, time.Now,
		[]rsl.RefChange{{RefName: "refs/heads/main", Target: head}})
	require.NoError(t, err)
	require.Len(t, ours, 1)

	runGittuf("verify-ref", "main") // gittuf verifies the entry this code signed

	// And our verifier accepts our own entry too (closing the loop).
	ourCommit, err := repo.CommitObject(ours[0].ID)
	require.NoError(t, err)
	_, err = ourCommit.Verify(ctx, object.WithVerifier(vfr))
	require.NoError(t, err)
}
