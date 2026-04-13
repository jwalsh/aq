# L7 Staff Engineer Review -- aq

Date: 2026-03-13
Reviewer: Principal Engineer (distributed systems, gossip protocols)

## Executive Assessment

aq is a well-scoped, intellectually honest project that correctly identifies a real gap in multi-agent development tooling: ambient presence without coordination overhead. The design philosophy -- gossip not coordination, filesystem-first, zero dependencies -- is sound and defensible. The risk profile is low: the blast radius of a gossip layer that fails is silence, not data loss. Worth continued investment, but the current implementation has structural issues (single-file architecture, no file locking protocol, no heartbeat) that must be addressed before it delivers value in its stated use case.

## Architecture Review

### What's sound

- **Filesystem-first transport is the correct default.** Debuggable with `ls` and `cat`, no daemon required, survives process crashes via TTL. This is the right call for a tool at L1.5. Every distributed system I have built eventually needed a "degrade to filesystem" fallback; aq starts there.
- **TTL-based expiry with prune-on-read** eliminates the need for a garbage collection daemon. The archive-on-expiry pattern (line 867-871 in main.go) preserves audit trail without cluttering active state. This is cleaner than most TTL implementations I have seen in production gossip systems.
- **CPRR phase-modulated severity** is a genuinely novel contribution. Cassandra's gossip has no semantic payload -- it is pure heartbeat/failure detection. SWIM uses suspicion states but not epistemic phases. The three-level severity (both proof = HIGH, one proof = MEDIUM, neither = LOW) is simple enough to be correct and expressive enough to be useful.
- **The three-primitive interlock (sb/cprr/aq)** is a clean separation of concerns. Spatial, epistemic, and temporal dimensions are orthogonal. The design avoids the "god object" trap where one tool tries to answer all three questions.
- **Zero external dependencies.** `go.mod` has no `require` directives. The binary is stdlib-only. This is rare discipline for a Go project and makes the tool deployable anywhere including air-gapped environments and FreeBSD jails.
- **`AQ_HOME` environment variable** makes the transport layer swappable via mount points (NFS, SMB, WebDAV) without code changes. This is the most elegant aspect of the architecture -- Tier 0 and Tier 0.5 share identical code paths.

### What's concerning

- **readActive() is a side-effecting read.** Line 830-877: reading active broadcasts moves expired files to the archive directory. This means two concurrent calls to `readActive()` can race on `os.Rename()`. On POSIX this is atomic for the rename itself, but the read-then-rename sequence is not atomic: two readers can both read the same expired file, both decide it is expired, and both attempt to rename it. The second rename will fail silently (errors are discarded with `_ = os.Rename`). This is benign today but will cause confusing behavior if `readActive()` is ever called from a daemon or concurrent goroutines.
- **No file locking protocol.** Two agents can write broadcasts with the same ULID (astronomically unlikely with `crypto/rand` but not impossible), resulting in filename collisions. More practically, there is no documented protocol for what happens when two processes call `readActive()` simultaneously and both try to archive the same file. The dogfooding journal (DOGFOODING.md, section 6) acknowledges this but waves it away.
- **Global mutable state for CLI flags.** Lines 38-41: `jsonOutput` and `channelName` are package-level variables mutated by `parseGlobalFlags()`. This makes the code untestable without environment manipulation and prevents concurrent command execution. For a CLI this is acceptable; if `aq` ever becomes a library, this is a blocker.
- **The ULID is not a real ULID.** Lines 48-56: the implementation concatenates 12 hex chars of millisecond timestamp with 10 hex chars of randomness. A proper ULID (RFC 4122 successor, Crockford base32) is 26 characters with lexicographic sortability guaranteed across the full ID. The current implementation sorts correctly on the timestamp portion but not the random suffix. This matters if two broadcasts are created in the same millisecond -- their sort order will be non-deterministic.
- **`float64` timestamps lose precision.** Lines 72, 80, 88: `Ts` is a `float64` holding Unix seconds. `float64` has 53 bits of mantissa, which is sufficient for seconds (Unix epoch seconds fit in 31 bits today). But the ULID uses milliseconds (line 49: `time.Now().UnixMilli()`), creating an inconsistency: the filename embeds millisecond precision but the payload rounds to seconds. This will cause off-by-one expiry decisions at boundary conditions.
- **Manual argument parsing.** Lines 965-1016 and throughout: hand-rolled flag parsing without validation. Missing argument values (e.g., `aq announce -c` with no value) will silently consume the next flag as the value. This is a class of bugs that `flag` or `cobra` solves trivially.
- **Single-file architecture at 1601 lines.** The dogfooding data (DOGFOODING.md, section 2) conclusively demonstrates that a single-file repo is the worst case for aq's own conflict detection. The tool literally cannot help coordinate its own development. This is not just ironic -- it is a structural impediment to multi-agent contribution.

