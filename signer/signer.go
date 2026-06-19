// Package signer provides the per-cluster SSH signer the RSL writer injects.
// Signatures are SSHSIG over go-git's signature-free commit payload (namespace
// "git", SHA-512) — gittuf's scheme. The corresponding public key must be an
// authorized principal in each repo's gittuf root-of-trust for a gittuf client
// to verify the log.
package signer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/georg/serverside-gittuf/rsl"
	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// NewSSHSigner builds an rsl.Signer from an ed25519 private key.
func NewSSHSigner(key ed25519.PrivateKey) (rsl.Signer, error) {
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("signer: build ssh signer from ed25519 key: %w", err)
	}
	return &sshSigner{s: s}, nil
}

type sshSigner struct{ s ssh.Signer }

// Sign returns the armored SSHSIG over the signature-free commit payload
// (HashSHA512, namespace "git"), which the RSL writer sets as the gpgsig header.
func (z *sshSigner) Sign(_ context.Context, payload []byte) ([]byte, error) {
	sig, err := sshsig.Sign(bytes.NewReader(payload), z.s, sshsig.HashSHA512, rsl.SignatureNamespace)
	if err != nil {
		return nil, fmt.Errorf("signer: ssh-sign commit: %w", err)
	}
	return sshsig.Armor(sig), nil
}

func (z *sshSigner) KeyID() string { return ssh.FingerprintSHA256(z.s.PublicKey()) }

var _ rsl.Signer = (*sshSigner)(nil)

// LoadOrGenerate loads an OpenSSH ed25519 private key from path, or generates
// and persists one (mode 0600) if path does not exist, also writing the public
// half to path+".pub". It returns the private key and the SSH public key. The
// caller should publish the public key so repo admins can authorize it.
func LoadOrGenerate(path string) (ed25519.PrivateKey, ssh.PublicKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		raw, err := ssh.ParseRawPrivateKey(b)
		if err != nil {
			return nil, nil, fmt.Errorf("signer: parse private key %s: %w", path, err)
		}
		key, ok := privateKeyEd25519(raw)
		if !ok {
			return nil, nil, fmt.Errorf("signer: key %s is not ed25519", path)
		}
		pub, err := ssh.NewPublicKey(key.Public())
		if err != nil {
			return nil, nil, fmt.Errorf("signer: derive public key: %w", err)
		}
		return key, pub, nil
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("signer: read key %s: %w", path, err)
	}

	pubEd, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("signer: generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(key, "serverside-gittuf rsl cluster key")
	if err != nil {
		return nil, nil, fmt.Errorf("signer: marshal private key: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("signer: create key dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, nil, fmt.Errorf("signer: write key %s: %w", path, err)
	}
	pub, err := ssh.NewPublicKey(pubEd)
	if err != nil {
		return nil, nil, fmt.Errorf("signer: derive public key: %w", err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(pub), 0o644); err != nil {
		return nil, nil, fmt.Errorf("signer: write public key %s.pub: %w", path, err)
	}
	return key, pub, nil
}

// privateKeyEd25519 normalizes ParseRawPrivateKey's result (which may be an
// ed25519.PrivateKey or a *ed25519.PrivateKey) to an ed25519.PrivateKey.
func privateKeyEd25519(raw any) (ed25519.PrivateKey, bool) {
	switch k := raw.(type) {
	case ed25519.PrivateKey:
		return k, true
	case *ed25519.PrivateKey:
		return *k, true
	default:
		return nil, false
	}
}
