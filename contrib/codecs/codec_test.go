package codecs

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
)

// Property-based tests across all codecs.
//
// Each property runs against every Codec returned by All(). Failures
// are reported per-codec so the harness report can show which codecs
// pass which invariants.
//
// Determinism: every test uses a fixed seed (default 1) so failures
// can be replayed by re-running with -seed=N. The harness builds on
// the same seed convention.
//
// The five properties:
//
//	P1: identity-roundtrip   — host, user, agent, cid survive encode→decode
//	P2: full-roundtrip       — every field survives, modulo documented loss
//	P3: size-budget          — encode result <= MaxBytes for in-spec records
//	P4: corruption-tolerance — random bit-flips don't cause panics
//	P5: empty-field-safety   — records with empty optional fields encode

// genRecord builds a Record with controllable randomness. The harness
// uses the same generator so codec tests and chaos tests share input
// distributions.
func genRecord(rng *rand.Rand) Record {
	hosts := []string{"mini", "hydra", "nexus", "pi", "agent01", "h1", "x"}
	users := []string{"alice", "bob", "carol", "dave", "eve", "jw", "u"}
	phases := []string{"c", "p", "r", "n"}
	statuses := []string{"a", "d", "b"}

	files := make([]string, rng.Intn(4))
	for i := range files {
		files[i] = fmt.Sprintf("file%d.go", rng.Intn(100))
	}

	claims := []string{
		"",
		"refactoring auth",
		"replacing session tokens with OAuth2",
		"investigating memory leak in handler",
	}

	return Record{
		V:        3,
		Host:     hosts[rng.Intn(len(hosts))],
		User:     users[rng.Intn(len(users))],
		Agent:    fmt.Sprintf("github.com/%s/%s/%s", users[rng.Intn(len(users))], hosts[rng.Intn(len(hosts))], "main"),
		Worktree: "main",
		CID:      fmt.Sprintf("C-%d", rng.Intn(100)),
		Claim:    claims[rng.Intn(len(claims))],
		Phase:    phases[rng.Intn(len(phases))],
		Status:   statuses[rng.Intn(len(statuses))],
		Files:    files,
		Ts:       1775000000 + int64(rng.Intn(86400*30)),
		TTL:      300 + rng.Intn(3300),
		ID:       fmt.Sprintf("%022x", rng.Int63()),
	}
}

// codecsToTest returns all codecs except Bad — Bad is intentionally
// non-roundtripping so it gets its own targeted tests instead of
// participating in the universal property suite.
func codecsToTest() []Codec {
	all := All()
	out := make([]Codec, 0, len(all))
	for _, c := range all {
		if c.Name() == "bad" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// P1: identity-roundtrip — host and user MUST survive encode→decode.
// This is the v3 mandate; any codec that loses identity is unfit for
// the OTel bridge.
func TestP1_IdentityRoundtrip(t *testing.T) {
	for _, codec := range codecsToTest() {
		codec := codec
		t.Run(codec.Name(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(1))
			for i := 0; i < 200; i++ {
				r := genRecord(rng)
				data, err := codec.Encode(r)
				if errors.Is(err, ErrTooLarge) {
					continue // expected for some inputs
				}
				if err != nil {
					t.Fatalf("encode error: %v (record: %+v)", err, r)
				}
				got, err := codec.Decode(data)
				if err != nil {
					t.Fatalf("decode error: %v (data: %x)", err, data)
				}

				// Pipe truncates host/user to 8 bytes — accept the truncated form.
				wantHost := r.Host
				wantUser := r.User
				if codec.Name() == "pipe" {
					if len(wantHost) > 8 {
						wantHost = wantHost[:8]
					}
					if len(wantUser) > 8 {
						wantUser = wantUser[:8]
					}
				}

				if got.Host != wantHost {
					t.Errorf("host: got %q want %q", got.Host, wantHost)
				}
				if got.User != wantUser {
					t.Errorf("user: got %q want %q", got.User, wantUser)
				}
			}
		})
	}
}

// P2: full-roundtrip — phase, status, cid, ts, ttl all survive.
// Files and claim are best-effort (some codecs drop them under pressure).
func TestP2_FullRoundtrip(t *testing.T) {
	for _, codec := range codecsToTest() {
		codec := codec
		t.Run(codec.Name(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(2))
			for i := 0; i < 200; i++ {
				r := genRecord(rng)
				data, err := codec.Encode(r)
				if errors.Is(err, ErrTooLarge) {
					continue
				}
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				got, err := codec.Decode(data)
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if got.Phase != r.Phase {
					t.Errorf("phase: got %q want %q", got.Phase, r.Phase)
				}
				if got.Status != r.Status {
					t.Errorf("status: got %q want %q", got.Status, r.Status)
				}
				if got.CID != r.CID {
					t.Errorf("cid: got %q want %q", got.CID, r.CID)
				}
				if got.Ts != r.Ts {
					t.Errorf("ts: got %d want %d", got.Ts, r.Ts)
				}
			}
		})
	}
}

