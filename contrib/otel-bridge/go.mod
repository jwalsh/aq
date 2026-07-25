// contrib/otel-bridge — gossip → OTel metrics bridge.
//
// The killer requirement: even when no LAN is available, a single
// node with a LoRa adapter can translate gossip into OTel metrics
// that flow to the central observability stack. This module proves
// the v3 wire format carries enough identity to make that bridge
// useful.
//
// Endpoint configuration is read from OTEL_EXPORTER_OTLP_ENDPOINT
// at runtime — never hardcoded. Falls back to stdout exporter when
// the env var is unset, so dev works without a collector.

module github.com/jwalsh/aq/contrib/otel-bridge

go 1.25.0

require (
	github.com/jwalsh/aq/contrib/codecs v0.0.0
	github.com/jwalsh/aq/contrib/harness v0.0.0
	go.opentelemetry.io/otel v1.43.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.32.0
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.32.0
	go.opentelemetry.io/otel/metric v1.43.0
	go.opentelemetry.io/otel/sdk v1.43.0
	go.opentelemetry.io/otel/sdk/metric v1.43.0
)

require (
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.23.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.3.1 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/jwalsh/aq/contrib/codecs => ../codecs

replace github.com/jwalsh/aq/contrib/harness => ../harness
