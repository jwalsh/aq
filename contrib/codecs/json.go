package codecs

import (
	"encoding/json"
)

// JSON is the v3 self-describing JSON codec — the human-readable baseline.
//
// Thesis position in the codec dialectic: maximum legibility, no schema
// required, every field present and named. Trades bytes for clarity.
//
// Wire format matches the v3 spec exactly: short keys (cid, claim),
// single-char phase/status, mandatory host/user, version field "v":3.
type JSON struct{}

// jsonWire is the on-wire shape for the JSON codec.
type jsonWire struct {
	V        int      `json:"v"`
	Host     string   `json:"host"`
	User     string   `json:"user"`
	Agent    string   `json:"agent"`
	Wt       string   `json:"worktree"`
	CID      string   `json:"cid"`
	Claim    string   `json:"claim,omitempty"`
	Phase    string   `json:"phase"`
	Status   string   `json:"status"`
	Files    []string `json:"files,omitempty"`
	Ts       int64    `json:"ts"`
	TTL      int      `json:"ttl"`
	ID       string   `json:"id"`
}

func (JSON) Name() string { return "json" }

// MaxBytes is 0: JSON is the baseline, no constraint. Whatever it costs,
// it costs. The harness reports this as the upper bound.
func (JSON) MaxBytes() int { return 0 }

func (JSON) Encode(r Record) ([]byte, error) {
	return json.Marshal(jsonWire{
		V:      3,
		Host:   r.Host,
		User:   r.User,
		Agent:  r.Agent,
		Wt:     r.Worktree,
		CID:    r.CID,
		Claim:  r.Claim,
		Phase:  r.Phase,
		Status: r.Status,
		Files:  r.Files,
		Ts:     r.Ts,
		TTL:    r.TTL,
		ID:     r.ID,
	})
}

func (JSON) Decode(data []byte) (Record, error) {
	var w jsonWire
	if err := json.Unmarshal(data, &w); err != nil {
		return Record{}, ErrCorrupt
	}
	return Record{
		V:        w.V,
		Host:     w.Host,
		User:     w.User,
		Agent:    w.Agent,
		Worktree: w.Wt,
		CID:      w.CID,
		Claim:    w.Claim,
		Phase:    w.Phase,
		Status:   w.Status,
		Files:    w.Files,
		Ts:       w.Ts,
		TTL:      w.TTL,
		ID:       w.ID,
	}, nil
}
