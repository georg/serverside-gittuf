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

// Entry-type message headers and field keys, byte-identical to gittuf's
// internal/rsl constants. Do not change without updating the cross-verify test.
const (
	ReferenceEntryHeader   = "RSL Reference Entry"
	AnnotationEntryHeader  = "RSL Annotation Entry"
	PropagationEntryHeader = "RSL Propagation Entry"

	refKey                = "ref"
	targetIDKey           = "targetID"
	numberKey             = "number"
	entryIDKey            = "entryID"
	skipKey               = "skip"
	upstreamRepositoryKey = "upstreamRepository"
	upstreamEntryIDKey    = "upstreamEntryID"

	// beginMessage opens the PEM block that holds an annotation's free-text
	// message; it is the last element of an annotation entry.
	beginMessage = "-----BEGIN MESSAGE-----"
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
// is an RSL ReferenceEntry. ok is false with a nil error for
// Annotation/Propagation/non-RSL messages — they are not ref-state records and
// are skipped by the dedup scan. A non-nil error means the message *is* a
// ReferenceEntry but is malformed (a duplicate or out-of-order field, an
// unexpected line, or a missing field); callers fail closed on it.
func ParseReferenceEntry(message string) (refName, targetID string, ok bool, err error) {
	lines := strings.Split(message, "\n")
	if lines[0] != ReferenceEntryHeader { // Split always yields at least one line.
		return "", "", false, nil
	}
	e, err := parseReferenceLines(lines)
	if err != nil {
		return "", "", false, err
	}
	return e.refName, e.targetID, true, nil
}

// ParseNumber returns the "number:" field of an RSL entry of any type
// (Reference, Annotation, or Propagation). A client-managed RSL can place any
// entry type at the tip, so it dispatches on the header and validates the whole
// canonical layout, erroring on a duplicate or out-of-order field. It returns
// (0, false, nil) when the entry is well-formed but carries no number line (an
// unnumbered legacy entry) or the message is not an RSL entry at all; callers
// treat a parse miss as fail-closed, never as "first entry".
func ParseNumber(message string) (uint64, bool, error) {
	e, err := parseEntry(message)
	if err != nil {
		return 0, false, err
	}
	if !e.hasNumber {
		return 0, false, nil
	}
	return e.number, true, nil
}

// entryKind identifies an RSL entry by its header. kindUnknown means the
// message carries no recognized header (not an RSL entry).
type entryKind int

const (
	kindUnknown entryKind = iota
	kindReference
	kindAnnotation
	kindPropagation
)

// parsedEntry is the structured result of the entry state machine. Only the
// fields relevant to the entry's kind are populated.
type parsedEntry struct {
	kind      entryKind
	refName   string
	targetID  string
	hasNumber bool
	number    uint64
}

// parseEntry runs the line state machine for whichever entry type the header
// names, validating the canonical field order and rejecting duplicate or
// out-of-order fields. A message with no recognized header is reported as
// kindUnknown (not an error), so callers can probe arbitrary commits.
func parseEntry(message string) (*parsedEntry, error) {
	lines := strings.Split(message, "\n") // always at least one line
	switch lines[0] {
	case ReferenceEntryHeader:
		return parseReferenceLines(lines)
	case AnnotationEntryHeader:
		return parseAnnotationLines(lines)
	case PropagationEntryHeader:
		return parsePropagationLines(lines)
	default:
		return &parsedEntry{kind: kindUnknown}, nil
	}
}

// parseReferenceLines walks: header, blank, ref, targetID, [number], EOF.
func parseReferenceLines(lines []string) (*parsedEntry, error) {
	e := &parsedEntry{kind: kindReference}
	if err := expectBlank(lines, 1); err != nil {
		return nil, err
	}
	i := 2
	var err error
	if e.refName, err = field(lines, i, refKey); err != nil {
		return nil, err
	}
	i++
	if e.targetID, err = field(lines, i, targetIDKey); err != nil {
		return nil, err
	}
	i++
	if i, err = optionalNumber(lines, i, e); err != nil {
		return nil, err
	}
	if err = expectEnd(lines, i); err != nil {
		return nil, err
	}
	if e.refName == "" || e.targetID == "" {
		return nil, fmt.Errorf("rsl: reference entry has an empty ref or targetID")
	}
	return e, nil
}

// parsePropagationLines walks: header, blank, ref, targetID,
// upstreamRepository, upstreamEntryID, [number], EOF.
func parsePropagationLines(lines []string) (*parsedEntry, error) {
	e := &parsedEntry{kind: kindPropagation}
	if err := expectBlank(lines, 1); err != nil {
		return nil, err
	}
	i := 2
	var err error
	if e.refName, err = field(lines, i, refKey); err != nil {
		return nil, err
	}
	i++
	if e.targetID, err = field(lines, i, targetIDKey); err != nil {
		return nil, err
	}
	i++
	if _, err = field(lines, i, upstreamRepositoryKey); err != nil {
		return nil, err
	}
	i++
	if _, err = field(lines, i, upstreamEntryIDKey); err != nil {
		return nil, err
	}
	i++
	if i, err = optionalNumber(lines, i, e); err != nil {
		return nil, err
	}
	if err = expectEnd(lines, i); err != nil {
		return nil, err
	}
	return e, nil
}

// parseAnnotationLines walks: header, blank, one-or-more entryID, skip,
// [number], [PEM message block]. The trailing message block, if present, is
// opaque and consumed to EOF.
func parseAnnotationLines(lines []string) (*parsedEntry, error) {
	e := &parsedEntry{kind: kindAnnotation}
	if err := expectBlank(lines, 1); err != nil {
		return nil, err
	}
	i := 2
	entryIDs := 0
	for i < len(lines) {
		key, _, found := strings.Cut(lines[i], ":")
		if !found || strings.TrimSpace(key) != entryIDKey {
			break
		}
		entryIDs++
		i++
	}
	if entryIDs == 0 {
		return nil, fmt.Errorf("rsl: annotation entry has no %s field", entryIDKey)
	}

	skip, err := field(lines, i, skipKey)
	if err != nil {
		return nil, err
	}
	if skip != "true" && skip != "false" {
		return nil, fmt.Errorf("rsl: annotation %s must be true or false, got %q", skipKey, skip)
	}
	i++

	if i, err = optionalNumber(lines, i, e); err != nil {
		return nil, err
	}

	// Anything left must be the free-text message PEM block (opaque to EOF).
	if i < len(lines) && lines[i] != beginMessage {
		return nil, fmt.Errorf("rsl: unexpected line %d in annotation entry: %q", i, lines[i])
	}
	return e, nil
}

// field asserts that lines[i] is "key: value" for the expected key, returning
// the trimmed value. Because the caller advances i field by field, a field in
// the wrong position (out of order) or a repeated field lands here against the
// wrong expected key and is rejected.
func field(lines []string, i int, key string) (string, error) {
	if i >= len(lines) {
		return "", fmt.Errorf("rsl: truncated entry: expected %q at line %d", key, i)
	}
	k, v, ok := strings.Cut(lines[i], ":")
	if !ok {
		return "", fmt.Errorf("rsl: line %d %q is not a key: value field", i, lines[i])
	}
	if got := strings.TrimSpace(k); got != key {
		return "", fmt.Errorf("rsl: line %d: expected %q, got %q", i, key, got)
	}
	return strings.TrimSpace(v), nil
}

// optionalNumber consumes a number line at i if present, recording it on e and
// returning the next index; otherwise it leaves i unchanged. A duplicate number
// line is rejected downstream (it lands where EOF or the message block is
// expected).
func optionalNumber(lines []string, i int, e *parsedEntry) (int, error) {
	if i >= len(lines) {
		return i, nil
	}
	k, v, ok := strings.Cut(lines[i], ":")
	if !ok || strings.TrimSpace(k) != numberKey {
		return i, nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return i, fmt.Errorf("rsl: malformed entry number %q: %w", strings.TrimSpace(v), err)
	}
	e.number = n
	e.hasNumber = true
	return i + 1, nil
}

// expectBlank requires lines[i] to be the empty line that follows every header.
func expectBlank(lines []string, i int) error {
	if i >= len(lines) {
		return fmt.Errorf("rsl: truncated entry: expected blank line at %d", i)
	}
	if lines[i] != "" {
		return fmt.Errorf("rsl: expected blank line after header, got %q", lines[i])
	}
	return nil
}

// expectEnd requires that i is past the last line, rejecting any trailing
// content (including a duplicate field that survived the ordered walk).
func expectEnd(lines []string, i int) error {
	if i != len(lines) {
		return fmt.Errorf("rsl: unexpected trailing line %d: %q", i, lines[i])
	}
	return nil
}

// ShouldRecord reports whether a ref update should produce an RSL
// ReferenceEntry: everything except the RSL ref itself and
// refs/gittuf/policy-staging. This is the recording-scope policy
// (gittuf's pkg/rsl does not ship it).
func ShouldRecord(refName string) bool {
	return refName != Ref && refName != PolicyStagingRef
}