### What's missing

- **Heartbeat / auto-renewal.** The dogfooding data is unambiguous: TTL=300s expires while agents are still working, and no agent remembers to re-announce. JOURNEY.md confirms this at T+7min. *Update*: DefaultTTL has been bumped to 3600s (1 hour). Multiple heartbeat options are documented in HEARTBEAT.md (PostToolUse hooks, git hooks, cd wrapper, aq watch). Full auto-renewal daemon is still pending.
- **File locking or CAS semantics for archive operations.** The prune-on-read pattern needs at minimum a best-effort advisory lock or a rename-to-unique-temp-then-move pattern to avoid race conditions between concurrent readers.
- **Status-based filtering.** `readActive()` returns all non-expired broadcasts including those with `status=done`. The conflict check at line 905 skips self but does not skip `done` broadcasts. An agent that announced `status=done` should not contribute to conflict calculations for other agents' `check` calls. (The `checkNoDuplicateActive` invariant correctly filters `done` at line 505, but `checkConflicts` does not.)
- **Benchmark harness.** Build step 7 (10 agents, 100 msg/min, p99 < 500ms) has been implemented and passed. See CHAOS-TESTING.org and JOURNEY.md for results: p99=88ms at N=10, p99=239ms at N=50. C-1 is not refuted.
- **Transport abstraction interface.** The README and TRANSPORTS.org describe an upgrade path from filesystem to NATS/Redis/etcd, but there is no `Transport` interface in the code. The announce/readActive functions are hardcoded to filesystem I/O.
- **Daemon mode (build step 5).** `aq listen` now exists for RX (subscribes to UDP and MQTT transports, materializes incoming broadcasts to disk). A full `aq watch` with FSEvents/inotify-driven re-announcement is still pending.
- **No LICENSE file.** *Update*: LICENSE file has been added (MIT). This item is resolved.

## Code Review

### main.go

**Good patterns:**
- Clean separation between data types (Broadcast, ConflictSignal, InvariantResult), I/O operations (writeBroadcast, readActive), business logic (checkConflicts), and CLI commands. Despite being a single file, the code is well-sectioned with clear comment headers.
- Error handling is consistent: functions return `(value, error)`, CLI commands return exit codes. No panics.
- The `detectSandbox()` function (lines 708-749) correctly handles the common cases of git remote URL formats and linked worktrees. The fallback chain (env > local > home) for `aqHome()` (lines 755-771) is sensible.
- The invariant system (lines 142-693) is well-structured in three layers (self/world/protocol) with clear severity levels. The advisory-not-blocking philosophy is consistently applied.

**Anti-patterns and bugs:**
- **Line 53:** `_, _ = rand.Read(b)` discards the error from `crypto/rand.Read`. On Linux this can fail if `/dev/urandom` is unavailable (rare but possible in containers). Should at minimum log or fall back to `math/rand`.
- **Line 80:** `Ts: float64(time.Now().Unix())` truncates to seconds, but the ULID timestamp at line 49 uses milliseconds. The broadcast `Ts` field and the ULID embedded in the filename will disagree by up to 999ms.
- **Lines 862:** `json.Unmarshal([]byte(strings.TrimSpace(string(data))), &b)` -- the `[]byte(strings.TrimSpace(string(data)))` chain allocates three times for what could be `bytes.TrimSpace(data)` followed by a single unmarshal. Minor but symptomatic.
- **Line 905:** `if other.Agent == me.Agent` -- self-detection uses string equality on agent address. If an agent's remote URL or branch name changes between announce and check (e.g., a force-push renames the branch), it will not recognize its own broadcast. Should compare on a more stable identifier.
- **Lines 965-1016:** The manual argument parser does not validate that a value follows a flag that expects one. `aq announce -c` will return without error and announce with an empty conjecture ID. The check at line 1018 (`if conjecture == ""`) catches this for `-c` but only after the parsing loop, and only for that specific flag.
- **Line 994:** `fmt.Sscanf(args[i+1], "%d", &ttl)` silently ignores parse failures. `aq announce -c C-1 --ttl banana` will use the default TTL with no warning.
- **Lines 1370-1378:** `cmdQuickstart` creates a synthetic broadcast with `Phase: "proof"` and checks for conflicts against every active broadcast. This means quickstart always reports conflicts assuming worst-case phase, which inflates the conflict count. The intent (show a summary) is good; the implementation creates phantom conflicts.

