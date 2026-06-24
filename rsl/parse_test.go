package rsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/georg/serverside-gittuf/rsl"
)

func TestParseReferenceEntry(t *testing.T) {
	t.Parallel()

	const ref = rsl.ReferenceEntryHeader

	tests := map[string]struct {
		message    string
		wantRef    string
		wantTarget string
		wantOK     bool
		wantErr    bool
	}{
		"valid with number": {
			message:    ref + "\n\nref: refs/heads/main\ntargetID: aaa\nnumber: 1",
			wantRef:    "refs/heads/main",
			wantTarget: "aaa",
			wantOK:     true,
		},
		"valid without number": {
			message:    ref + "\n\nref: refs/heads/main\ntargetID: aaa",
			wantRef:    "refs/heads/main",
			wantTarget: "aaa",
			wantOK:     true,
		},
		"not a reference entry": {
			message: rsl.AnnotationEntryHeader + "\n\nentryID: aaa\nskip: false",
			wantOK:  false,
		},
		"not an rsl entry at all": {
			message: "just a normal commit\n\nbody",
			wantOK:  false,
		},
		"duplicate ref": {
			message: ref + "\n\nref: refs/heads/main\nref: refs/heads/evil\ntargetID: aaa",
			wantErr: true,
		},
		"duplicate targetID": {
			message: ref + "\n\nref: refs/heads/main\ntargetID: aaa\ntargetID: bbb",
			wantErr: true,
		},
		"fields out of order": {
			message: ref + "\n\ntargetID: aaa\nref: refs/heads/main",
			wantErr: true,
		},
		"trailing junk": {
			message: ref + "\n\nref: refs/heads/main\ntargetID: aaa\nnumber: 1\nextra: x",
			wantErr: true,
		},
		"missing targetID": {
			message: ref + "\n\nref: refs/heads/main\nnumber: 1",
			wantErr: true,
		},
		"missing blank line": {
			message: ref + "\nref: refs/heads/main\ntargetID: aaa",
			wantErr: true,
		},
		"empty ref value": {
			message: ref + "\n\nref: \ntargetID: aaa",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gotRef, gotTarget, ok, err := rsl.ParseReferenceEntry(tc.message)
			if tc.wantErr {
				require.Error(t, err)
				assert.False(t, ok)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantRef, gotRef)
			assert.Equal(t, tc.wantTarget, gotTarget)
		})
	}
}

func TestParseNumber(t *testing.T) {
	t.Parallel()

	const (
		refH  = rsl.ReferenceEntryHeader
		annH  = rsl.AnnotationEntryHeader
		propH = rsl.PropagationEntryHeader
	)
	const msgBlock = "-----BEGIN MESSAGE-----\naGk=\n-----END MESSAGE-----"

	tests := map[string]struct {
		message string
		wantN   uint64
		wantOK  bool
		wantErr bool
	}{
		"reference with number": {
			message: refH + "\n\nref: refs/heads/main\ntargetID: aaa\nnumber: 7",
			wantN:   7,
			wantOK:  true,
		},
		"reference without number": {
			message: refH + "\n\nref: refs/heads/main\ntargetID: aaa",
			wantOK:  false,
		},
		"propagation with number": {
			message: propH + "\n\nref: refs/heads/main\ntargetID: aaa\nupstreamRepository: u\nupstreamEntryID: bbb\nnumber: 9",
			wantN:   9,
			wantOK:  true,
		},
		"annotation with number and message": {
			message: annH + "\n\nentryID: aaa\nskip: true\nnumber: 4\n" + msgBlock,
			wantN:   4,
			wantOK:  true,
		},
		"annotation multiple entryIDs": {
			message: annH + "\n\nentryID: aaa\nentryID: bbb\nskip: false\nnumber: 2",
			wantN:   2,
			wantOK:  true,
		},
		"annotation without number": {
			message: annH + "\n\nentryID: aaa\nskip: false",
			wantOK:  false,
		},
		"not an rsl entry": {
			message: "normal commit",
			wantOK:  false,
		},
		"duplicate number": {
			message: refH + "\n\nref: refs/heads/main\ntargetID: aaa\nnumber: 1\nnumber: 2",
			wantErr: true,
		},
		"non-numeric number": {
			message: refH + "\n\nref: refs/heads/main\ntargetID: aaa\nnumber: abc",
			wantErr: true,
		},
		"propagation out of order": {
			message: propH + "\n\nref: refs/heads/main\nupstreamRepository: u\ntargetID: aaa\nupstreamEntryID: bbb\nnumber: 1",
			wantErr: true,
		},
		"annotation missing skip": {
			message: annH + "\n\nentryID: aaa\nnumber: 1",
			wantErr: true,
		},
		"annotation bad skip value": {
			message: annH + "\n\nentryID: aaa\nskip: maybe\nnumber: 1",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, ok, err := rsl.ParseNumber(tc.message)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantN, n)
		})
	}
}