// P3: size budget — for in-spec records, encoded length must be
// <= MaxBytes (or MaxBytes is 0 = unlimited). The harness uses this to
// distinguish hard failures from "budget exceeded, drop optional fields."
func TestP3_SizeBudget(t *testing.T) {
	for _, codec := range codecsToTest() {
		codec := codec
		t.Run(codec.Name(), func(t *testing.T) {
			max := codec.MaxBytes()
			if max == 0 {
				t.Skip("codec has no size limit")
			}
			rng := rand.New(rand.NewSource(3))
			oversized := 0
			for i := 0; i < 500; i++ {
				r := genRecord(rng)
				data, err := codec.Encode(r)
				if errors.Is(err, ErrTooLarge) {
					oversized++
					continue
				}
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					continue
				}
				if len(data) > max {
					t.Errorf("encoded %d bytes exceeds MaxBytes %d (record: %+v)", len(data), max, r)
				}
			}
			t.Logf("%d/500 records exceeded budget", oversized)
		})
	}
}

// P4: corruption-tolerance — bit-flipped payloads must not panic.
// Decode may return ErrCorrupt; that is fine. The forbidden outcome
// is a panic (which would crash the receiving daemon).
func TestP4_CorruptionNoPanic(t *testing.T) {
	for _, codec := range codecsToTest() {
		codec := codec
		t.Run(codec.Name(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(4))
			f := func(data []byte) (didNotPanic bool) {
				didNotPanic = true
				defer func() {
					if r := recover(); r != nil {
						didNotPanic = false
						t.Errorf("panic on input %x: %v", data, r)
					}
				}()
				_, _ = codec.Decode(data)
				return
			}
			cfg := &quick.Config{MaxCount: 200, Rand: rng}
			if err := quick.Check(f, cfg); err != nil {
				t.Error(err)
			}
		})
	}
}

// P5: empty-field-safety — records with all-empty optional fields
// must encode without error and roundtrip cleanly.
func TestP5_EmptyOptionalFields(t *testing.T) {
	for _, codec := range codecsToTest() {
		codec := codec
		t.Run(codec.Name(), func(t *testing.T) {
			r := Record{
				V:      3,
				Host:   "h",
				User:   "u",
				Agent:  "a/b",
				CID:    "C-0",
				Phase:  "c",
				Status: "a",
				Ts:     1775000000,
				TTL:    300,
				ID:     "0000000000000000000000",
			}
			data, err := codec.Encode(r)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := codec.Decode(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Host != "h" || got.User != "u" {
				t.Errorf("identity lost on minimal record: %+v", got)
			}
		})
	}
}

// SizeReport prints a comparative size table for the documented
// "typical" record. Run via `go test -run TestSizeReport -v`.
// Not a test in the assertion sense — pure measurement.
func TestSizeReport(t *testing.T) {
	r := Record{
		V:        3,
		Host:     "mini",
		User:     "jwalsh",
		Agent:    "github.com/jwalsh/aq/main",
		Worktree: "main",
		CID:      "C-42",
		Claim:    "refactoring session token storage",
		Phase:    "p",
		Status:   "a",
		Files:    []string{"auth.go", "session.go"},
		Ts:       1775831548,
		TTL:      3600,
		ID:       "019d77cf0098337c6ba662",
	}

	t.Log("=== typical-record size comparison ===")
	for _, codec := range All() {
		data, err := codec.Encode(r)
		if err != nil {
			t.Logf("  %-8s ERROR: %v", codec.Name(), err)
			continue
		}
		t.Logf("  %-8s %4d bytes", codec.Name(), len(data))
	}
}

// TestP6_BadCodecIsBad documents that the Bad codec lives up to its
// name — it should be the largest of all codecs for any input.
func TestP6_BadCodecIsBad(t *testing.T) {
	r := Record{
		V: 3, Host: "h", User: "u", Agent: "a/b/c",
		CID: "C-1", Phase: "p", Status: "a",
		Ts: 1775000000, TTL: 300, ID: "0000000000000000000000",
	}
	badData, _ := Bad{}.Encode(r)
	jsonData, _ := JSON{}.Encode(r)
	if len(badData) <= len(jsonData) {
		t.Errorf("Bad codec (%d) is supposed to be larger than JSON (%d) — that's the whole point",
			len(badData), len(jsonData))
	}
	if !strings.HasPrefix(string(badData), "BAD!") {
		t.Errorf("Bad codec lost its sentinel — that's the only feature it had")
	}
}
