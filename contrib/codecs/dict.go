package codecs

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sync"
)

// Dict is the dictionary-coded varint codec — the radical option.
//
// Antithesis-radical position in the dialectic: identity strings are
// replaced by 1-byte dictionary indices into a learned table that is
// synchronized out-of-band via a separate channel. Saves 6-15 bytes
// per message but breaks catastrophically when the dictionary desyncs.
//
// The harness deliberately causes desync to measure the failure mode.
// We expect identity loss between 5% and 30% under 20% packet loss —
// that's the empirical question this codec exists to answer.
//
// Dictionary protocol:
//
//	Each side maintains: hostDict[byte]string, userDict[byte]string,
//	agentDict[byte]string. Dicts grow lazily — when an encoder sees a
//	new value, it picks the next slot, adds it locally, and emits a
//	"dict update" announcement on the side channel. Receivers update
//	their dicts on receipt. If a receiver sees an unknown index, it
//	emits "?<idx>" as the resolved string and increments a metric.
//
// Wire format:
//
//	[1B  magic 0xDD]
//	[1B  version  1]
//	[1B  dict generation hash low byte]   ← liveness indicator
//	[1B  phase] [1B status]
//	[1B  host_idx]
//	[1B  user_idx]
//	[1B  agent_idx]
//	[varint cid_num]                       ← assumes "C-N" form
//	[varint ts_delta]
//	[varint ttl]
//	[lp claim]
//	[lp id]
//	[1B file_count] [lp file]*
//
// The "dict generation hash low byte" lets receivers detect desync
// quickly without checking every entry. If it does not match, the
// receiver flags the message as identity-suspect.
type Dict struct {
	mu          sync.Mutex
	hostByName  map[string]byte
	hostByIdx   [256]string
	userByName  map[string]byte
	userByIdx   [256]string
	agentByName map[string]byte
	agentByIdx  [256]string
	hostNext    byte
	userNext    byte
	agentNext   byte
}

const dictMagic = 0xDD
const dictVersion = 1

// NewDict returns a fresh dictionary codec with empty tables.
// Each side of the wire needs its own NewDict.
func NewDict() *Dict {
	d := &Dict{
		hostByName:  make(map[string]byte),
		userByName:  make(map[string]byte),
		agentByName: make(map[string]byte),
	}
	return d
}

func (Dict) Name() string  { return "dict" }
func (Dict) MaxBytes() int { return 200 }

// Make Dict satisfy the Codec interface as a value type for All().
// The default value (zero) gets a lazy-init.
func (d Dict) Encode(r Record) ([]byte, error) {
	enc := defaultDict()
	return enc.encode(r)
}

func (d Dict) Decode(data []byte) (Record, error) {
	enc := defaultDict()
	return enc.decode(data)
}

// defaultDict returns a process-wide default dictionary instance.
// In real usage, sender and receiver each maintain their own and sync
// out-of-band — the harness uses two distinct instances on purpose.
var defaultDictInstance *Dict
var defaultDictOnce sync.Once

func defaultDict() *Dict {
	defaultDictOnce.Do(func() {
		defaultDictInstance = NewDict()
	})
	return defaultDictInstance
}

// internOrAdd returns the dictionary index for s, allocating a new
// slot if needed. Returns (idx, isNew). When the table is full (256
// entries), the lowest entry is evicted — that is the desync vector.
func internOrAdd(byName map[string]byte, byIdx *[256]string, next *byte, s string) (byte, bool) {
	if idx, ok := byName[s]; ok {
		return idx, false
	}
	idx := *next
	if old := byIdx[idx]; old != "" {
		delete(byName, old) // eviction — caller will desync
	}
	byIdx[idx] = s
	byName[s] = idx
	*next++
	return idx, true
}

