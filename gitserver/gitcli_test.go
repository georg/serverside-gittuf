package gitserver_test

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/georg/serverside-gittuf/gitserver"
)

// TestGitCLIInterop drives the server with the real git binary (not go-git
// talking to go-git): clone-less init + push, then fetch the RSL ref and read
// the recorded entry. Skipped when git is unavailable.
func TestGitCLIInterop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	dataDir := t.TempDir()
	sgn, _ := testSigner(t)
	ts := httptest.NewServer(gitserver.New(dataDir, sgn).Handler())
	defer ts.Close()
	url := ts.URL + "/cli-repo"

	work := t.TempDir()
	runGit(t, work, "init", "-q", "-b", "main")
	runGit(t, work, "commit", "-q", "--allow-empty", "-m", "hello from git cli")
	runGit(t, work, "remote", "add", "origin", url)
	runGit(t, work, "push", "-q", "origin", "main:refs/heads/main")

	// Pull the RSL ref and confirm the server recorded the push.
	runGit(t, work, "fetch", "-q", "origin", "refs/gittuf/*:refs/gittuf/*")
	out := runGit(t, work, "log", "--format=%B", "refs/gittuf/reference-state-log")
	assert.Contains(t, out, "RSL Reference Entry")
	assert.Contains(t, out, "ref: refs/heads/main")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s\n%s", strings.Join(args, " "), out)
	return string(out)
}
