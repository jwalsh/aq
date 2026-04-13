// Package codecs is the wire-format research lab for aq.
//
// Six codecs span the design space from self-describing JSON to
// dictionary-coded binary. The point is empirical comparison under
// adverse conditions, not picking a winner before measuring.
//
// Hegelian framing:
//
//	thesis     — self-describing, human-readable, large (json, cbor)
//	antithesis — schema-required, opaque, tiny (varint, dict)
//	synthesis  — pipe-positional sits in between, brittle but small
//
// The harness measures: size distributions, identity attribution under
// truncation, decode resilience under bit-flips, and the cost of dict
// desync. None of these can be predicted from theory alone.
//
// This package has its own go.mod so its experimental deps (CBOR, etc.)
// stay out of the root aq module.
package codecs

import "errors"

// Record is the codec-internal broadcast representation.
//
// Field names match the v3 wire spec but the type is decoupled from
// main.Broadcast so this package can be tested in isolation. Conversion
// between Record and main.Broadcast happens at the boundary in the
// harness.
//
// Phase and Status are stored as single characters (v3 wire form):
//
//	phase:  c=conjecture p=proof r=refutation n=refinement
//	status: a=active(prosecuting) d=done b=blocked
type Record struct {
	V        int      // wire version (3 for v3, 0 for unspecified)
	Host     string   // short hostname, 1-8 chars typical
	User     string   // username, 1-8 chars typical
	Agent    string   // remote/branch path, 10-40 chars typical
	Worktree string   // branch name
	CID      string   // conjecture id, e.g. "C-42"
	Claim    string   // human-readable intent
	Phase    string   // single char
	Status   string   // single char
	Files    []string // optional supporting context
	Ts       int64    // unix seconds
	TTL      int      // seconds until expiry
	ID       string   // ULID, 22 hex chars
}

// Codec is the wire format interface every research codec implements.
//
// Encode produces the wire bytes for a Record. Decode parses bytes back
// into a Record. Round-trip MUST preserve identity (host, user) and
// conjecture (cid, phase, status). Other fields MAY be lossy if the
// codec needs to truncate to fit a frame budget — the harness measures
// loss instead of treating it as an error.
type Codec interface {
	// Name returns the short codec identifier (e.g. "json", "cbor").
	// Used for OTel metric labels and report tables.
	Name() string

	// MaxBytes returns the codec's hard ceiling, or 0 if unlimited.
	// 200 for Meshtastic-targeted codecs, 0 for JSON/CBOR.
	MaxBytes() int

	// Encode serializes a Record. Returns ErrTooLarge if the result
	// would exceed MaxBytes — the caller decides whether to drop
	// fields or fall back to a different codec.
	Encode(r Record) ([]byte, error)

	// Decode parses bytes into a Record. Returns an error for
	// unrecoverable corruption. Lossy decoding (e.g. truncated host)
	// MUST succeed and report loss via metrics, not errors.
	Decode(data []byte) (Record, error)
}

// ErrTooLarge is returned by Encode when the result exceeds MaxBytes.
// The caller should retry with fewer files, a shorter claim, or a
// different codec entirely.
var ErrTooLarge = errors.New("encoded payload exceeds codec MaxBytes budget")

// ErrCorrupt is returned by Decode when the input is unrecoverable.
// Truncation, missing fields, and version mismatches return this.
var ErrCorrupt = errors.New("encoded payload is corrupt or unrecognized")

// All returns the canonical list of research codecs in registration
// order. The harness iterates this to compare every codec against the
// same input distribution.
func All() []Codec {
	return []Codec{
		JSON{},
		Pipe{},
		CBOR{},
		Varint{},
		NewDict(),
		Bad{},
	}
}
