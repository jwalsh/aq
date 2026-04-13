package harness

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
)

// Observer is the read-only meta-agent for the harness. It subscribes
// to the bus, decodes everything it sees with every codec, and computes
// the probabilistic view: size distributions, identity attribution
// rate, decode survival rate, and invariant violation counts.
//
// Critically: the Observer never writes anything to the bus. It is
// the bridge to OTel — the same role one LoRa-equipped node would
// play in a LAN-less deployment.
type Observer struct {
	bus    *Bus
	codecs []codecs.Codec

	mu          sync.Mutex
	sizes       map[string][]int // codec -> bytes seen
	decodeOK    map[string]int64 // codec -> successful decodes
	decodeFail  map[string]int64 // codec -> failed decodes
	identityOK  map[string]int64 // codec -> identity preserved
	identityErr map[string]int64 // codec -> identity lost (truncation, desync)
	violations  map[string]int64 // invariant -> count
	envelopes   atomic.Int64     // total envelopes processed
	desyncs     atomic.Int64     // dict desync detections
}

// NewObserver constructs an Observer subscribed to all codecs.
func NewObserver(bus *Bus) *Observer {
	cs := codecs.All()
	return &Observer{
		bus:         bus,
		codecs:      cs,
		sizes:       make(map[string][]int),
		decodeOK:    make(map[string]int64),
		decodeFail:  make(map[string]int64),
		identityOK:  make(map[string]int64),
		identityErr: make(map[string]int64),
		violations:  make(map[string]int64),
	}
}

// Run subscribes to the bus and processes envelopes until stop closes.
// All work happens in this goroutine — no concurrent access to the
// internal maps, so no locking on the hot path.
func (o *Observer) Run(stop <-chan struct{}) {
	sub := o.bus.Subscribe()
	for {
		select {
		case <-stop:
			return
		case env, ok := <-sub:
			if !ok {
				return
			}
			o.process(env)
		}
	}
}

// process is the per-envelope hot path.
func (o *Observer) process(env Envelope) {
	o.envelopes.Add(1)

	// Re-encode with every codec to get a comparable size for the
	// SAME logical record. This is the apples-to-apples comparison
	// that "I sent this in pipe so I only know pipe's size" cannot give.
	for _, c := range o.codecs {
		data, err := c.Encode(env.Record)
		if err == nil {
			o.mu.Lock()
			o.sizes[c.Name()] = append(o.sizes[c.Name()], len(data))
			o.mu.Unlock()
		}
	}

	// Then decode the actual wire bytes — this measures the codec
	// the agent actually used, not the alternatives.
	originalCodec := o.findCodec(env.Codec)
	if originalCodec == nil {
		o.bumpInv("unknown_codec")
		return
	}
	got, err := originalCodec.Decode(env.WireBytes)
	if err != nil {
		o.mu.Lock()
		o.decodeFail[env.Codec]++
		o.mu.Unlock()
		if !errors.Is(err, codecs.ErrCorrupt) {
			o.bumpInv("decode_unexpected_error")
		}
		return
	}
	o.mu.Lock()
	o.decodeOK[env.Codec]++
	o.mu.Unlock()

	// Identity attribution check (the v3 mandate).
	wantHost := env.Record.Host
	wantUser := env.Record.User
	if env.Codec == "pipe" {
		// Pipe truncates to 8 chars — accept that as canonical loss.
		if len(wantHost) > 8 {
			wantHost = wantHost[:8]
		}
		if len(wantUser) > 8 {
			wantUser = wantUser[:8]
		}
	}
	if got.Host == wantHost && got.User == wantUser {
		o.mu.Lock()
		o.identityOK[env.Codec]++
		o.mu.Unlock()
	} else {
		o.mu.Lock()
		o.identityErr[env.Codec]++
		o.mu.Unlock()
		o.bumpInv("identity_attribution_lost")
	}

	// Dict desync detection — Dict marks decoded records with a
	// "[DESYNC]" prefix on Claim when the genHash mismatches.
	if env.Codec == "dict" && len(got.Claim) > 9 && got.Claim[:9] == "[DESYNC] " {
		o.desyncs.Add(1)
	}

	// Invariant: V should be 3 for v3 messages
	if env.Record.V == 3 && got.V != 3 && env.Codec != "pipe" {
		// pipe doesn't carry V explicitly — it's implied by version "3" prefix
		o.bumpInv("v_field_lost")
	}

	// Invariant: phase must be a valid char
	if got.Phase != "c" && got.Phase != "p" && got.Phase != "r" && got.Phase != "n" && got.Phase != "" {
		o.bumpInv("invalid_phase_char")
	}

	// Invariant: timestamp must not be wildly in the future
	if got.Ts > time.Now().Unix()+300 {
		o.bumpInv("ts_far_future")
	}
}

