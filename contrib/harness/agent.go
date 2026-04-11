// Package harness simulates 20 aq agents in-process with deterministic
// chaos. The point is to push the wire format harder than real LAN
// testing did, with seed-based replayability for any failure found.
//
// Architecture:
//
//	+--Coordinator (Run)----------+
//	| seed → split into PRNGs     |
//	|                             |
//	|  +--Cohorts--+              |
//	|  | A normal  | x6           |
//	|  | B edge    | x4           |
//	|  | C clash   | x5           |
//	|  | D adversarial | x5       |
//	|  +-----------+              |
//	|                             |
//	|  +--Observer (read-only)--+ |
//	|  | invariant checks       | |
//	|  | per-codec size metrics | |
//	|  | per-cohort failure rate| |
//	|  +------------------------+ |
//	+-----------------------------+
package harness

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
)

// Cohort identifies an agent's behavioral group.
type Cohort int

const (
	CohortNormal Cohort = iota
	CohortEdge
	CohortClash
	CohortAdversarial
)

func (c Cohort) String() string {
	switch c {
	case CohortNormal:
		return "normal"
	case CohortEdge:
		return "edge"
	case CohortClash:
		return "clash"
	case CohortAdversarial:
		return "adv"
	}
	return "?"
}

// FailureMode is the kind of misbehavior an adversarial agent injects.
type FailureMode int

const (
	FailNone FailureMode = iota
	FailCorruptBytes        // bit-flip a few bytes after encode
	FailStaleTimestamp      // ts in the past beyond TTL
	FailFutureTimestamp     // ts in the future
	FailEmptyHost           // emit Host=""
	FailDuplicateID         // reuse the same ULID
	FailOversizedPayload    // 500 files, 200-char claim
	FailVersionDowngrade    // emit V=0 (pretends to be v2)
)

// AgentProfile is the static configuration of one simulated agent.
type AgentProfile struct {
	ID           string
	Cohort       Cohort
	Host         string
	User         string
	Agent        string
	Worktree     string
	Conjectures  []string
	FilePool     []string
	Phase        string
	Status       string
	Interval     time.Duration
	Failure      FailureMode
	FailureRate  float64 // 0.0 to 1.0
}

// agentRun is a single agent's runtime state.
type agentRun struct {
	profile AgentProfile
	rng     *rand.Rand
	bus     *Bus
	codec   codecs.Codec
	stats   *AgentStats
}

// AgentStats are per-agent counters collected during a run.
type AgentStats struct {
	Attempts atomic.Int64 // tick() called this many times
	Sent     atomic.Int64 // successful publishes
	Errors   atomic.Int64 // encode errors (Bad codec, oversize)
	BytesOut atomic.Int64
	Failures atomic.Int64 // intentional failure injections fired
}

// run executes the agent's broadcast loop, either by ticker or by
// fixed iteration count depending on Config.Iterations.
func (a *agentRun) run(stop <-chan struct{}, fixedIters int) {
	if fixedIters > 0 {
		// Deterministic mode: do exactly N ticks regardless of wall clock
		for i := 0; i < fixedIters; i++ {
			select {
			case <-stop:
				return
			default:
				a.tick()
			}
		}
		return
	}
	ticker := time.NewTicker(a.profile.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.tick()
		}
	}
}

// tick produces one broadcast per tick, applying any failure injection.
func (a *agentRun) tick() {
	a.stats.Attempts.Add(1)
	rec := a.makeRecord()

	// Apply failure injection BEFORE encoding (so the encoder sees a
	// deliberately broken record). Some failures happen post-encode.
	rec = a.injectPreEncode(rec)

	data, err := a.codec.Encode(rec)
	if err != nil {
		a.stats.Errors.Add(1)
		return
	}

	data = a.injectPostEncode(data)

	a.bus.Publish(Envelope{
		AgentID:   a.profile.ID,
		Cohort:    a.profile.Cohort,
		Codec:     a.codec.Name(),
		Sent:      time.Now(),
		Record:    rec,
		WireBytes: data,
	})

	a.stats.Sent.Add(1)
	a.stats.BytesOut.Add(int64(len(data)))
}

