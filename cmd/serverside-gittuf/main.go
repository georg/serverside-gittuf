// Command serverside-gittuf runs a git smart-HTTP server that records a gittuf
// RSL entry for every pushed ref change. Clients push normally and fetch
// refs/gittuf/reference-state-log to verify what was written — git is the only
// interface.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-git/go-git/v6/x/plugin"
	objectsigner "github.com/go-git/x/plugin/objectsigner/ssh"
	objectverifier "github.com/go-git/x/plugin/objectverifier/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/georg/serverside-gittuf/gitserver"
	"github.com/georg/serverside-gittuf/util"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dataDir := flag.String("data-dir", "./data", "directory holding bare repositories")
	keyPath := flag.String("signing-key", "", "path to the RSL signing key (default <data-dir>/cluster_ed25519)")
	flag.Parse()

	if *keyPath == "" {
		*keyPath = *dataDir + "/cluster_ed25519"
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	key, pub, err := util.LoadOrGenerate(*keyPath)
	if err != nil {
		logger.Error("load signing key", "err", err)
		os.Exit(1)
	}
	sshSigner, err := ssh.NewSignerFromKey(key)
	if err != nil {
		logger.Error("build ssh signer from key", "err", err)
		os.Exit(1)
	}
	sgn, err := objectsigner.FromKey(sshSigner)
	if err != nil {
		logger.Error("signer from key", "err", err)
		os.Exit(1)
	}

	vfr, err := objectverifier.FromKey(sshSigner.PublicKey())
	if err != nil {
		logger.Error("verifier from key", "err", err)
		os.Exit(1)
	}

	plugin.Register(plugin.ObjectSigner(), func() plugin.Signer { return sgn })
	// Verifier could in fact be a "chain" verifier, that navigates through
	// different public keys.
	plugin.Register(plugin.ObjectVerifier(), func() plugin.Verifier { return vfr })

	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	logger.Info("RSL signing key loaded",
		"fingerprint", ssh.FingerprintSHA256(pub),
		"public_key", authorized,
		"note", "add this key to a repo's gittuf root of trust to verify its RSL")
	fmt.Fprintf(os.Stderr, "\nRSL public key (authorize this in gittuf policy to verify):\n  %s\n\n", authorized)

	srv := gitserver.New(*dataDir, sgn)
	logger.Info("serving git smart-HTTP", "addr", *addr, "data_dir", *dataDir)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
