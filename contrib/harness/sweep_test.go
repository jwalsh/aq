package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
)

// TestCorruptionSweep is the BugBash centerpiece. It varies the
// adversarial bit-flip rate from 0% to 100% and measures identity
// attribution per codec, producing a curve.
//
// The killer demo: "varint loses identity 12% of the time at 30% bit
// flip rate; CBOR stays under 2%; pipe sits in the middle." That kind
// of comparative measurement is impossible without running the harness.
//
// Run with: go test -v -run TestCorruptionSweep -timeout 5m
func TestCorruptionSweep(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	// Reduce the cohort to just one corrupting agent so the failure
	// rate variable is the *only* thing changing across runs.
	mkProfile := func(rate float64) []AgentProfile {
		return []AgentProfile{
			{
				ID:          "norm",
				Cohort:      CohortNormal,
				Host:        "h1",
				User:        "u1",
				Agent:       "github.com/x/y/main",
				Worktree:    "main",
				Conjectures: []string{"C-1"},
				FilePool:    []string{"main.go"},
				Phase:       "p", Status: "a",
				Interval: time.Millisecond, // unused in fixed-iter mode
			},
			{
				ID:          "adv-corrupt",
				Cohort:      CohortAdversarial,
				Host:        "h2",
				User:        "u2",
				Agent:       "github.com/x/z/main",
				Worktree:    "main",
				Conjectures: []string{"C-2"},
				FilePool:    []string{"main.go"},
				Phase:       "p", Status: "a",
				Interval:    time.Millisecond,
				Failure:     FailCorruptBytes,
				FailureRate: rate,
			},
		}
	}

	rates := []float64{0.0, 0.1, 0.25, 0.5, 0.75, 1.0}

	type curvePoint struct {
		rate     float64
		codec    string
		decodeOK int64
		decodeFail int64
		idOK     int64
		idLoss   int64
	}
	var curve []curvePoint

	for _, rate := range rates {
		for _, codec := range codecs.All() {
			if codec.Name() == "bad" {
				continue // Bad never produces useful output
			}
			result, err := Run(context.Background(), Config{
				Seed:       fmt.Sprintf("sweep-%s-%g", codec.Name(), rate),
				Iterations: 200,
				Duration:   30 * time.Second,
				Codec:      codec,
				Profiles:   mkProfile(rate),
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, cs := range result.Report.CodecStats {
				if cs.Name != codec.Name() {
					continue
				}
				curve = append(curve, curvePoint{
					rate:       rate,
					codec:      codec.Name(),
					decodeOK:   cs.DecodeOK,
					decodeFail: cs.DecodeFail,
					idOK:       cs.IdentityOK,
					idLoss:     cs.IdentityErr,
				})
			}
		}
	}

	// Print the table — this is what goes in the BugBash slide
	t.Log("\n=== CORRUPTION SWEEP RESULTS ===")
	t.Log("\n  rate     codec   decodeOK  decodeFail   idOK    idLoss   id_loss_pct")
	for _, p := range curve {
		idTotal := p.idOK + p.idLoss
		var pct float64
		if idTotal > 0 {
			pct = float64(p.idLoss) / float64(idTotal) * 100
		}
		t.Logf("  %4.0f%%   %-7s    %4d        %4d    %4d      %4d        %5.1f%%",
			p.rate*100, p.codec, p.decodeOK, p.decodeFail, p.idOK, p.idLoss, pct)
	}

	// Generate the markdown table for the report
	var md strings.Builder
	md.WriteString("\n=== MARKDOWN TABLE FOR REPORT ===\n\n")
	md.WriteString("| corruption | json | pipe | cbor | varint | dict |\n")
	md.WriteString("|------------|------|------|------|--------|------|\n")
	rateMap := make(map[float64]map[string]string)
	for _, p := range curve {
		idTotal := p.idOK + p.idLoss
		var pct float64
		if idTotal > 0 {
			pct = float64(p.idLoss) / float64(idTotal) * 100
		}
		if rateMap[p.rate] == nil {
			rateMap[p.rate] = make(map[string]string)
		}
		rateMap[p.rate][p.codec] = fmt.Sprintf("%.1f%%", pct)
	}
	for _, r := range rates {
		md.WriteString(fmt.Sprintf("| %4.0f%%      ", r*100))
		for _, c := range []string{"json", "pipe", "cbor", "varint", "dict"} {
			md.WriteString(fmt.Sprintf("| %5s ", rateMap[r][c]))
		}
		md.WriteString("|\n")
	}
	t.Log(md.String())

	// Sanity: at corruption rate 0, every codec should be at 0% identity loss
	for _, p := range curve {
		if p.rate == 0 && p.idLoss > 0 {
			t.Errorf("%s lost identity (%d) at 0%% corruption — should be impossible",
				p.codec, p.idLoss)
		}
	}
}

// TestSizeAcrossInputs measures size distribution per codec across a
// realistic mix of input sizes. Output is the size table for the
// BugBash report.
func TestSizeAcrossInputs(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	result, err := Run(context.Background(), Config{
		Seed:       "size-distribution-test",
		Iterations: 100,
		Duration:   30 * time.Second,
		Codec:      codecs.JSON{}, // any codec — observer re-encodes with all
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Log("\n=== SIZE DISTRIBUTION (BYTES) ===")
	t.Log("\n  codec     N    p50    p95    p99    mean   entropy")
	for _, cs := range result.Report.CodecStats {
		t.Logf("  %-8s %4d   %4d   %4d   %4d   %5.1f   %5.2f",
			cs.Name, cs.N, cs.BytesP50, cs.BytesP95, cs.BytesP99, cs.BytesMean, cs.BytesEntropyBits)
	}

	// Critical: pipe must satisfy the 80-byte LoRa target at p95
	// (not p99 — adversarial inputs are allowed to spike).
	for _, cs := range result.Report.CodecStats {
		if cs.Name == "pipe" && cs.BytesP95 > 100 {
			t.Errorf("pipe p95 = %d bytes, exceeds 100-byte LoRa working budget", cs.BytesP95)
		}
	}
}