// makeRecord builds the next broadcast for this agent.
func (a *agentRun) makeRecord() codecs.Record {
	cid := a.profile.Conjectures[a.rng.Intn(len(a.profile.Conjectures))]
	files := pickFiles(a.rng, a.profile.FilePool, 1, 4)
	return codecs.Record{
		V:        3,
		Host:     a.profile.Host,
		User:     a.profile.User,
		Agent:    a.profile.Agent,
		Worktree: a.profile.Worktree,
		CID:      cid,
		Claim:    fmt.Sprintf("%s working on %s", a.profile.ID, cid),
		Phase:    a.profile.Phase,
		Status:   a.profile.Status,
		Files:    files,
		Ts:       time.Now().Unix(),
		TTL:      3600,
		ID:       fmt.Sprintf("%022x", a.rng.Int63()),
	}
}

// injectPreEncode mutates the record according to the agent's failure mode.
// All randomness drawn from the agent's PRNG so failures are reproducible.
func (a *agentRun) injectPreEncode(r codecs.Record) codecs.Record {
	if a.profile.Failure == FailNone || a.rng.Float64() > a.profile.FailureRate {
		return r
	}
	a.stats.Failures.Add(1)
	switch a.profile.Failure {
	case FailStaleTimestamp:
		r.Ts -= 7200
	case FailFutureTimestamp:
		r.Ts += 3600
	case FailEmptyHost:
		r.Host = ""
	case FailDuplicateID:
		r.ID = "0000000000000000000000"
	case FailOversizedPayload:
		r.Files = make([]string, 500)
		for i := range r.Files {
			r.Files[i] = fmt.Sprintf("file%d.go", i)
		}
		r.Claim = "x" + makeStr(a.rng, 199)
	case FailVersionDowngrade:
		r.V = 0
	}
	return r
}

// injectPostEncode mutates the wire bytes themselves (bit-flip etc.).
func (a *agentRun) injectPostEncode(data []byte) []byte {
	if a.profile.Failure != FailCorruptBytes || a.rng.Float64() > a.profile.FailureRate {
		return data
	}
	a.stats.Failures.Add(1)
	out := make([]byte, len(data))
	copy(out, data)
	// Flip 3 random bits
	for i := 0; i < 3; i++ {
		if len(out) == 0 {
			break
		}
		bytePos := a.rng.Intn(len(out))
		bitPos := a.rng.Intn(8)
		out[bytePos] ^= 1 << bitPos
	}
	return out
}

// pickFiles draws between min and max files from the pool, deterministically.
func pickFiles(rng *rand.Rand, pool []string, min, max int) []string {
	if len(pool) == 0 || max < min {
		return nil
	}
	n := min + rng.Intn(max-min+1)
	if n > len(pool) {
		n = len(pool)
	}
	out := make([]string, n)
	indices := rng.Perm(len(pool))[:n]
	for i, idx := range indices {
		out[i] = pool[idx]
	}
	return out
}

func makeStr(rng *rand.Rand, n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rng.Intn(len(letters))]
	}
	return string(b)
}