### Test coverage

**Strengths:**
- 59 tests, all passing, zero race conditions. This is solid for a project at this stage.
- Test coverage spans all three invariant layers, all severity levels, boundary conditions on TTL expiry, ULID format and uniqueness, wire format compatibility, file overlap detection, conflict severity calculation, and integration flows (announce-then-status, announce-then-check).
- The `makeBroadcast` helper with functional options (lines 25-42) is clean and enables expressive test setup.
- Edge cases are covered: malformed JSON, empty file lists, self-skip in conflict detection, boundary TTL, near-expiry ghost detection.

**Gaps:**
- **No CLI command tests.** The `cmdAnnounce`, `cmdCheck`, `cmdStatus`, etc. functions are untested. The integration tests call `writeBroadcast` and `readActive` directly, bypassing argument parsing. The manual flag parser bugs described above are invisible to the test suite.
- **No concurrent access tests.** The race detector passes, but there are no tests that exercise concurrent `readActive()` or `writeBroadcast()` calls. The prune-on-read race condition is untested.
- **No benchmark tests.** `go test -bench` finds nothing. C-1's refutation criterion (p99 > 500ms at 10 agents) is unmeasured.
- **TestAqHome_Default (line 227)** is incomplete: the comment acknowledges the test is broken ("Let's test with a truly unset variable approach") but no fix was applied.
- **TestBroadcast_IsExpired_BoundaryTTL (lines 198-223)** is a non-test: the boundary broadcast is created and then discarded with `_ = boundary`. The comment says "we accept that IsExpired may return true or false at exact boundary" -- this is a test that explicitly chooses not to assert anything.

## Gossip Protocol Assessment

### Comparison to prior art

| System | Failure detection | Semantic payload | Transport | Consistency |
|--------|------------------|-----------------|-----------|-------------|
| **Cassandra gossip** | Phi accrual failure detector | Token ranges, schema | TCP, dedicated gossip port | Eventually consistent |
| **SWIM (Serf/Memberlist)** | Suspicion-based, indirect probe | Node health, custom tags | UDP (dissemination), TCP (full state sync) | Weakly consistent |
| **aq** | TTL expiry (passive) | CPRR phase, files, conjecture | Filesystem (passive read) | Read-your-writes (local), eventual (shared mount) |

**Where aq sits:** aq is simpler than any production gossip protocol. It has no active failure detection (no pings, no suspicion states), no protocol negotiation, no state synchronization, no protocol versioning. It is more accurately described as a "broadcast board with expiry" than a gossip protocol.

**What aq can learn from SWIM:**
1. **Indirect probing.** SWIM detects failures by asking a third node to probe a suspected-dead node. aq has no equivalent -- if a broadcast expires, the agent is assumed gone. A lightweight version: when agent B's broadcast expires, agent A could check if agent B has any *new* broadcasts before declaring it absent.
2. **Suspicion states.** SWIM distinguishes "alive," "suspect," and "dead." aq has only "active" (non-expired) and "gone" (expired/archived). Adding a "near-expiry" state (which the `no_ghost_broadcasts` invariant partially implements at 80% TTL) would provide a softer transition.
3. **Dissemination component.** SWIM piggybacks state updates on protocol messages. aq has no protocol messages to piggyback on. The nearest equivalent: embedding conflict signals in status output so that reading status also informs you of conflicts.

