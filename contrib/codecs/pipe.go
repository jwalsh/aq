package codecs

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Pipe is the positional pipe-delimited compact codec — what aq currently
// uses for Meshtastic LoRa transport.
//
// Synthesis position in the dialectic: small, ASCII-printable, no schema
// negotiation, but brittle to field reordering. Sacrifices structure for
// bytes.
//
// v3 wire format:
//
//	3|<host>|<user>|<phase>|<status>|<cid>|<agent_short>|<ts_delta>[|<files>]
//
// ts_delta is seconds since 2026-01-01 (epochBase below) so the field
// stays compact through 2029.
type Pipe struct{}

// epochBase is 2026-01-01T00:00:00Z. Choosing a recent base buys us
// ~3-4 bytes per timestamp vs raw unix seconds, valid through 2029.
const epochBase int64 = 1767225600

func (Pipe) Name() string     { return "pipe" }
func (Pipe) MaxBytes() int    { return 200 } // Meshtastic AES-256-CTR effective payload
func (Pipe) maxIdentBytes() int { return 8 }

func (p Pipe) Encode(r Record) ([]byte, error) {
	host := truncate(orQ(r.Host), p.maxIdentBytes())
	user := truncate(orQ(r.User), p.maxIdentBytes())
	phase := orC(r.Phase, "c")
	status := orC(r.Status, "a")

	// Truncate agent to last 20 chars for wire efficiency.
	agentShort := r.Agent
	if len(agentShort) > 20 {
		agentShort = agentShort[len(agentShort)-20:]
	}

	tsDelta := strconv.FormatInt(r.Ts-epochBase, 10)

	parts := []string{"3", host, user, phase, status, r.CID, agentShort, tsDelta}

	if len(r.Files) > 0 {
		bases := make([]string, len(r.Files))
		for i, f := range r.Files {
			bases[i] = filepath.Base(f)
		}
		parts = append(parts, strings.Join(bases, ","))
	}

	encoded := strings.Join(parts, "|")
	if len(encoded) > p.MaxBytes() {
		// Drop files first, retry once.
		parts = parts[:8]
		encoded = strings.Join(parts, "|")
		if len(encoded) > p.MaxBytes() {
			return nil, ErrTooLarge
		}
	}
	return []byte(encoded), nil
}

func (Pipe) Decode(data []byte) (Record, error) {
	parts := strings.Split(string(data), "|")
	if len(parts) < 8 {
		return Record{}, fmt.Errorf("%w: pipe needs >=8 fields, got %d", ErrCorrupt, len(parts))
	}
	if parts[0] != "3" {
		return Record{}, fmt.Errorf("%w: pipe version %q not supported", ErrCorrupt, parts[0])
	}

	tsDelta, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return Record{}, fmt.Errorf("%w: bad ts_delta: %v", ErrCorrupt, err)
	}

	worktree := parts[6]
	if idx := strings.LastIndex(worktree, "/"); idx >= 0 {
		worktree = worktree[idx+1:]
	}

	r := Record{
		V:        3,
		Host:     parts[1],
		User:     parts[2],
		Phase:    parts[3],
		Status:   parts[4],
		CID:      parts[5],
		Agent:    parts[6],
		Worktree: worktree,
		Ts:       tsDelta + epochBase,
		TTL:      3600,
	}

	if len(parts) > 8 && parts[8] != "" {
		r.Files = strings.Split(parts[8], ",")
	}
	return r, nil
}

// orQ returns "?" if s is empty, otherwise s. Compact codecs use "?"
// as the null sentinel since empty positional fields would shift parse.
func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// orC returns d if s is empty.
func orC(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// truncate clips s to n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
