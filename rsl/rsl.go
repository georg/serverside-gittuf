// Package rsl writes and reads a gittuf-compatible Reference State Log (RSL) at
// refs/gittuf/reference-state-log, operating directly over a go-git v6
// storage.Storer (the narrow Storer interface in this package) plus an injected
// Signer.
//
// It follows the "Storer model" from entiredb's gittuf-storer-interface-spec:
// gittuf's RSL logic delegates the commit codec to go-git v6
// (object.Commit.Encode / EncodeWithoutSignature) and signs in-process via the
// Signer, rather than shelling out to git or owning a hand-rolled codec. The
// byte format and signing scheme here MUST match gittuf's internal/rsl +
// pkg/gitinterface so gittuf clients can verify the log; the cross-verify test
// (gitserver/gittuf_verify_test.go) guards that compatibility.
package rsl

import (
	"fmt"
	"strconv"
	"strings"
)

// Ref is the gittuf Reference State Log ref. Mirrors gittuf internal/rsl: Ref.
const Ref = "refs/gittuf/reference-state-log"

// PolicyStagingRef is gittuf's policy staging area; it is never recorded.
const PolicyStagingRef = "refs/gittuf/policy-staging"

// SignatureNamespace is the SSH signature namespace gittuf uses for git object
// signatures (gittuf internal/signerverifier/ssh.SigNamespace). Both the Signer
// and VerifyEntrySignature use it.
const SignatureNamespace = "git"

// Entry-type message headers and field keys, byte-identical to gittuf's
// internal/rsl constants. Do not change without updating the cross-verify test.
const (
	ReferenceEntryHeader   = "RSL Reference Entry"
	AnnotationEntryHeader  = "RSL Annotation Entry"
	PropagationEntryHeader = "RSL Propagation Entry"

	refKey      = "ref"
	targetIDKey = "targetID"
	numberKey   = "number"
)

// referenceEntryMessage builds the exact commit message for an RSL
// ReferenceEntry. The layout matches gittuf's createCommitMessage:
//
//	RSL Reference Entry\n\nref: <refName>\ntargetID: <hex>\nnumber: <n>
//
// with NO trailing newline. number is always >= 1 here (the server assigns it).
func referenceEntryMessage(refName, targetID string, number uint64) string {
	return strings.Join([]string{
		ReferenceEntryHeader,
		"",
		fmt.Sprintf("%s: %s", refKey, refName),
		fmt.Sprintf("%s: %s", targetIDKey, targetID),
		fmt.Sprintf("%s: %d", numberKey, number),
	}, "\n")
}

// ParseReferenceEntry extracts (refName, targetID) from a commit message if it
// is an RSL ReferenceEntry. ok is false for Annotation/Propagation/non-RSL
// messages — they are not ref-state records and are ignored by the dedup scan.
func ParseReferenceEntry(message string) (refName, targetID string, ok bool) {
	lines := strings.Split(message, "\n")
	if len(lines) == 0 || lines[0] != ReferenceEntryHeader {
		return "", "", false
	}
	for _, line := range lines[1:] {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case refKey:
			refName = strings.TrimSpace(val)
		case targetIDKey:
			targetID = strings.TrimSpace(val)
		}
	}
	if refName == "" || targetID == "" {
		return "", "", false
	}
	return refName, targetID, true
}

// ParseNumber extracts the "number:" field from any RSL entry commit message
// (Reference, Annotation, or Propagation). A client-managed RSL can place any
// entry type at the tip, so this must not assume ReferenceEntry. Returns
// (0, false) when no number line is present (an unnumbered legacy entry);
// callers treat a parse miss as fail-closed, never as "first entry".
func ParseNumber(message string) (uint64, bool, error) {
	for _, line := range strings.Split(message, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != numberKey {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("malformed RSL entry number %q: %w", strings.TrimSpace(val), err)
		}
		return n, true, nil
	}
	return 0, false, nil
}

// ShouldRecord reports whether a ref update should produce an RSL
// ReferenceEntry: everything except the RSL ref itself and
// refs/gittuf/policy-staging. This is the recording-scope policy
// (gittuf's pkg/rsl does not ship it).
func ShouldRecord(refName string) bool {
	return refName != Ref && refName != PolicyStagingRef
}