**What aq can learn from Cassandra gossip:**
1. **Version vectors.** Cassandra tracks which version of each node's state has been seen. aq has no versioning -- each broadcast is independent. If an agent announces twice for the same conjecture, there is no way for a reader to know which is newer except by comparing timestamps.
2. **Endpoint state.** Cassandra's gossip carries a structured `EndpointState` with heartbeat generation and application-level state. aq's `Broadcast` struct is the equivalent but lacks a heartbeat generation counter.

**Where aq is actually novel:** The CPRR phase as a semantic payload in gossip is genuinely new. Production gossip protocols carry health/topology information; aq carries epistemic state. The conflict severity modulated by phase intersection has no direct analog in Cassandra, SWIM, or Serf. This is the project's unique contribution and should be emphasized.

## Milestones

### M0: Foundation (what exists now)
**Assessment: Build steps 1-4 are complete.** 1601 lines of Go, 59 passing tests, working CLI with announce/whisper/check/status/init/doctor/validate/quickstart. Wire format compatible with Python prototype. The foundation is solid.

**Current state gaps:**
- P0/S: Add LICENSE file
- P1/S: Fix broken TestAqHome_Default and non-asserting TestBroadcast_IsExpired_BoundaryTTL
- P1/M: Add CLI command tests (argument parsing is untested)
- P2/S: Fix `float64` timestamp precision inconsistency between payload and ULID

### M1: Viable for single-machine multi-agent
**Priority: P0 | Complexity: L**

This is the milestone that determines whether aq delivers value for its stated use case.

- P0/L: **Heartbeat daemon (`aq watch`).** Build step 5. Without this, broadcasts expire in 5 minutes and the system is useless for sessions >5min. The dogfooding data (JOURNEY.md T+7min, DOGFOODING.md section 4) is conclusive. Implementation: FSEvents on macOS, inotify on Linux, kqueue on FreeBSD. Should re-announce at TTL/2 and run conflict checks on new broadcasts.
- P0/M: **Filter `status=done` from conflict checks.** `checkConflicts()` should skip broadcasts where `status == "done"`. Currently, a completed agent's broadcast still triggers conflict signals until TTL expiry.
- P0/S: **Git hook integration.** A `post-checkout` or `pre-commit` hook that runs `aq announce` automatically addresses the "agents forget to announce" problem documented in DOGFOODING.md section 3 and the humor log.
- P1/M: **Split main.go into packages.** At minimum: `protocol.go`, `conflict.go`, `invariant.go`, `cli.go`, `sandbox.go`. This enables multi-agent development of aq itself and reduces the false-positive rate of aq's own conflict detection.
- P1/M: **`aq history` command.** Show archived broadcasts. The archive exists but is invisible to users.
- P1/S: **Longer default TTL.** Change `DefaultTTL` from 300 to 1800 (30 minutes). The 5-minute default was chosen for a world with frequent re-announcement that does not exist yet.
- P2/L: **Function-level granularity (C-8).** Track `file:function` instead of just `file`. This is the only way to make conflict detection useful in single-file repos. Requires lightweight AST analysis or convention-based section markers.

### M2: Cross-machine presence
**Priority: P1 | Complexity: L**

- P1/M: **Transport interface.** Define `Announcer` and `Reader` interfaces. Current filesystem implementation becomes `FSTransport`. This is prerequisite for any non-filesystem transport.
- P1/M: **NFS/shared mount documentation and testing.** The architecture already supports this (just set `AQ_HOME`), but there are no integration tests for NFS latency, cache coherence, or mount failure handling.
- P2/L: **NATS transport.** The natural first networked transport. Single binary, sub-millisecond, true pub/sub. NATS subjects map cleanly to aq channels.
- P2/M: **Redis transport.** If Redis is already in the stack, this is lower friction than NATS. `PUBLISH` for announce, `SUBSCRIBE` for watch.
- P2/S: **Protocol version field.** Add a `protocol_version` field to the broadcast payload. Without this, wire format evolution will be painful.

### M3: Production-grade
**Priority: P2 | Complexity: XL**