// BuildCohorts returns the canonical 20-agent set for the harness.
// Deterministic given a single seed: changing the seed changes the
// random elements (claim text, file selection) but cohort structure
// is fixed.
func BuildCohorts() []AgentProfile {
	sharedFiles := []string{"shared.go", "api.go", "auth.go"}
	commonFiles := []string{"main.go", "config.go", "server.go", "handler.go", "session.go"}

	hosts := []string{"agent01", "agent02", "agent03"}
	users := []string{"alice", "bob", "carol", "dave", "eve", "frank"}

	// Cohort A: 6 normal agents
	var profiles []AgentProfile
	for i := 0; i < 6; i++ {
		profiles = append(profiles, AgentProfile{
			ID:          fmt.Sprintf("norm-%d", i+1),
			Cohort:      CohortNormal,
			Host:        hosts[i%len(hosts)],
			User:        users[i],
			Agent:       fmt.Sprintf("github.com/%s/repo/main", users[i]),
			Worktree:    "main",
			Conjectures: []string{fmt.Sprintf("C-%d", 10+i)},
			FilePool:    commonFiles,
			Phase:       "p",
			Status:      "a",
			Interval:    600 * time.Millisecond,
		})
	}

	// Cohort B: 4 edge-case agents
	profiles = append(profiles,
		AgentProfile{
			ID: "edge-empty", Cohort: CohortEdge,
			Host: "h", User: "u", Agent: "a/b", Worktree: "main",
			Conjectures: []string{"C-1"}, FilePool: nil,
			Phase: "c", Status: "a", Interval: 800 * time.Millisecond,
		},
		AgentProfile{
			ID: "edge-max", Cohort: CohortEdge,
			Host: "longhostname", User: "longusername",
			Agent: "github.com/very-long-org-name/very-long-repo-name/main",
			Worktree:    "feature/long-branch-name-with-extras",
			Conjectures: []string{"C-9999999"},
			FilePool:    []string{"a/very/deep/path/file.go", "another/long/path.go"},
			Phase:       "p", Status: "a", Interval: 700 * time.Millisecond,
		},
		AgentProfile{
			ID: "edge-unicode", Cohort: CohortEdge,
			Host: "naïve", User: "résumé",
			Agent:       "github.com/u/répo/main",
			Worktree:    "main",
			Conjectures: []string{"C-π"},
			FilePool:    []string{"contrôle.go", "naïveté.go"},
			Phase:       "p", Status: "a", Interval: 750 * time.Millisecond,
		},
		AgentProfile{
			ID: "edge-rapid", Cohort: CohortEdge,
			Host: "agent04", User: "rapid",
			Agent:       "github.com/rapid/repo/main",
			Worktree:    "main",
			Conjectures: []string{"C-101", "C-102", "C-103"},
			FilePool:    commonFiles,
			Phase:       "p", Status: "a", Interval: 50 * time.Millisecond,
		},
	)

	// Cohort C: 5 clash agents — all on the same files
	for i := 0; i < 5; i++ {
		profiles = append(profiles, AgentProfile{
			ID:          fmt.Sprintf("clash-%d", i+1),
			Cohort:      CohortClash,
			Host:        fmt.Sprintf("clashhost%d", i+1),
			User:        fmt.Sprintf("clashuser%d", i+1),
			Agent:       fmt.Sprintf("github.com/clash/agent%d/main", i+1),
			Worktree:    "main",
			Conjectures: []string{fmt.Sprintf("C-2%d", i)},
			FilePool:    sharedFiles,
			Phase:       "p",
			Status:      "a",
			Interval:    500 * time.Millisecond,
		})
	}

	// Cohort D: 5 adversarial agents
	failures := []FailureMode{
		FailCorruptBytes,
		FailStaleTimestamp,
		FailFutureTimestamp,
		FailDuplicateID,
		FailOversizedPayload,
	}
	failureNames := []string{"corrupt", "stale", "future", "dup", "oversize"}
	for i, f := range failures {
		profiles = append(profiles, AgentProfile{
			ID:          fmt.Sprintf("adv-%s", failureNames[i]),
			Cohort:      CohortAdversarial,
			Host:        fmt.Sprintf("advhost%d", i+1),
			User:        "mallory",
			Agent:       fmt.Sprintf("github.com/adv/%s/main", failureNames[i]),
			Worktree:    "main",
			Conjectures: []string{fmt.Sprintf("C-3%d", i)},
			FilePool:    commonFiles,
			Phase:       "p",
			Status:      "a",
			Interval:    1000 * time.Millisecond,
			Failure:     f,
			FailureRate: 0.5,
		})
	}

	return profiles
}

// statsForCohort sums stats for all agents in a given cohort.
func statsForCohort(stats map[string]*AgentStats, profiles []AgentProfile, c Cohort) (sent, errs int64) {
	for _, p := range profiles {
		if p.Cohort != c {
			continue
		}
		s := stats[p.ID]
		sent += s.Sent.Load()
		errs += s.Errors.Load()
	}
	return
}

var _ = sync.Mutex{} // keep import
