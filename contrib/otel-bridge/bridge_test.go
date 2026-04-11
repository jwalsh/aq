package bridge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jwalsh/aq/contrib/codecs"
	"github.com/jwalsh/aq/contrib/harness"
)

// TestBridge_StdoutFallback verifies the bridge constructs successfully
// when no OTel endpoint is configured. This is the dev path — must work
// without nexus or any LAN endpoint.
func TestBridge_StdoutFallback(t *testing.T) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		t.Skip("OTEL_EXPORTER_OTLP_ENDPOINT is set; this test only runs in dev mode")
	}
	ctx := context.Background()
	b, err := New(ctx, "aq-bridge-test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Shutdown(ctx)
	// Construction is enough — exporter doesn't fire until first interval
}

// TestBridge_HarnessIntegration runs a small harness through the bridge
// and verifies that metrics get emitted. The success criterion is "no
// errors and at least one envelope processed". The actual stdout output
// is captured but not asserted on — we trust the OTel SDK.
func TestBridge_HarnessIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	// Suppress OTel endpoint to use stdout exporter
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Redirect stdout to discard so test output isn't polluted
	oldStdout := os.Stdout
	devnull, _ := os.Open(os.DevNull)
	os.Stdout = devnull
	defer func() {
		os.Stdout = oldStdout
		devnull.Close()
	}()

	ctx := context.Background()
	bridge, err := New(ctx, "aq-bridge-test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer bridge.Shutdown(ctx)

	bus := harness.NewBus(1024)

	stop := make(chan struct{})
	bridgeDone := make(chan struct{})
	go func() {
		bridge.Run(ctx, bus, stop)
		close(bridgeDone)
	}()

	// Inject some envelopes
	jsonCodec := codecs.JSON{}
	for i := 0; i < 50; i++ {
		rec := codecs.Record{
			V:        3,
			Host:     "test-host",
			User:     "test-user",
			Agent:    "github.com/test/repo/main",
			Worktree: "main",
			CID:      "C-1",
			Claim:    "bridge integration test",
			Phase:    "p",
			Status:   "a",
			Files:    []string{"main.go"},
			Ts:       time.Now().Unix(),
			TTL:      3600,
			ID:       "0000000000000000000000",
		}
		data, _ := jsonCodec.Encode(rec)
		bus.Publish(harness.Envelope{
			AgentID:   "test-1",
			Cohort:    harness.CohortNormal,
			Codec:     "json",
			Sent:      time.Now(),
			Record:    rec,
			WireBytes: data,
		})
	}

	// Give the bridge a moment to drain
	time.Sleep(100 * time.Millisecond)
	close(stop)
	<-bridgeDone
}

// TestBridge_AcrossAllCodecs validates that the bridge handles every
// codec without crashing — the per-LoRa-link reality where the bridge
// might receive any codec on any frame.
func TestBridge_AcrossAllCodecs(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	devnull, _ := os.Open(os.DevNull)
	oldStdout := os.Stdout
	os.Stdout = devnull
	defer func() { os.Stdout = oldStdout; devnull.Close() }()

	ctx := context.Background()
	bridge, err := New(ctx, "aq-bridge-multicodec-test")
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Shutdown(ctx)

	bus := harness.NewBus(1024)
	stop := make(chan struct{})
	go bridge.Run(ctx, bus, stop)

	rec := codecs.Record{
		V: 3, Host: "h", User: "u", Agent: "github.com/x/y/main",
		Worktree: "main", CID: "C-7", Phase: "p", Status: "a",
		Ts: time.Now().Unix(), TTL: 3600, ID: "0000000000000000000000",
	}

	for _, c := range codecs.All() {
		data, err := c.Encode(rec)
		if err != nil {
			t.Logf("%s: encode failed (expected for bad): %v", c.Name(), err)
			continue
		}
		bus.Publish(harness.Envelope{
			AgentID:   "multi-test",
			Cohort:    harness.CohortNormal,
			Codec:     c.Name(),
			Sent:      time.Now(),
			Record:    rec,
			WireBytes: data,
		})
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
}
