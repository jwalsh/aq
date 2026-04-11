package codecs

import (
	"encoding/binary"
	"fmt"
)

// Varint is the schema-required minimum-bytes binary codec.
//
// Antithesis position in the dialectic: opaque, fixed-schema, no
// self-description. Maximum byte savings short of dictionary coding.
//
// Wire format (big-endian, mostly):
//
//	[1B  magic 0xAA]
//	[1B  version  3]
//	[1B  phase char] [1B status char]
//	[varint ts_delta]   (since 2026-01-01)
//	[varint ttl]
//	[lp host]   (1 byte length, then bytes)
//	[lp user]
//	[lp cid]
//	[lp agent]
//	[lp worktree]
//	[lp claim]
//	[lp id]
//	[1B file count] [lp file]*
//
// "lp" = length-prefixed, 1-byte length (0-255). Strings >255 bytes
// get truncated — the harness measures truncation rate.
//
// Failure mode: schema drift between encoder and decoder versions.
// There is no version negotiation. The 1B version field at offset 1
// is the only safety net — receivers MUST check it.
type Varint struct{}

const varintMagic = 0xAA
const varintVersion = 3

func (Varint) Name() string  { return "varint" }
func (Varint) MaxBytes() int { return 200 }

func (v Varint) Encode(r Record) ([]byte, error) {
	buf := make([]byte, 0, 128)
	buf = append(buf, varintMagic, varintVersion)
	buf = append(buf, byteOrZ(r.Phase, 'c'), byteOrZ(r.Status, 'a'))

	tsBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutVarint(tsBuf, r.Ts-epochBase)
	buf = append(buf, tsBuf[:n]...)

	ttlBuf := make([]byte, binary.MaxVarintLen64)
	n = binary.PutUvarint(ttlBuf, uint64(r.TTL))
	buf = append(buf, ttlBuf[:n]...)

	for _, s := range []string{r.Host, r.User, r.CID, r.Agent, r.Worktree, r.Claim, r.ID} {
		buf = appendLP(buf, s)
	}

	if len(r.Files) > 255 {
		return nil, fmt.Errorf("varint: %d files exceeds 255 limit", len(r.Files))
	}
	buf = append(buf, byte(len(r.Files)))
	for _, f := range r.Files {
		buf = appendLP(buf, f)
	}

	if len(buf) > v.MaxBytes() {
		return nil, ErrTooLarge
	}
	return buf, nil
}

func (Varint) Decode(data []byte) (Record, error) {
	if len(data) < 4 {
		return Record{}, fmt.Errorf("%w: varint payload too short (%d bytes)", ErrCorrupt, len(data))
	}
	if data[0] != varintMagic {
		return Record{}, fmt.Errorf("%w: varint bad magic 0x%02x", ErrCorrupt, data[0])
	}
	if data[1] != varintVersion {
		return Record{}, fmt.Errorf("%w: varint version %d not supported", ErrCorrupt, data[1])
	}

	r := Record{V: int(data[1])}
	r.Phase = string(data[2])
	r.Status = string(data[3])
	off := 4

	tsDelta, n := binary.Varint(data[off:])
	if n <= 0 {
		return Record{}, fmt.Errorf("%w: varint bad ts", ErrCorrupt)
	}
	r.Ts = tsDelta + epochBase
	off += n

	ttl, n := binary.Uvarint(data[off:])
	if n <= 0 {
		return Record{}, fmt.Errorf("%w: varint bad ttl", ErrCorrupt)
	}
	r.TTL = int(ttl)
	off += n

	var err error
	r.Host, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.User, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.CID, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.Agent, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.Worktree, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.Claim, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.ID, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}

	if off >= len(data) {
		return r, nil
	}
	fileCount := int(data[off])
	off++
	r.Files = make([]string, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		var f string
		f, off, err = readLP(data, off)
		if err != nil {
			return Record{}, err
		}
		r.Files = append(r.Files, f)
	}
	return r, nil
}

// appendLP appends a length-prefixed string. Strings longer than 255
// bytes are truncated; longer than that simply does not fit the format.
func appendLP(buf []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	buf = append(buf, byte(len(s)))
	buf = append(buf, s...)
	return buf
}

func readLP(data []byte, off int) (string, int, error) {
	if off >= len(data) {
		return "", 0, fmt.Errorf("%w: varint truncated string at offset %d", ErrCorrupt, off)
	}
	n := int(data[off])
	off++
	if off+n > len(data) {
		return "", 0, fmt.Errorf("%w: varint string length %d overruns buffer", ErrCorrupt, n)
	}
	return string(data[off : off+n]), off + n, nil
}

func byteOrZ(s string, d byte) byte {
	if len(s) == 0 {
		return d
	}
	return s[0]
}
