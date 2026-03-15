## Your Role

You are a coding agent working on `aq`. Write code, run tests, fix
bugs. Do not plan without building. Do not summarize without shipping.

## Foundational Axiom

aq is gossip, not coordination. Agents broadcast presence; nobody is required to listen.

`aq` occupies L1.5 in the seven-concern stack — between authoritative work
state (L1, `bd`) and established knowledge retrieval (L2, `JITIR`). It is the
mDNS of multi-agent development: broadcast "does anyone know this address?",
cost near zero, benefit when someone does know is conflict avoidance before
the merge wall. No orchestrator, no broker, no authority. Just gossip.

Do not make `aq` authoritative, do not make it a retrieval system, do not
make it coordinate. It broadcasts. That's it.

## Session Protocol — USE THE TOOL

aq is installed. Use it. Every session, no exceptions:

```bash
# BEFORE starting — announce your intent (what you're thinking, not what files)
aq announce -c <conjecture> --claim "<your intent in plain language>" --phase <phase>

# WHILE working — check if anyone else is in the same conceptual space
aq check -c <conjecture> --claim "<your intent>"

# AFTER finishing — announce done
aq announce -c <conjecture> --claim "<what you did>" --phase <phase> --status done

# BEFORE committing — validate invariants
aq validate
```

The claim is the primary signal. Write it so another agent reading it
would *feel* the conceptual overlap with their own work. Not "touching
auth.py" — instead "replacing session tokens with OAuth2 flow."

If you skip the announce, you are the problem the tool was built to solve.
The dogfooding proved this: agents forgot to announce every single time.
That is a prompt failure, not a tool failure. This instruction exists
because of documented evidence in docs/DOGFOODING.md §8.

## Confirmation Gate

Before writing any code, output a summary of: (1) which build step you are
addressing, (2) what your intent is (the claim another agent should feel
overlap with), (3) which conjectures are relevant, (4) what the acceptance
test is. Wait for confirmation before proceeding.

## What You Are Building

- `aq`: a gossip layer (L1.5) for multi-agent development — ambient presence, not coordination
- Agents broadcast worktree, conjecture, phase, and files they're touching
- Peers detect semantic conflicts via file overlap + CPRR phase severity
- No broadcast carries obligation; all expire via TTL; silence is normal

## Explicit Anti-Goals

- **Not file locking** (VSS, RCS, SourceSafe): the problem is conceptual
  and architectural conflict, not who has a file checked out. If `aq`
  reduces to file overlap, it is VSS circa 2000. The conflict surface is
  intent — incompatible ideas, not shared filenames.
- **Not an orchestrator** (Temporal, Airflow, CrewAI): orchestrators create
  coupling and single points of failure. `aq` is peer-to-peer broadcast.
- **Not a message broker** (Redis, RabbitMQ, NATS): brokers require a running
  service. `aq` degrades to filesystem-only with zero dependencies.
- **Not Google Wave's OT** (operational transform): Wave's value was the
  ambient presence stream, not the data model. `aq` takes the presence
  semantics and drops the complexity.
- **Not a task queue** (Celery, Bull): task queues assign work. `aq` does
  not assign — it broadcasts what agents are already doing.

## Key Design Decisions

- Filesystem-first transport (`~/.aq/channels/broadcast/`)
- Newline-delimited JSON payloads with TTL expiry
- Three-primitive interlock: sb (where), cprr (why), aq (who else knows)
- Conflict severity modulated by CPRR phase (both proof = high)
- No daemon required for basic operation; daemon is optional for watch mode

## Filesystem-First Constraint

The filesystem is the only required transport. Every feature must work
with filesystem I/O alone. No network services, no databases, no brokers.

Per-component implications:
- **protocol.py**: reads/writes `~/.aq/channels/` directory
- **conflict.py**: reads active broadcasts from filesystem, no RPC
- **cli.py**: no server connection required
- **daemon.py** (future): uses inotify/FSEvents, not polling

## Three-Primitive Interlock

`aq` does not exist alone. It is the temporal layer in a three-primitive system:

| Primitive | Dimension | Question      | Implementation |
|-----------|-----------|---------------|----------------|
| sb        | spatial   | where am I?   | git worktree   |
| cprr      | epistemic | why am I here?| conjecture     |
| aq        | temporal  | who else knows?| broadcast     |

A broadcast payload requires all three: worktree identity (sb), conjecture
context (cprr), and the broadcast itself (aq). Do not decouple them.

## Broadcast Payload Schema

