# aq v0.6.1 — System State Meta Prompt

> Point-in-time checkpoint: 2026-04-13
> Purpose: alignment check for contracts, domains, responsibilities, and cached system state.
> Use this to bootstrap a new agent session against this codebase.

## What aq IS

A gossip layer (L1.5) for multi-agent development. Agents broadcast
intent — which conjecture, what claim, what phase — via filesystem-backed
channels so peers detect semantic conflicts before they become merge
conflicts. The conflict surface is **ideas and architecture**, not files.
It is the mDNS of coding agents: "does anyone know this address?" Nobody
is required to answer.

aq is gossip, not coordination. Agents broadcast presence; nobody is
required to listen. No orchestrator, no broker, no authority. Just gossip.

## System topology

```
Agent → aq announce (any host)
          ├── Filesystem (~/.aq/channels/broadcast/)     always, zero deps
          ├── UDP broadcast (:4181)                       LAN, stdlib
          ├── MQTT (broker:1883)                          LAN/WAN, QoS 0
          ├── mDNS (_aq._tcp)                             LAN discovery
          ├── ggwave (audio, Python)                      physical proximity
          └── Meshtastic LoRa (contrib, 80B compact)      radio, no LAN needed

Receiver → aq listen
          ├── UDP RX → dedup → materialize to disk
          ├── MQTT RX → dedup → materialize to disk
          └── aq check → conflict detection → severity signal

Observer → contrib/otel-bridge
          ├── Reads from bus (in-process) or MQTT (out-of-process)
          ├── Emits: aq_broadcasts_total, aq_broadcast_size_bytes, etc.
          └── Endpoint: $OTEL_EXPORTER_OTLP_ENDPOINT (env, never hardcoded)
```

## Three-primitive interlock

aq does not exist alone. It is the temporal layer in a three-primitive system:

| Primitive | Dimension | Question       | Implementation |
|-----------|-----------|----------------|----------------|
| sb        | spatial   | where am I?    | git worktree   |
| cprr      | epistemic | why am I here? | conjecture     |
| aq        | temporal  | who else knows?| broadcast      |

All three must be installed: `sb v0.1.0-10`, `cprr 328c65a`, `aq v0.6.1`.

## Source structure (main.go, 2998 lines)

Single-file Go binary, stdlib only. No runtime dependencies.

| Section | Lines | Responsibility |
|---------|-------|----------------|
| ULID generation | ~60-67 | 22-char timestamp+random IDs |
| Broadcast struct | ~70-310 | v3 wire format with custom MarshalJSON/UnmarshalJSON (bilingual v2/v3) |
| Phase/Status | ~70-170 | CPRR phases with single-char marshal (p/c/r/n, a/d/b) |
| ConflictSignal | ~360-380 | File overlap × phase severity → HIGH/MEDIUM/LOW |
| Invariants | ~382-770 | 3-layer advisory checks: self (A), world (B), protocol (C) |
| Storage | ~830-1050 | Filesystem I/O: write, read, archive, gc |
| CLI: announce | ~1050-1210 | Build broadcast, write, fanout |
| CLI: whisper | ~1210-1230 | Like announce but TTL=60, severity ceiling MEDIUM |
| CLI: check | ~1290-1460 | Conflict detection against active broadcasts |
| CLI: status | ~1490-1560 | List active broadcasts |
| CLI: doctor | ~1560-1810 | Health check: home, config, transports, tools |
| CLI: quickstart | ~1810-1870 | Agent-consumable context dump |
| Transport config | ~1870-1910 | Load UDP/MQTT/mDNS/ggwave from config.json |
| Fanout | ~1910-2100 | Best-effort broadcast to all enabled transports |
| UDP frame codec | ~2100-2210 | 4-byte header (AQ 0x03 0x01) + JSON payload |
| UDP listen | ~2250-2360 | Broadcast or multicast RX, dedup, materialize |
| MQTT listen | ~2360-2420 | mosquitto_sub → dedup → materialize |
| CLI: listen | ~2420-2490 | Combined RX daemon |
| CLI: mqtt | ~2500-2720 | MQTT subcommands (tail, pub, topics, config) |
| main() | ~2720-2998 | Flag parse, subcommand dispatch |

## v3 wire format (canonical since v0.6.0)

Opinionated on write, tolerant on read. Single-char phase/status, mandatory identity.

```json
{
  "v": 3,
  "agent": "github.com/user/repo/main",
  "host": "host-a",
  "user": "alice",
  "worktree": "main",
  "cid": "C-42",
  "claim": "refactoring auth",
  "phase": "p",
  "status": "a",
  "files": ["auth.go"],
  "ts": 1775831548,
  "ttl": 3600,
  "id": "019d77cf0098337c6ba662"
}
```

Reads v2 (long keys `conjecture_id`, full-word phase `"proof"`) without error. Gossip is bilingual.

## Contrib packages (separate go.mod, deps allowed)

