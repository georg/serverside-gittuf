// Package gitserver serves the git smart-HTTP protocol over go-git v6 and, on
// every push, records a gittuf RSL entry per ref change. The only interface is
// git: clients push normally and fetch refs/gittuf/reference-state-log to verify
// what was written.
//
// It leans on go-git for advertisement (transport.AdvertiseRefs), fetch
// (transport.UploadPack), and packfile ingest (packfile.UpdateObjectStorage),
// but OWNS the receive-pack ref-commit step so the RSL append happens before the
// "ok" report-status reaches the client (go-git's transport.ReceivePack has no
// hook and emits report-status itself). See receivepack.go.
package gitserver

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"

	"github.com/georg/serverside-gittuf/rsl"
)

// Server is the smart-HTTP git server with server-side RSL recording.
type Server struct {
	dataDir string
	signer  rsl.Signer
	now     func() time.Time
	locks   *keyedMutex
	logger  *slog.Logger
}

// New creates a Server rooted at dataDir, signing RSL entries with signer.
func New(dataDir string, signer rsl.Signer) *Server {
	return &Server{
		dataDir: dataDir,
		signer:  signer,
		now:     time.Now,
		locks:   newKeyedMutex(),
		logger:  slog.Default(),
	}
}

var (
	errInvalidRepo  = errors.New("invalid repository name")
	errRepoNotFound = errors.New("repository not found")
)

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// cleanRepo validates and normalizes a repo path segment from the URL,
// rejecting traversal and odd segments.
func cleanRepo(repo string) (string, error) {
	repo = strings.Trim(repo, "/")
	if repo == "" || !repoNameRe.MatchString(repo) || strings.Contains(repo, "..") {
		return "", errInvalidRepo
	}
	for _, seg := range strings.Split(repo, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errInvalidRepo
		}
	}
	return repo, nil
}

// openStorer opens (or, when create is set, auto-inits as a bare sha1 repo) the
// go-git storer for repo. A sha256 repo is supported by pre-creating the bare
// repo on disk; the server opens whatever format it finds.
func (s *Server) openStorer(repo string, create bool) (storage.Storer, error) {
	clean, err := cleanRepo(repo)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dataDir, filepath.FromSlash(clean))

	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		if !create {
			return nil, errRepoNotFound
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create repo dir: %w", err)
		}
	}

	st := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
	if _, err := git.Open(st, nil); err != nil {
		if !errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, fmt.Errorf("open repo %q: %w", clean, err)
		}
		if !create {
			return nil, errRepoNotFound
		}
		if _, err := git.Init(st); err != nil {
			return nil, fmt.Errorf("init repo %q: %w", clean, err)
		}
		s.logger.Info("auto-initialized repository", "repo", clean, "dir", dir)
	}
	return st, nil
}

// keyedMutex hands out a *sync.Mutex per repo, created on first use and never
// evicted (bounded by the number of repos). It serializes the receive-pack
// critical section (validate + apply refs + RSL append) per repo — the
// in-process analog of entiredb's per-repo advisory lock.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{m: map[string]*sync.Mutex{}} }

func (k *keyedMutex) get(key string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	mu, ok := k.m[key]
	if !ok {
		mu = &sync.Mutex{}
		k.m[key] = mu
	}
	return mu
}
