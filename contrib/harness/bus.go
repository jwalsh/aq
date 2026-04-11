package harness

import (
	"sync"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
)

// Envelope is one message on the in-process bus. It carries both the
// original Record (ground truth) and the wire bytes (what observers
// would actually see). The discrepancy between the two is what we
// measure under chaos.
type Envelope struct {
	AgentID   string
	Cohort    Cohort
	Codec     string
	Sent      time.Time
	Record    codecs.Record // ground truth
	WireBytes []byte        // what was actually transmitted
}

// Bus is the in-process pub/sub for the harness. It keeps a bounded
// ring buffer of envelopes per codec so observers can compute metrics
// without blocking publishers.
//
// This is the "filesystem" of the harness, but without disk I/O —
// we want to measure protocol behavior, not APFS throughput.
type Bus struct {
	mu        sync.RWMutex
	envelopes []Envelope
	cap       int
	subs      []chan Envelope
}

// NewBus returns a Bus retaining the most recent `cap` envelopes.
// Subscribers receive new envelopes via channels (best-effort: a slow
// subscriber drops messages, just like UDP).
func NewBus(cap int) *Bus {
	return &Bus{cap: cap}
}

// Publish broadcasts an envelope. Always non-blocking.
func (b *Bus) Publish(env Envelope) {
	b.mu.Lock()
	b.envelopes = append(b.envelopes, env)
	if len(b.envelopes) > b.cap {
		b.envelopes = b.envelopes[len(b.envelopes)-b.cap:]
	}
	subs := append([]chan Envelope(nil), b.subs...)
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- env:
		default:
			// Drop — that's a real condition we want to measure.
		}
	}
}

// Subscribe returns a channel for new envelopes. Buffered to absorb bursts.
func (b *Bus) Subscribe() <-chan Envelope {
	ch := make(chan Envelope, 256)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// Snapshot returns all envelopes currently in the ring. Used by the
// observer's periodic invariant scan.
func (b *Bus) Snapshot() []Envelope {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Envelope, len(b.envelopes))
	copy(out, b.envelopes)
	return out
}

// Len reports current ring depth.
func (b *Bus) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.envelopes)
}
