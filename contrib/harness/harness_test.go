package harness

import (
	"context"
	"testing"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
)

// TestRun_AllCodecs is the main harness invocation. Runs each codec
// in deterministic mode (fixed iterations per agent) and prints the
// comparative report. The Bad codec is expected to fail catastrophically;
// that is the contribution it makes to the BugBash narrative.
//
// Run with: go test -v -run TestRun_AllCodecs -timeout 5m
func TestRun_AllCodecs(t *testing.T) {
	if testing.Short() {
		t.Skip("harness is slow; -short")
	}

	for _, codec := range codecs.All() {
		codec := codec
		t.Run(codec.Name(), func(t *testing.T) {
			result, err := Run(context.Background(), Config{
				Seed:       "bugbash-baseline-" + codec.Name(),
				Iterations: 30,
				Duration:   30 * time.Second,
				Codec:      codec,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			t.Log("\n" + result.Report.String())

			// Bad codec is supposed to fail. If it does NOT fail, that's
			// the bug — the floor wasn't low enough.
			if codec.Name() == "bad" {
				if result.Report.TotalEnvelopes > 0 {
					t.Errorf("Bad codec produced %d envelopes — should be 0 (it's the floor)",
						result.Report.TotalEnvelopes)
				}
				return
			}

			// All other codecs: sanity check that envelopes flowed
			if result.Report.TotalEnvelopes < 10 {
				t.Errorf("only %d envelopes processed — agents not running?",
					result.Report.TotalEnvelopes)
			}

			for _, cs := range result.Report.CodecStats {
				if cs.Name != codec.Name() {
					continue
				}
				total := cs.IdentityOK + cs.IdentityErr
				if total == 0 {
					continue
				}
				rate := float64(cs.IdentityOK) / float64(total)
				t.Logf("%s identity attribution: %.1f%% (%d/%d)",
					codec.Name(), rate*100, cs.IdentityOK, total)
			}
		})
	}
}

// TestRun_DeterministicReplay verifies that the same seed produces
// the same chaos when running in deterministic (Iterations > 0) mode.
// This is the Antithesis-style replay guarantee — given a seed, every
// failure injection fires at the same iteration on the same agent.
func TestRun_DeterministicReplay(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	cfg := Config{
		Seed:       "deterministic-replay-test",
		Iterations: 50,
		Duration:   30 * time.Second,
		Codec:      codecs.Pipe{},
	}

	r1, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Per-agent counters must match exactly. If they don't, somewhere
	// in the harness we have a non-PRNG source of randomness leaking in.
	for id, s1 := range r1.AgentStats {
		s2 := r2.AgentStats[id]
		if s2 == nil {
			t.Errorf("agent %s missing in r2", id)
			continue
		}
		a1, a2 := s1.Attempts.Load(), s2.Attempts.Load()
		f1, f2 := s1.Failures.Load(), s2.Failures.Load()
		if a1 != a2 {
			t.Errorf("agent %s attempts: r1=%d r2=%d", id, a1, a2)
		}
		if f1 != f2 {
			t.Errorf("agent %s failures: r1=%d r2=%d (PRNG diverged)", id, f1, f2)
		}
	}
}

// TestRun_AdversarialCohortShowsViolations verifies that the adversarial
// agents actually break things — if they don't, our chaos isn't working.
func TestRun_AdversarialCohortShowsViolations(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	result, err := Run(context.Background(), Config{
		Seed:       "adversarial-test",
		Iterations: 100,
		Duration:   30 * time.Second,
		Codec:      codecs.JSON{},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Log("\n" + result.Report.String())

	// We expect at least *some* violations or decode failures from the
	// adversarial cohort. Either is evidence the chaos reached observer.
	totalViolations := int64(0)
	for _, v := range result.Report.Violations {
		totalViolations += v
	}
	totalDecodeFails := int64(0)
	for _, cs := range result.Report.CodecStats {
		totalDecodeFails += cs.DecodeFail
	}

	// Iterations=100 × 5 adversarial agents × 50% rate = ~250 fired
	// failures, well above any seed-driven variance threshold.
	if totalViolations == 0 && totalDecodeFails == 0 {
		t.Error("zero violations AND zero decode fails — adversarial chaos isn't reaching observer")
	} else {
		t.Logf("violations: %d types, %d total events; decode fails: %d",
			len(result.Report.Violations), totalViolations, totalDecodeFails)
	}
}