func (o *Observer) findCodec(name string) codecs.Codec {
	for _, c := range o.codecs {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func (o *Observer) bumpInv(name string) {
	o.mu.Lock()
	o.violations[name]++
	o.mu.Unlock()
}

// Report is a snapshot of observer state suitable for human or
// programmatic consumption.
type Report struct {
	TotalEnvelopes int64
	CodecStats     []CodecReport
	Violations     map[string]int64
	Desyncs        int64
}

// CodecReport is per-codec aggregated stats.
type CodecReport struct {
	Name             string
	N                int
	BytesP50         int
	BytesP95         int
	BytesP99         int
	BytesMean        float64
	BytesEntropyBits float64
	DecodeOK         int64
	DecodeFail       int64
	IdentityOK       int64
	IdentityErr      int64
}

// Report computes the current state of the observer.
func (o *Observer) Report() Report {
	o.mu.Lock()
	defer o.mu.Unlock()

	r := Report{
		TotalEnvelopes: o.envelopes.Load(),
		Desyncs:        o.desyncs.Load(),
		Violations:     make(map[string]int64),
	}
	for k, v := range o.violations {
		r.Violations[k] = v
	}

	for _, c := range o.codecs {
		name := c.Name()
		sizes := o.sizes[name]
		cr := CodecReport{
			Name:        name,
			N:           len(sizes),
			DecodeOK:    o.decodeOK[name],
			DecodeFail:  o.decodeFail[name],
			IdentityOK:  o.identityOK[name],
			IdentityErr: o.identityErr[name],
		}
		if len(sizes) > 0 {
			sorted := append([]int(nil), sizes...)
			sort.Ints(sorted)
			cr.BytesP50 = percentile(sorted, 0.50)
			cr.BytesP95 = percentile(sorted, 0.95)
			cr.BytesP99 = percentile(sorted, 0.99)
			cr.BytesMean = mean(sorted)
			cr.BytesEntropyBits = entropy(sorted)
		}
		r.CodecStats = append(r.CodecStats, cr)
	}
	return r
}

// percentile returns the p-th percentile of a sorted int slice.
func percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func mean(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

// entropy returns the Shannon entropy in bits of the size distribution.
// Higher entropy = more variability in encoded sizes (a property of the
// codec's sensitivity to input variation).
func entropy(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	counts := make(map[int]int)
	for _, x := range xs {
		counts[x]++
	}
	n := float64(len(xs))
	var h float64
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// String produces a human-readable report.
func (r Report) String() string {
	out := fmt.Sprintf("=== HARNESS REPORT ===\nenvelopes: %d  desyncs: %d\n\n", r.TotalEnvelopes, r.Desyncs)
	out += fmt.Sprintf("%-8s %5s %5s %5s %5s %7s %5s %6s %6s %6s\n",
		"codec", "N", "p50", "p95", "p99", "mean", "Hbit", "decok", "decer", "idok")
	for _, c := range r.CodecStats {
		out += fmt.Sprintf("%-8s %5d %5d %5d %5d %7.1f %5.2f %6d %6d %6d\n",
			c.Name, c.N, c.BytesP50, c.BytesP95, c.BytesP99, c.BytesMean, c.BytesEntropyBits,
			c.DecodeOK, c.DecodeFail, c.IdentityOK)
	}
	if len(r.Violations) > 0 {
		out += "\nviolations:\n"
		for k, v := range r.Violations {
			out += fmt.Sprintf("  %-30s %d\n", k, v)
		}
	}
	return out
}