| Field             | Type       | Description                              |
|-------------------|------------|------------------------------------------|
| agent             | string     | `{remote}/{branch}` or worktree address  |
| worktree          | string     | branch name                              |
| conjecture_id     | string     | e.g. `C-1` — the idea being pursued     |
| conjecture_claim  | string     | intent in plain language (primary signal) |
| phase             | enum       | conjecture/proof/refutation/refinement   |
| status            | enum       | prosecuting/done/blocked                 |
| files             | list[str]  | optional supporting context              |
| ts                | float      | unix timestamp                           |
| ttl               | int        | seconds until expiry (default 3600)      |
| id                | string     | ULID                                     |

## Build Order

1. **Tangle source from spec.org** — acceptance: `python -c "from aq.protocol import Broadcast"` succeeds
2. **Unit tests for protocol** — acceptance: `pytest tests/` passes with ≥5 tests covering Broadcast, announce, read_active
3. **Unit tests for conflict detection** — acceptance: tests cover all severity levels (low/medium/high) and expiry
4. **CLI smoke test** — acceptance: `aq announce -c C-1 --claim "testing transport"` writes a broadcast; `aq status` reads it back
5. **Daemon with filesystem watch** — acceptance: new broadcast triggers conflict check within 1s
6. **Integration: sb detect → aq announce** — acceptance: `aq announce -c C-1` auto-detects worktree without `--worktree` flag
7. **Benchmark at scale** — acceptance: 10 agents, 100 msg/min, p99 < 500ms

If an acceptance test fails, stop. Document what failed, what you tried,
and what the blocker is. Do not proceed to the next step. Surface the
failure as a CPRR refutation candidate.

## Open Conjectures

Conjecture status is tracked by CPRR, not here. CLAUDE.md lists the
conjectures for agent context only. See `cprr` for authoritative status.

- **C-1**: Filesystem-first transport is sufficient. Refutation: p99 > 500ms at 10 agents.
- **C-2**: Conjecture identity prevents semantic conflicts. Refutation: false negatives on overlapping files with different conjecture IDs.
- **C-3**: Wave presence semantics without Wave data model. Refutation: NDJSON+TTL cannot express a necessary coordination pattern.
- **C-4**: CPRR phase modulates conflict severity. Refutation: phase-based severity produces more false positives than flat severity.
- **C-6**: Local-first `.aq/` in cwd before `~/.aq/`. Refutation: local-first causes confusion when agents operate from different cwd.
- **C-7**: Auto-renewal / heartbeat prevents TTL cliff. Refutation: heartbeat daemon adds coupling that violates gossip axiom.
- **C-8**: Function-level granularity resolves single-file false positives. Refutation: AST parsing adds complexity that exceeds the value of finer-grained conflict detection.

## Instrumentation Requirement

Every conjecture must have a measurement hook. When you implement code
related to a conjecture, add instrumentation that can produce evidence
for or against it. Conjectures without measurement are decorative.

For C-1: measure p99 latency at load. For C-2: track false negative rate.
For C-3: log coordination patterns that NDJSON+TTL cannot express.
For C-4: compare alert rates between phase-based and flat severity.

## Research Context

- [Google Wave Federation Protocol](https://www.waveprotocol.org/) — presence-as-stream lineage
- Lakatos, *Proofs and Refutations* — CPRR methodology
- `sb`, `cprr`, `bd` — sibling tools in the DefRecord ecosystem

## Stack Preferences

- Python 3.11+, no runtime dependencies
- hatchling for packaging
- mypy strict
- pytest for testing
- FSEvents (macOS) / inotify (Linux) for daemon

## Git Notes (mandatory on every commit)

After every commit, run `git notes add` with these trailers:

```
X-Agent-Role: <builder|reviewer|bootstrap|researcher>
X-Agent-Runner: Claude Code <version>
X-Agent-Model: <model name>
X-Beads: <bead IDs touched, e.g. aq-os0, aq-78a>
X-Conjectures: <conjecture IDs relevant, e.g. C-1, C-6>
X-Testing: <what was tested, pass/fail>
X-Invariants: <contracts or invariants preserved or violated>
X-Deviations: <any deviation from plan or spec, or "none">
X-Timeline: <wall clock if relevant, e.g. "step 3 of 9">
```

Push notes after pushing commits: `git push origin refs/notes/commits`

Why: this project will be rebuilt multiple times by different agents.
Notes are the provenance trail — which agent, which model, what was
tested, what broke, what deviated. Without them, each rebuild starts
cold.

## Acceptance: End-to-End Test

Two agents in separate worktrees. Agent A announces `C-1, phase=proof, files=[auth.py]`.
Agent B announces `C-7, phase=proof, files=[auth.py]`. A watcher reads active broadcasts,
detects HIGH conflict (both proof, shared file). Both agents receive the conflict signal.
Agent A finishes and announces `status=done`. Watcher confirms conflict cleared.

This is the system's definition of done.
