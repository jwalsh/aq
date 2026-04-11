package codecs

import (
	"encoding/base32"
	"fmt"
	"strings"
)

// Bad is the deliberately ill-conceived codec — included as a control.
//
// Floor-of-the-dialectic: a codec engineered to demonstrate every wrong
// answer at once, so the BugBash demo has a baseline for "what bad
// looks like." If your codec performs *worse* than Bad, your codec is
// not a codec.
//
// Design crimes:
//
//  1. Base32-encoded JSON. Pays the JSON tax, then pays a 1.6x base32
//     tax on top. ~1.6x larger than plain JSON for no reason.
//  2. Hidden flags in case-folding: uppercase = phase=p, lowercase = phase=c.
//     Decoder must scan the entire payload looking for case patterns.
//  3. No version field. Future evolution requires sniffing.
//  4. Payload prefix is the literal string "BAD!" so it's grep-friendly
//     but adds 4 wasted bytes.
//  5. No length information — relies on base32 padding alone.
//  6. Self-referential failure: if Encode produces > MaxBytes, Decode
//     of the truncated bytes still "succeeds" with garbage data.
//
// Use this in the harness to demonstrate the *floor*. Anything above
// it is a real codec; anything that performs worse is a bug.
type Bad struct{}

func (Bad) Name() string  { return "bad" }
func (Bad) MaxBytes() int { return 200 }

func (b Bad) Encode(r Record) ([]byte, error) {
	// Step 1: produce JSON (already wasteful for this purpose).
	jbytes, err := JSON{}.Encode(r)
	if err != nil {
		return nil, err
	}

	// Step 2: base32-encode it (1.6x size penalty).
	enc := base32.StdEncoding.EncodeToString(jbytes)

	// Step 3: smuggle phase via case-folding. Uppercase = proof,
	// lowercase = anything else. Decoder has to guess.
	if r.Phase == "p" {
		enc = strings.ToUpper(enc)
	} else {
		enc = strings.ToLower(enc)
	}

	// Step 4: prepend the BAD! sentinel.
	out := append([]byte("BAD!"), []byte(enc)...)

	// Step 5: deliberately allow oversized output. The harness will
	// catch this as ErrTooLarge but Decode "succeeds" anyway.
	if len(out) > b.MaxBytes() {
		return out, ErrTooLarge
	}
	return out, nil
}

func (Bad) Decode(data []byte) (Record, error) {
	if len(data) < 5 || string(data[:4]) != "BAD!" {
		return Record{}, fmt.Errorf("%w: bad missing sentinel", ErrCorrupt)
	}
	body := data[4:]

	// Recover phase from case-folding before normalizing.
	upperCount, lowerCount := 0, 0
	for _, c := range body {
		if c >= 'A' && c <= 'Z' {
			upperCount++
		} else if c >= 'a' && c <= 'z' {
			lowerCount++
		}
	}
	phaseGuess := "c"
	if upperCount > lowerCount {
		phaseGuess = "p"
	}

	// Base32 is case-insensitive but stdlib expects uppercase.
	jbytes, err := base32.StdEncoding.DecodeString(strings.ToUpper(string(body)))
	if err != nil {
		return Record{}, fmt.Errorf("%w: bad base32 decode: %v", ErrCorrupt, err)
	}

	r, err := JSON{}.Decode(jbytes)
	if err != nil {
		return Record{}, err
	}

	// Override phase from the case-folding signal — yes, this is bad.
	// We only override if the JSON didn't have one, so we don't make it
	// even worse than it already is.
	if r.Phase == "" {
		r.Phase = phaseGuess
	}
	return r, nil
}