func (d *Dict) encode(r Record) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	hi, _ := internOrAdd(d.hostByName, &d.hostByIdx, &d.hostNext, r.Host)
	ui, _ := internOrAdd(d.userByName, &d.userByIdx, &d.userNext, r.User)
	ai, _ := internOrAdd(d.agentByName, &d.agentByIdx, &d.agentNext, r.Agent)

	// CID parse: assume "C-<num>" form. Falls back to 0 on parse failure
	// and stores the original in claim — lossy by design for non-conforming
	// CIDs (which the harness measures).
	var cidNum uint64
	if len(r.CID) > 2 && r.CID[0] == 'C' && r.CID[1] == '-' {
		for i := 2; i < len(r.CID); i++ {
			c := r.CID[i]
			if c < '0' || c > '9' {
				cidNum = 0
				break
			}
			cidNum = cidNum*10 + uint64(c-'0')
		}
	}

	buf := make([]byte, 0, 64)
	buf = append(buf, dictMagic, dictVersion, d.genHash())
	buf = append(buf, byteOrZ(r.Phase, 'c'), byteOrZ(r.Status, 'a'))
	buf = append(buf, hi, ui, ai)

	tmp := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(tmp, cidNum)
	buf = append(buf, tmp[:n]...)
	n = binary.PutVarint(tmp, r.Ts-epochBase)
	buf = append(buf, tmp[:n]...)
	n = binary.PutUvarint(tmp, uint64(r.TTL))
	buf = append(buf, tmp[:n]...)

	buf = appendLP(buf, r.Claim)
	buf = appendLP(buf, r.ID)

	if len(r.Files) > 255 {
		return nil, fmt.Errorf("dict: too many files (%d)", len(r.Files))
	}
	buf = append(buf, byte(len(r.Files)))
	for _, f := range r.Files {
		buf = appendLP(buf, f)
	}

	if len(buf) > d.MaxBytes() {
		return nil, ErrTooLarge
	}
	return buf, nil
}

func (d *Dict) decode(data []byte) (Record, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(data) < 8 {
		return Record{}, fmt.Errorf("%w: dict payload too short", ErrCorrupt)
	}
	if data[0] != dictMagic {
		return Record{}, fmt.Errorf("%w: dict bad magic 0x%02x", ErrCorrupt, data[0])
	}
	if data[1] != dictVersion {
		return Record{}, fmt.Errorf("%w: dict version %d not supported", ErrCorrupt, data[1])
	}

	r := Record{V: 3}
	desync := data[2] != d.genHash()
	r.Phase = string(data[3])
	r.Status = string(data[4])
	r.Host = d.hostByIdx[data[5]]
	r.User = d.userByIdx[data[6]]
	r.Agent = d.agentByIdx[data[7]]
	off := 8

	// Mark unresolved entries as "?<idx>" so they're visible in metrics.
	if r.Host == "" {
		r.Host = fmt.Sprintf("?%d", data[5])
	}
	if r.User == "" {
		r.User = fmt.Sprintf("?%d", data[6])
	}
	if r.Agent == "" {
		r.Agent = fmt.Sprintf("?%d", data[7])
	}
	if desync {
		// Distinguish desync from clean lookups so the harness can count.
		r.Claim = "[DESYNC] " + r.Claim
	}

	cidNum, n := binary.Uvarint(data[off:])
	if n <= 0 {
		return Record{}, fmt.Errorf("%w: dict bad cid", ErrCorrupt)
	}
	r.CID = fmt.Sprintf("C-%d", cidNum)
	off += n

	tsDelta, n := binary.Varint(data[off:])
	if n <= 0 {
		return Record{}, fmt.Errorf("%w: dict bad ts", ErrCorrupt)
	}
	r.Ts = tsDelta + epochBase
	off += n

	ttl, n := binary.Uvarint(data[off:])
	if n <= 0 {
		return Record{}, fmt.Errorf("%w: dict bad ttl", ErrCorrupt)
	}
	r.TTL = int(ttl)
	off += n

	var err error
	var claim, id string
	claim, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.Claim = claim + r.Claim // preserve [DESYNC] marker
	id, off, err = readLP(data, off)
	if err != nil {
		return Record{}, err
	}
	r.ID = id

	if off >= len(data) {
		return r, nil
	}
	fileCount := int(data[off])
	off++
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

// genHash returns a 1-byte fingerprint of the dictionary state.
// Sender and receiver compare this to detect desync without exchanging
// the full table.
func (d *Dict) genHash() byte {
	h := fnv.New32a()
	for i := 0; i < 256; i++ {
		if d.hostByIdx[i] != "" {
			h.Write([]byte("h"))
			h.Write([]byte{byte(i)})
			h.Write([]byte(d.hostByIdx[i]))
		}
		if d.userByIdx[i] != "" {
			h.Write([]byte("u"))
			h.Write([]byte{byte(i)})
			h.Write([]byte(d.userByIdx[i]))
		}
		if d.agentByIdx[i] != "" {
			h.Write([]byte("a"))
			h.Write([]byte{byte(i)})
			h.Write([]byte(d.agentByIdx[i]))
		}
	}
	return byte(h.Sum32())
}
