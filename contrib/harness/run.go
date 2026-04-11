package harness

import (
	"context"
	"hash/fnv"
	"math/rand"
	"sync"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
)

// Config controls a harness run.
type Config struct {
	// Seed is the deterministic seed string. Same seed → same chaos.
	// Use a memorable phrase like "bugbash-2026-04" for replay.
	Seed string

	// Duration is the total wall clock budget. If Iterations > 0,
	// Duration is a hard cap; if Iterations == 0, run until Duration.
	Duration time.Duration

	// Iterations is the number of ticks each agent performs. Set this
	// for deterministic replay (Antithesis-style). Set to 0 for time-
	// based execution where wall clock variance is acceptable.
	Iterations int

	// Codec is the codec all agents use for this run. Use one Run()
	// per codec to compare them apples-to-apples.
	Codec codecs.Codec

	// Profiles overrides the default 20-agent set. nil = use BuildCohorts().
	Profiles []AgentProfile
}

// Result is what a Run returns.
type Result struct {
	Seed       string
	Duration   time.Duration
	Codec      string
	AgentStats map[string]*AgentStats
	Report     Report
}

// Run executes the harness with the given config and returns the
// observer report. Deterministic given Seed (modulo wall-clock time).
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Profiles == nil {
		cfg.Profiles = BuildCohorts()
	}
	if cfg.Codec == nil {
		cfg.Codec = codecs.JSON{}
	}
	if cfg.Duration == 0 {
		cfg.Duration = 30 * time.Second
	}

	// Derive a master seed from the seed string. Each agent gets its
	// own derived PRNG so failures are reproducible from cfg.Seed alone.
	masterSeed := hashSeed(cfg.Seed)

	bus := NewBus(2048)
	observer := NewObserver(bus)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Observer first so we don't miss the early bursts.
	wg.Add(1)
	go func() {
		defer wg.Done()
		observer.Run(stop)
	}()

	stats := make(map[string]*AgentStats, len(cfg.Profiles))
	for i, profile := range cfg.Profiles {
		profile := profile
		stats[profile.ID] = &AgentStats{}

		// Per-agent PRNG seeded from master + index. Reproducible.
		agentSeed := masterSeed ^ (uint64(i+1) * 0x9E3779B97F4A7C15)
		agentRng := rand.New(rand.NewSource(int64(agentSeed)))

		ar := &agentRun{
			profile: profile,
			rng:     agentRng,
			bus:     bus,
			codec:   cfg.Codec,
			stats:   stats[profile.ID],
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			ar.run(stop, cfg.Iterations)
		}()
	}

	// Wait for: context cancel, duration timeout, OR all agents done.
	// In fixed-iteration mode, agents finish in milliseconds; in time
	// mode, they run forever and we cap them at Duration.
	agentsDone := make(chan struct{})
	go func() {
		// Wait only on the agent goroutines, not the observer.
		// We do this by tracking agents separately... but we already
		// have one wg. Use a sleep-poll to keep this simple: when
		// the bus is idle (no new envelopes for 100ms in fixed mode)
		// we declare the agents done. Slight hack but adequate.
		if cfg.Iterations <= 0 {
			return // never close in time mode
		}
		// In fixed-iter mode, just wait briefly for agents to finish.
		// Agents do N ticks then return. With 20 agents × 30 ticks
		// each, total work is ~600 publishes, well under 100ms.
		time.Sleep(500 * time.Millisecond)
		close(agentsDone)
	}()

	select {
	case <-ctx.Done():
	case <-time.After(cfg.Duration):
	case <-agentsDone:
	}
	close(stop)
	wg.Wait()

	return &Result{
		Seed:       cfg.Seed,
		Duration:   cfg.Duration,
		Codec:      cfg.Codec.Name(),
		AgentStats: stats,
		Report:     observer.Report(),
	}, nil
}

// hashSeed turns a seed string into a deterministic uint64.
func hashSeed(s string) uint64 {
	if s == "" {
		return 1
	}
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