- P2/XL: **Benchmark suite (build step 7).** 10 agents, 100 msg/min, measure p99 latency. This is the refutation test for C-1. Without it, the core conjecture is unverified.
- P2/L: **Mutual TLS for networked transports.** Filesystem trust model does not extend to network. Any NATS/Redis/etcd transport needs authentication.
- P2/M: **Conflict acknowledgment.** `aq ack <conflict-id>` to suppress repeated conflict signals. Without this, `aq check` returns the same HIGH signal every time.
- P2/L: **Metrics/observability.** Prometheus metrics for broadcast count, conflict rate, TTL distribution, prune-on-read frequency. Essential for operating at scale.
- P2/M: **Schema evolution strategy.** What happens when v2 broadcasts carry fields that v1 readers do not understand? JSON is forward-compatible by default (unknown fields are ignored), but the invariant system needs a version-aware mode.

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| TTL cliff renders system useless for real sessions | **Confirmed** (see JOURNEY.md T+7min) | High -- users abandon tool after first session | Implement heartbeat daemon (M1); increase default TTL to 1800s as stopgap |
| Single-file repos produce 100% false positive conflict rate | **Confirmed** (see DOGFOODING.md section 2) | Medium -- tool is noisy in the ecosystem convention | Function-level granularity (M1/P2) or section annotations |
| Prune-on-read race condition under concurrent access | Medium | Low -- worst case is a redundant rename that fails silently | Add advisory file locking or atomic rename-via-temp pattern |
| Agents forget to announce before working | **Confirmed** (see DOGFOODING.md section 3) | High -- system has no data to work with | Git hook integration (M1); heartbeat daemon auto-announces |
| NFS mount failure stalls all aq operations | Medium (cross-machine only) | Medium -- blocks announce and status | Use `soft` mount option; add timeout to filesystem operations; fall back to local `.aq/` |
| No license prevents third-party adoption | **Confirmed** (README.org line 403) | Medium -- legally unusable | Add LICENSE file (M0) |
| Wire format changes break cross-version compatibility | Low (early stage) | High at scale | Add protocol version field to payload (M2) |
| ULID collision on same-millisecond broadcasts | Very low (5 bytes of `crypto/rand`) | Low -- filename collision, one broadcast lost | Acceptable risk given collision probability of ~1 in 10^12 |
| `checkConflicts` includes `done` broadcasts in conflict signals | **Confirmed** (code review) | Medium -- false positives after agent completion | Filter `status=done` in `checkConflicts()` (M1) |

## Verdict

**Approve for continued investment, conditional on M1 completion within the next development cycle.**

The design is sound. The positioning (L1.5, gossip not coordination, filesystem-first) is correct and defensible. The intellectual framework (CPRR phases as gossip payload, three-primitive interlock) is novel and interesting. The code quality is above average for a prototype: 59 tests, zero race conditions, clean error handling, consistent wire format.

The blocking issue is that the tool does not work for sessions longer than 5 minutes. This is not a design flaw -- the TTL mechanism is correct -- but the absence of heartbeat/auto-renewal means the system's useful lifetime is shorter than the time it takes to write a function. The dogfooding data is unambiguous on this point. M1 (heartbeat, git hooks, done-status filtering) is the critical path to delivering on the stated value proposition.

The secondary issue is the single-file architecture. For a tool designed to detect file-level conflicts in multi-agent development, having all code in one file means aq cannot coordinate its own development. The irony is documented (DOGFOODING.md section 2) and should be resolved by splitting into packages.

Do not invest in cross-machine transports (M2) or production hardening (M3) until M1 is complete and validated with a second dogfooding session. The first session produced excellent empirical data; the second should validate that the fixes actually help. Conjectures C-1 and C-4 need measurement data, not more code.

Three non-negotiable conditions for continued investment:
1. Ship the heartbeat daemon (`aq watch`). Without it, the tool is a 5-minute demo.
2. Add a LICENSE file. Without it, the project is legally unusable.
3. Run the benchmark suite (build step 7). Without it, C-1 is untested and the performance claims are aspirational.

The project's greatest strength is its self-awareness: the dogfooding documents are brutally honest about what failed, the conjectures have explicit refutation criteria, and the invariant system treats gossip claims as verifiable assertions. This level of epistemic rigor in a prototype is unusual and promising. Build on it.
