package codecs

import (
	cbor "github.com/fxamacker/cbor/v2"
)

// CBOR is the v3 self-describing binary codec via RFC 8949.
//
// Thesis-binary position: same semantics as JSON (every field named,
// version explicit), but binary-encoded with shorter map keys via the
// "FieldName" tags. Used in production by COSE, WebAuthn, IPLD.
//
// Expected size advantage over JSON: ~30-40% via binary integers,
// length-prefixed strings, and byte-string maps. The harness will
// measure the actual delta.
//
// Failure mode: a single bit-flip in a length prefix corrupts the
// remainder of the message — the harness's bitflip property test
// quantifies this resilience cost vs JSON's ability to skip-and-resync.
type CBOR struct{}

// cborWire uses single-letter keys to maximize the binary advantage.
// CBOR encodes string map keys as length-prefixed UTF-8, so 1-char
// keys cost 2 bytes (length=1 + 1 char) instead of 5+ for JSON.
type cborWire struct {
	V      int      `cbor:"v"`
	H      string   `cbor:"h"`             // host
	U      string   `cbor:"u"`             // user
	A      string   `cbor:"a"`             // agent
	W      string   `cbor:"w"`             // worktree
	C      string   `cbor:"c"`             // cid
	M      string   `cbor:"m,omitempty"`   // claim ("m" for "meaning")
	P      string   `cbor:"p"`             // phase
	S      string   `cbor:"s"`             // status
	F      []string `cbor:"f,omitempty"`   // files
	T      int64    `cbor:"t"`             // ts
	L      int      `cbor:"l"`             // ttl ("l" for lifetime)
	I      string   `cbor:"i"`             // id
}

func (CBOR) Name() string  { return "cbor" }
func (CBOR) MaxBytes() int { return 0 }

// encMode is constructed once with deterministic encoding so that two
// equivalent records always produce identical bytes — important for
// deterministic chaos testing.
var cborEncMode, _ = cbor.CanonicalEncOptions().EncMode()

func (CBOR) Encode(r Record) ([]byte, error) {
	w := cborWire{
		V: 3, H: r.Host, U: r.User, A: r.Agent, W: r.Worktree,
		C: r.CID, M: r.Claim, P: r.Phase, S: r.Status, F: r.Files,
		T: r.Ts, L: r.TTL, I: r.ID,
	}
	return cborEncMode.Marshal(w)
}

func (CBOR) Decode(data []byte) (Record, error) {
	var w cborWire
	if err := cbor.Unmarshal(data, &w); err != nil {
		return Record{}, ErrCorrupt
	}
	return Record{
		V: w.V, Host: w.H, User: w.U, Agent: w.A, Worktree: w.W,
		CID: w.C, Claim: w.M, Phase: w.P, Status: w.S, Files: w.F,
		Ts: w.T, TTL: w.L, ID: w.I,
	}, nil
}