| Package | Description | go.mod? |
|---------|-------------|---------|
| codecs | 6-codec wire format research lab (json, pipe, cbor, varint, dict, bad) | yes (fxamacker/cbor) |
| harness | In-process 20-agent stress harness, deterministic chaos | yes |
| otel-bridge | Gossip → OTel metrics bridge (env-driven endpoint) | yes (go.opentelemetry.io) |
| chaos-docker | Jepsen-lite container chaos scaffolding | no (not yet functional) |
| meshtastic | LoRa mesh transport (v3 compact format) | no (//go:build ignore) |
| chaos | Subprocess-based 6-scenario chaos suite | no (//go:build ignore) |
| dashboard | WebSocket live dashboard | no (//go:build ignore) |
| mqtt | MQTT transport helpers | no (//go:build ignore) |
| ggwave | Audio transport (Python, audible-fast) | n/a (Python) |
| irc | IRC transport | no (//go:build ignore) |
| mdns | mDNS broadcast demo | no (//go:build ignore) |

## Test coverage

108 tests across 4 modules. All green at v0.6.1.

| Module | Tests | Focus |
|--------|-------|-------|
| main_test.go | 93 | Unit + 14 PBT properties (testing/quick) |
| contrib/codecs | 7 | P1-P6 × 5 codecs, size report |
| contrib/harness | 5 | 20-agent sim, deterministic replay, corruption sweep |
| contrib/otel-bridge | 3 | Stdout fallback, harness integration, multi-codec |

Key finding from corruption sweep (BugBash 2026):

| codec | p50 bytes | identity loss at 100% bit-flip |
|-------|----------|-------------------------------|
| pipe | 78 | **10.5%** (worst — positional encoding is brittle) |
| dict | 88 | 2.8% |
| varint | 135 | 6.4% |
| cbor | 167 | 4.7% |
| json | 264 | **2.3%** (best — parser errors fail loud) |

## Contracts and invariants

1. **Wire format contract**: v3 writes short keys, mandatory host/user. Reads any dialect (v2/v3).
2. **Filesystem contract**: `~/.aq/channels/broadcast/requests/` is the only required transport. Every feature must work with filesystem I/O alone.
3. **TTL contract**: DefaultTTL=3600s (1 hour), WhisperTTL=60s. Broadcasts expire; silence is normal.
4. **Conflict severity contract**: both proof=HIGH, one proof=MEDIUM, neither=LOW. Whisper broadcasts have severity ceiling of MEDIUM.
5. **Identity contract**: host and user are never empty. If detection fails, populated with "unknown".
6. **Transport contract**: fanout is best-effort. UDP always on. MQTT/mDNS/ggwave require config. No transport failure blocks announce.
7. **Invariant contract**: 3 layers (self/world/protocol), advisory only, never block. 10 invariant checks.
8. **OTel contract**: endpoint read from `$OTEL_EXPORTER_OTLP_ENDPOINT`, never hardcoded. Falls back to stdout.
9. **Git hygiene contract**: never `git add` a directory. Never commit IPs, hostnames, credentials. Audit before push.
10. **Doc contract**: docs/ = living (must match code). reports/ = frozen (YYYYMMDD). adr/ = immutable decisions.

## 5 living docs (agent-essential minimum)

Everything else is nice-to-have. These 5 + README.org + CLAUDE.md are sufficient to implement or extend aq:

| Document | Owns | Update trigger |
|----------|------|----------------|
| spec-v2.org | Protocol spec | Broadcast schema changes |
| spec-v3-wire.org | Wire format (compact + JSON) | Field added/renamed |
| docs/INVARIANTS.md | Machine-verifiable contracts | Invariant added/removed |
| docs/adr/DOGFOODING.md | Empirical evidence behind design | Design rule added |
| docs/adr/TRANSPORT-CONTRACT.org | Version matrix, transport interface | Transport added/changed |

Reports (docs/research/) are point-in-time snapshots. Never updated.

## Open work (beads)

| Bead | Priority | Status | Description |
|------|----------|--------|-------------|
| aq-za9 | P1 | done (worktree) | Multi-codec stress harness |
| aq-8ue | P1 | done (merged) | Scrub real IPs from contrib docs |
| aq-ht4 | P1 | done (merged) | Scrub hostnames from docs/CLAUDE.md |
| aq-14i | P2 | open | Claude Code PostToolUse hook for auto-announce |
| aq-bqr | P2 | open | aq watch — filesystem watcher daemon |
| aq-dde | P2 | open | Guile Scheme port of aq |
| aq-qpa | P2 | open | Lean 4 proof of conflict detection correctness |
| aq-rnv | P2 | open | TLA+ spec of aq broadcast protocol |
| aq-tit | P2 | open | Cron/loop heartbeat re-announcement |
| aq-zgy | P2 | open | CI: GitHub Actions lint+test workflow + README badges |

## Known bugs

- `aq listen` agent identity is `local/unknown` when run outside a git repo (launchd starts in /)
- mDNS doctor check fails on macOS (expects `avahi-publish-service`, macOS uses `dns-sd`)
- Dict codec uses process-wide singleton — never desyncs in-process (needs per-instance state for real testing)
- Dependabot reports 5 vulnerabilities in otel-bridge transitive deps (gRPC, protobuf)
- Context limit for `aq quickstart` output not bounded

## Corpus stats (mini, 29 days)

413 archived broadcasts, 17 active. Time range: 2026-03-14 → 2026-04-13.

| Metric | Value |
|--------|-------|
| Hosts seen | host-a (60), host-b (33), unknown (318 = **77% pre-v3**) |
| Users | jwalsh (93), null (318) |
| Phases | proof (300), refinement (83), conjecture (29) |
| Wire size | min 168B, avg 291B, max 794B |
| Top conjecture | C-0 (140 = placeholder, UX debt) |
| Repos | 4+ distinct (aq, builder-workspace, http-axiom, dw-eval) |

The 77% identity gap is the empirical justification for v3 mandatory host/user.

## Session conventions

- `aq announce -c <conjecture> --claim "<intent>"` before starting work
- `aq check -c <conjecture>` while working — are others in the same space?
- `aq announce --status done` when finished
- `bd` for issue tracking (not TaskCreate/markdown)
- Git notes with X-Agent-Role, X-Conjectures, X-Testing trailers
- Never commit network info — `~/.aq/config.json` is the only place for real endpoints
- `/doc-check` skill validates docs against implementation
