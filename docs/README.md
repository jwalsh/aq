# aq Documentation

Current reference docs for agents working on aq. Read these before
making changes.

## Current State (read these first)

| Doc | What it covers |
|-----|---------------|
| CONFIGURATION.md | `~/.aq/config.json` format, MQTT/mDNS/mesh settings, env overrides |
| INVARIANTS.md | Advisory invariant system — gossip verification, not blocking |
| HEARTBEAT.md | C-7 TTL/heartbeat options, PostToolUse hook approach |
| CONCEPT-CONFLICTS.md | Semantic conflict detection beyond file-name overlap (C-2, C-4, C-8) |
| BENCHMARKS.md | Performance baselines (p99 latency, throughput on M4) |
| UX-REVIEW.md | CLI ergonomics: flag naming, command overlap, shell quoting |
| TRANSPORTS.org | Transport scoring matrix and why filesystem is canonical |
| PRESENTATION.org | Beamer slides for stakeholder context |

## Subdirectories

### `adr/` — Architectural Decision Records

Point-in-time records: reviews, experiments, experience reports. These
document *why* decisions were made, not *what* the current state is.
Read when you need context on a past decision. Do not treat as current.

Key files:
- `DOGFOODING.md` — the empirical record from 4-agent parallel build
- `REVIEW-L7-ENGINEER.md` — structural review (race conditions, ULID, single-file)
- `REVIEW-CTO.md` — strategic assessment

### `research/` — Research and Roadmap

Future-looking specs, transport designs, and conjectures not yet
implemented. Read when working on new features or evaluating
architectural options.

Key files:
- `otel-passive-presence.org` — C-015: bootstrap paradox resolution via OTEL tap
- `observability-landscape.org` — gossip vs OTel industry comparison
- `transport-state-machine.org` — v0.5 transport layer architecture
- `transport-mounts.org` — declarative transport configuration spec
- `protocol-landscape.org` — aq positioning vs MCP/A2A/AMTP

## Format conventions

- **Markdown** for agent-readable specs, protocols, and reference docs
- **Org-mode** for research, presentations, and literate specs with tangled code
