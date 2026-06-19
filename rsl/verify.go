package rsl

import (
	"bytes"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// VerifyEntrySignature re-derives the canonical signature-free payload of the
// commit at h (via go-git's EncodeWithoutSignature, the mirror image of signing)
// and verifies its armored SSHSIG against pub — no *Repository, git binary, or
// working tree. It is the standalone analog of gittuf's signature verification;
// a full gittuf verifier additionally checks pub is an authorized principal in
// the repo's root of trust.
func VerifyEntrySignature(st Storer, h plumbing.Hash, pub ssh.PublicKey) error {
	c, err := loadCommit(st, h)
	if err != nil {
		return err
	}
	if c.Signature == "" {
		return fmt.Errorf("rsl: commit %s is unsigned", h)
	}

	payloadObj := st.NewEncodedObject()
	if err := c.EncodeWithoutSignature(payloadObj); err != nil {
		return fmt.Errorf("rsl: encode payload: %w", err)
	}
	payload, err := readObject(payloadObj)
	if err != nil {
		return err
	}

	sig, err := sshsig.Unarmor([]byte(c.Signature))
	if err != nil {
		return fmt.Errorf("rsl: unarmor signature on %s: %w", h, err)
	}
	if err := sshsig.Verify(bytes.NewReader(payload), sig, pub, sshsig.HashSHA512, SignatureNamespace); err != nil {
		return fmt.Errorf("rsl: verify signature on %s: %w", h, err)
	}
	return nil
}
