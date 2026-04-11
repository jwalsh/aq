// Package bridge translates aq gossip into OpenTelemetry metrics.
//
// Architectural role: in a deployment with no LAN — only a single LoRa
// adapter on one bridge node — this bridge is the *only* path from
// agent presence to centralized observability. If the v3 wire format
// does not carry enough identity to make these metrics useful, the
// observability story collapses.
//
// The bridge subscribes to a harness Bus (in-process) or to MQTT
// (out-of-process) and emits:
//
//	aq_broadcasts_total{host,user,phase,status,cid,codec,cohort}
//	aq_broadcast_size_bytes{codec}                              (histogram)
//	aq_broadcast_age_seconds{}                                  (histogram)
//	aq_active_agents{host}                                      (gauge)
//	aq_decode_errors_total{codec,error_type}
//	aq_identity_attribution_loss_total{codec}
//
// Endpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT at runtime. If
// unset, falls back to stdout — never hardcodes a network address.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/jwalsh/aq/contrib/codecs"
	"github.com/jwalsh/aq/contrib/harness"
)

// Bridge subscribes to a harness Bus and emits OTel metrics.
type Bridge struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter

	broadcasts metric.Int64Counter
	sizeBytes  metric.Int64Histogram
	ageSeconds metric.Float64Histogram
	decodeErrs metric.Int64Counter
	idLoss     metric.Int64Counter

	mu           sync.Mutex
	activeHosts  map[string]time.Time // host -> last seen
	codecsByName map[string]codecs.Codec
}

// New constructs a Bridge with an OTel meter provider chosen by env:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT set → OTLP gRPC exporter to that endpoint
//	unset                           → stdout exporter (dev mode)
//
// The endpoint string is NEVER hardcoded — only read from env.
func New(ctx context.Context, serviceName string) (*Bridge, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	var reader sdkmetric.Reader
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exp,
			sdkmetric.WithInterval(5*time.Second))
	} else {
		exp, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("stdout exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exp,
			sdkmetric.WithInterval(5*time.Second))
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	meter := provider.Meter("github.com/jwalsh/aq/contrib/otel-bridge")

	b := &Bridge{
		provider:     provider,
		meter:        meter,
		activeHosts:  make(map[string]time.Time),
		codecsByName: make(map[string]codecs.Codec),
	}
	for _, c := range codecs.All() {
		b.codecsByName[c.Name()] = c
	}

	if b.broadcasts, err = meter.Int64Counter("aq_broadcasts_total",
		metric.WithDescription("aq broadcasts observed by codec/host/user/phase")); err != nil {
		return nil, err
	}
	if b.sizeBytes, err = meter.Int64Histogram("aq_broadcast_size_bytes",
		metric.WithDescription("encoded broadcast size in bytes"),
		metric.WithExplicitBucketBoundaries(50, 80, 100, 150, 200, 300, 500, 1000)); err != nil {
		return nil, err
	}
	if b.ageSeconds, err = meter.Float64Histogram("aq_broadcast_age_seconds",
		metric.WithDescription("seconds between broadcast ts and bridge ingest"),
		metric.WithExplicitBucketBoundaries(0.1, 1, 5, 30, 60, 300, 1800)); err != nil {
		return nil, err
	}
	if b.decodeErrs, err = meter.Int64Counter("aq_decode_errors_total",
		metric.WithDescription("decode failures by codec and error type")); err != nil {
		return nil, err
	}
	if b.idLoss, err = meter.Int64Counter("aq_identity_attribution_loss_total",
		metric.WithDescription("messages where decoded host/user differ from ground truth")); err != nil {
		return nil, err
	}

	// Async gauge for active hosts. Reads activeHosts under lock.
	_, err = meter.Int64ObservableGauge("aq_active_agents",
		metric.WithDescription("distinct hosts seen in last 60s"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			b.mu.Lock()
			defer b.mu.Unlock()
			cutoff := time.Now().Add(-60 * time.Second)
			counts := make(map[string]int64)
			for host, last := range b.activeHosts {
				if last.After(cutoff) {
					counts[host]++
				}
			}
			for host, count := range counts {
				o.Observe(count, metric.WithAttributes(attribute.String("host", host)))
			}
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}

	return b, nil
}

// Run subscribes to the bus and processes envelopes until stop closes.
// This is the in-process integration point used by the harness.
func (b *Bridge) Run(ctx context.Context, bus *harness.Bus, stop <-chan struct{}) {
	sub := bus.Subscribe()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case env, ok := <-sub:
			if !ok {
				return
			}
			b.process(ctx, env)
		}
	}
}

// process is the per-envelope hot path. Decodes the wire bytes (the
// LoRa-equivalent: only the bytes survive, ground truth is unknown to
// receivers in real deployment) and emits metrics.
func (b *Bridge) process(ctx context.Context, env harness.Envelope) {
	codec, ok := b.codecsByName[env.Codec]
	if !ok {
		b.decodeErrs.Add(ctx, 1, metric.WithAttributes(
			attribute.String("codec", env.Codec),
			attribute.String("error_type", "unknown_codec"),
		))
		return
	}

	got, err := codec.Decode(env.WireBytes)
	if err != nil {
		errType := "other"
		if errors.Is(err, codecs.ErrCorrupt) {
			errType = "corrupt"
		}
		b.decodeErrs.Add(ctx, 1, metric.WithAttributes(
			attribute.String("codec", env.Codec),
			attribute.String("error_type", errType),
		))
		return
	}

	// Identity attribution check — the v3 mandate. Compare *decoded*
	// values (the only thing a real bridge would have) against the
	// ground truth (which only the harness knows).
	wantHost := env.Record.Host
	wantUser := env.Record.User
	if env.Codec == "pipe" {
		// Pipe truncates to 8 chars by design.
		if len(wantHost) > 8 {
			wantHost = wantHost[:8]
		}
		if len(wantUser) > 8 {
			wantUser = wantUser[:8]
		}
	}
	if got.Host != wantHost || got.User != wantUser {
		b.idLoss.Add(ctx, 1, metric.WithAttributes(
			attribute.String("codec", env.Codec),
		))
	}

	// Emit broadcast counter with full attribution. Cardinality matters
	// here — host × user × phase × status × cohort × codec is bounded
	// by the agent population, which is ~20 in the harness.
	b.broadcasts.Add(ctx, 1, metric.WithAttributes(
		attribute.String("host", got.Host),
		attribute.String("user", got.User),
		attribute.String("phase", got.Phase),
		attribute.String("status", got.Status),
		attribute.String("cid", got.CID),
		attribute.String("codec", env.Codec),
		attribute.String("cohort", env.Cohort.String()),
	))

	b.sizeBytes.Record(ctx, int64(len(env.WireBytes)),
		metric.WithAttributes(attribute.String("codec", env.Codec)))

	age := time.Since(env.Sent).Seconds()
	b.ageSeconds.Record(ctx, age)

	// Track active host for the gauge.
	if got.Host != "" {
		b.mu.Lock()
		b.activeHosts[got.Host] = time.Now()
		b.mu.Unlock()
	}
}

// Shutdown flushes pending metrics and tears down the provider.
func (b *Bridge) Shutdown(ctx context.Context) error {
	return b.provider.Shutdown(ctx)
}
