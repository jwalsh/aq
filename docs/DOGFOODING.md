# aq Dogfooding Observations

Agent Delta, 2026-03-13. Four agents building aq in parallel worktrees.

## 1. The Bootstrap Problem

We are four agents trying to use aq to build aq, but aq is a stub when
we start. The Python prototype exists and works, but has no tests. The
Go port is an empty shell with only `version` and `help` commands.

This creates a circular dependency: we need the tool to coordinate, but
the tool does not exist yet because we have not finished coordinating to
build it.

In practice, we bootstrapped by reading the Python source directly and
treating it as the specification. The `spec.org` literate document was
the contract. aq itself provided zero coordination value during its own
construction -- the gossip layer was silent because there was nothing to
gossip with.

**Observation**: Dogfooding a coordination tool during its own initial
implementation is not useful. The tool provides value only after it
exists. This is not a design flaw -- it is a phase constraint. aq will
be useful for the *second* round of changes, not the first.

## 2. The Single-File Paradox

All four agents must edit `main.go`. The aq conflict detection algorithm
is simple: shared files + CPRR phase = severity. When four agents all
touch `main.go` in `proof` phase, every pair is a HIGH conflict. That is
4-choose-2 = 6 HIGH conflict signals.

The heuristic is useless here. It produces all-HIGH all-the-time. There
is no signal -- just noise. The conflict detector cannot distinguish
between:

- Two agents editing the same function (real conflict)
- Two agents adding independent functions to the same file (no conflict)
- Two agents where one is writing tests and the other the implementation
  (complementary, not conflicting)

**What would work better**:

- **Function-level granularity**: Track which functions/symbols each agent
  touches, not just filenames. `main.go:cmdAnnounce` vs `main.go:cmdCheck`
  is not a conflict.
- **Section-based file splitting**: If the file is a single `main.go`,
  aq could track byte ranges or section markers. But this adds complexity
  that violates the gossip-not-coordination axiom.
- **Semantic intent**: Agents could broadcast not just files but intent
  categories (e.g., "adding CLI commands" vs "adding tests" vs "adding
  types"). Two agents both "adding CLI commands" is riskier than one
  adding commands and one adding tests.

The fundamental tension: file-level overlap is the cheapest heuristic
(just list filenames), but it is also the least informative for
single-file repositories. aq's design is optimized for the common case
(many files, few agents per file), not the degenerate case (one file,
many agents).

## 3. The Merge Prediction

Four worktrees, four branches, one file.

**Prediction**: At least 3 of the 4 branches will have merge conflicts
with each other. The conflicts will be in `main.go` and will be
structural (all agents adding different functions to the same file) not
semantic (agents changing the same logic in contradictory ways).

**Will aq's warnings have been useful?** No, for this specific case.
The warnings would have been "HIGH conflict with everyone, everywhere,
always." That is equivalent to no warning at all. A useful warning
system must have both true positives and true negatives. When the false
positive rate is 100%, the system is a constant.

However, aq's warnings *would* be useful for the post-merge phase: once
all four branches are merged and agents begin working on different
features touching different files, file-level overlap becomes meaningful
again.

## 4. The TTL Cliff

Default TTL is 300 seconds (5 minutes). For a coding session that runs
30-60 minutes, that means:

- Agent announces at T+0
- Announcement expires at T+5min
- For the remaining 25-55 minutes, the gossip layer has no memory of the
  agent's work

The CLAUDE.md spec says to "re-announce every TTL/2 while working," but
no agent implemented re-announcement. There is no daemon, no cron, no
heartbeat. The announce-once-and-forget pattern means aq's memory is
5 minutes long in a 30-minute session. That is embarrassing.

**Possible fixes**:

- **Longer default TTL**: 1800s (30 min) or 3600s (1 hour). Trades
  staleness risk for coverage.
- **Heartbeat daemon**: `aq watch` could re-announce periodically. But
  the spec says "no daemon required for basic operation."
- **Activity-based refresh**: Hook into git pre-commit or editor
  save events to re-announce automatically.
- **Explicit session model**: `aq begin` / `aq end` instead of
  ephemeral broadcasts. But this moves toward coordination, which
  violates the gossip axiom.

The 5-minute TTL was designed for a world where broadcasts are cheap and
frequent. In practice, agents announce once and then code for 30 minutes.
The design assumption (frequent re-announcement) does not match the
usage pattern (announce-and-forget).

## 5. What aq Got Right

Despite the problems above, several design decisions proved sound:

- **Filesystem-first transport**: No setup, no running services, no
  dependencies. `t.TempDir()` in tests gives perfect isolation. The
  filesystem is the simplest possible transport and it works.

- **NDJSON payload format**: Human-readable, machine-parseable, trivially
  debuggable with `cat`. No schema evolution headaches -- just add fields.

- **TTL-based expiry**: Even though the default is too short, the
  mechanism is correct. Expired broadcasts are archived, not deleted.
  The archive provides an audit trail without cluttering active state.

- **Severity modulated by phase**: The C-4 conjecture (CPRR phase
  modulates severity) is sound in principle. Both-proof-on-same-file is
  genuinely riskier than conjecture-on-same-file. The problem is not the
  heuristic but the granularity of "same file."

- **Zero-dependency Go binary**: stdlib only, single binary, cross-
  compiles. This is the right deployment model for a tool that must be
  present in every worktree.

## 6. What's Missing

### Commands we wished existed

- `aq watch`: Daemon mode that re-announces and checks conflicts
  periodically. Without this, TTL expiry makes the system useless for
  sessions longer than 5 minutes.
- `aq peers`: Show which agents are known (from the agents/ directory).
  Currently there is no discovery -- you only see broadcasts, not agents.
- `aq history`: Show the archive. Expired broadcasts have value for
  post-hoc analysis ("who was working on auth.go yesterday?").
- `aq ignore`: Mark certain conflict signals as acknowledged. Without
  this, the same HIGH signal repeats every time you run `aq check`.

### Behaviors that surprised us

- `readActive` moves expired files to archive as a side effect of
  reading. This means a read operation mutates the filesystem. It works,
  but it violates the principle of least surprise.
- There is no locking. Two agents calling `readActive` simultaneously
  could both try to move the same expired file. In practice, filesystem
  rename is atomic on most systems, so this is fine -- but it is
  undocumented.
- The ULID uses `crypto/rand` for the random portion, which is more
  expensive than necessary. The Python prototype uses `random.choices`,
  which is not cryptographically secure but is faster. For an ID that
  only needs to be unique (not secret), `math/rand` would suffice.

### Ergonomic papercuts

- `-c` is required for `announce` but the error message does not suggest
  the flag name.
- `--files` takes a comma-separated string, not repeated flags. This is
  a shell quoting hazard: `aq announce -c C-1 -f "a.go, b.go"` works
  but `aq announce -c C-1 -f a.go -f b.go` does not.
- No tab completion. For a CLI tool used frequently, this matters.
- `aq status` and `aq check` have overlapping concerns. It is not
  obvious when to use which.

## 7. Comparison to Alternatives

### Would Slack DMs have been better?

For four agents working for 30 minutes: yes, probably. Slack DMs are
persistent, human-readable, and require no setup. But Slack DMs do not
scale to 10+ agents, cannot be consumed programmatically, and require
a human to interpret "I'm working on auth.go" as a conflict signal.

aq's advantage over Slack is structured, machine-parseable broadcasts
with automatic conflict detection. The disadvantage is that aq requires
the tool to exist first (bootstrap problem) and the conflict heuristic
is too coarse for single-file repos.

### A shared Google Doc?

A Google Doc listing "Agent Alpha: main.go (tests), Agent Beta: main.go
(CLI commands)" would have provided more value than aq for this specific
session. But Google Docs require manual updates, have no TTL (stale
information persists forever), and cannot run conflict detection
algorithms.

### A Trello board?

Trello boards are task assignment tools (anti-goal: "not a task queue").
aq is explicitly not about assigning work. A Trello board would have
helped with coordination but would have violated the gossip-not-
coordination axiom.

### What does gossip provide that those don't?

- **No single point of failure**: If the Slack server goes down, DMs
  stop. If aq's filesystem is available, gossip continues.
- **Automatic expiry**: Stale information self-cleans. No human needs to
  update a Google Doc to say "I'm done."
- **Machine-parseable by default**: Other tools can consume aq broadcasts
  without screen-scraping or API integration.
- **Zero setup cost for agents**: No accounts, no invitations, no OAuth.
  Just write a JSON file to a directory.

The gossip model is correct for the steady-state case (many agents,
many files, ongoing work). It is less useful for the bootstrap case
(few agents, one file, building the tool itself). This dogfooding
exercise tested aq in its worst-case scenario and found it wanting --
but that is the expected result. The tool was designed for a different
operating point.

## 8. Hour 2 Failures: The Merge and Beyond

### The invariants agent discovered ghost broadcasts

After the 4-agent parallel build, an invariants agent was launched to add
advisory assertions. It discovered `no_ghost_broadcasts` -- checking whether
broadcasts reference files that don't exist. In the single-file convention
where all agents touched `main.go`, every broadcast referenced a real file.
But what about the broadcasts from the Python prototype? Those referenced
`auth.py` and `session.py` -- files that existed in the spec but never in
the Go repo. Ghost gossip about phantom files.

**Observation**: Invariants retroactively reveal that earlier broadcasts were
lying. The gossip layer happily broadcast claims about non-existent files.
Nobody noticed because nobody checked. The invariant system catches this --
but only if someone runs `aq validate`. Advisory warnings with no audience
are just logs.

### The WS-* déjà vu

A 2005 enterprise architect, reviewing DOGFOODING.md:

> "So let me understand. You built a message bus. It has:
>  - No guaranteed delivery          ✓ (we called this 'QoS 0')
>  - No schema registry              ✓ (we called this 'UDDI')
>  - TTL-based expiry                ✓ (we called this 'WS-Expiry')
>  - Broadcast semantics             ✓ (we called this 'WS-Notification')
>  - No transactions                 ✓ (we called this 'WS-AtomicTransaction, absent')
>  - File-level conflict detection   ✓ (we called this 'pessimistic locking, absent')
>  - Bootstrap problem               ✓ (we called this 'chicken-and-egg SOA governance')
>  - Single point of... wait, you have no single point of failure?
>
> Hmm. You may have actually gotten that one right.
>
> But your TTL cliff? We had that. We called it 'UDDI entry expiry'.
> Nobody re-registered their services either. The registry went stale
> in about 5 minutes. We wrote a 34-page WS-* specification about it.
> You wrote a TODO comment. Different era, same problem."

**Observation**: Every problem aq encountered has prior art in SOA governance
circa 2003-2008. The UDDI registry expiry problem is *exactly* the TTL cliff.
WS-Notification is *exactly* broadcast semantics. The difference: WS-* solved
these problems with 34-page specs and XML Schema. aq solves them with JSON
files in a directory. The failure modes are identical. The recovery cost is not.

### We forgot to announce (again)

After merging the invariants agent, committing the Wave protocol doc,
updating TRANSPORTS.org with the WS-* lineage, and pushing 8 commits
to main -- we realized we never ran `aq announce` for any of it. The
gossip layer was silent during 40 minutes of active work. We had the
tool. It was compiled. It was in the working directory. We just didn't
use it.

Retroactive announcements were added with `status=done`, which is
technically correct but defeats the entire purpose of ambient presence.
A broadcast that arrives after the work is finished is a press release,
not gossip.

**Observation**: The announce-before-working discipline requires
discipline that agents don't have. This is not an edge case -- it is
the default behavior. Every agent in every session will forget to
announce unless announcement is automatic. C-7 (auto-renewal /
heartbeat) is not a nice-to-have. It is the only viable path. Manual
gossip is an oxymoron.

Protocol gap #7: agent forgot to use the gossip tool while writing
documentation about gossip.

### The Wave protocol is dead but its ghost lingers

waveprotocol.org returns ERR_CONNECTION_CLOSED. The site is down. The primary
source for aq's spiritual ancestor is gone. We reconstructed the protocol
from Apache SVN whitepapers and secondary sources (see docs/WAVE-PROTOCOL.md).

**Observation**: Wave's presence-as-stream model is exactly what aq
implements, but Wave coupled it to OT, XMPP, Protocol Buffers, and a
five-layer data model. You couldn't get Wave's excellent presence semantics
without buying the entire editing stack. aq decouples presence from everything
else. That's the whole design thesis.

### New conjectures from the wreckage

| ID  | Conjecture | Refutation criterion |
|-----|------------|---------------------|
| C-6 | Local-first `.aq/` in cwd before `~/.aq/` | Local-first causes confusion when agents operate from different cwd |
| C-7 | Auto-renewal / heartbeat prevents TTL cliff | Heartbeat daemon adds coupling that violates gossip axiom |
| C-8 | Function-level granularity resolves single-file false positives | AST parsing adds complexity exceeding the value |

C-6 is already implemented. C-7 is build step 5. C-8 is the hardest and
most interesting -- it's where aq either becomes useful for the single-file
ecosystem convention or admits that file-level is the wrong abstraction.

## 9. First Successful Dogfood: Agent Used aq

An agent was tasked with reducing cyclomatic complexity in `cmdAnnounce`
(27 → 7) and `cmdCheck` (20 → 5). The CLAUDE.md session protocol now
says "USE THE TOOL" — and this agent did:

```
$ ./aq announce -c C-1 --claim "Reducing cyclomatic complexity" --phase refinement -f "main.go"
announced: C-1 -> aq-...json

$ ./aq check -c C-1 -f "main.go"
no conflicts detected

$ ./aq validate
5 passed, 2 warning(s), 0 error(s)

$ ./aq announce -c C-1 --phase refinement -f "main.go" --status done
announced: C-1 -> aq-...json
```

The agent announced before working, checked for conflicts, validated
invariants, and announced done. All 59 tests passed. The prompt
engineer was right: once CLAUDE.md says "use the tool," agents use it.

**Observation**: The first agent to successfully use aq during its own
work session was the one given explicit step-by-step instructions in
CLAUDE.md. The earlier agents had "build aq announce" as a build step
but not "run aq announce as part of your workflow." Prompt design, not
tool design, was the bottleneck. Protocol gap #7 is fixed by
documentation, not code.

## 10. The Intentional Collision: Wire Format vs Strict Validation

Two agents were launched in parallel worktrees with deliberately
incompatible features:

- **Agent A** (C-3, proof): Add a `severity` field to the broadcast
  wire format — every JSON file gets `"severity": "none|low|medium|high"`
- **Agent B** (C-2, proof): Add `DisallowUnknownFields()` to
  `readActive()` — reject any broadcast with fields not in the struct

The collision:

```
# Agent A's broadcasts appeared with the new field:
$ ./aq status
warning: skipping aq-...json: json: unknown field "severity"
```

The current binary (neither A nor B merged) was already dropping
Agent A's broadcasts because Go's `json.Unmarshal` is lenient by
default but the broadcasts had a field that triggered warnings in
status display. After Agent A rebuilt its binary, its status showed
`severity=high` — it detected the conflict with Agent B.

`aq check` correctly flagged HIGH (both proof, shared main.go). But
it couldn't explain *why* they conflict:

- If B merges first: A's broadcasts get silently rejected by every reader
- If A merges first: B's strict validation rejects A's new field
- Either way: dsp-dr's Scheme port (scheme/aq.scm, bead aq-dde)
  would choke on or silently ignore the new field

**The triple collision**: Go wire format change (A) vs Go strict
validation (B) vs Scheme port wire compatibility contract (dsp-dr).
Three consumers, one schema, zero coordination.

**What aq detected**: HIGH conflict on main.go (correct)
**What aq couldn't detect**: the *semantic* incompatibility — that
A adds a field and B rejects unknown fields. File-level overlap was
the right signal for the wrong reason.

**Observation**: This is the strongest evidence yet for C-8
(function-level granularity). `main.go:readActive` vs
`main.go:Broadcast` vs `main.go:cmdAnnounce` would have told the
whole story. `main.go` vs `main.go` tells you nothing you didn't
already know.

**Resolution**: Neither feature should merge as-is. The correct fix
is: add the severity field to the struct (A's change), then strict
validation works because the field is known (B's change becomes
compatible). Order matters. aq can't tell you the order. A human
reading the conflict signal can.

This is the gap: gossip detects *that* agents conflict, not *how*
to resolve it. Gossip is presence, not coordination. The axiom
holds — but the axiom has limits.

## 11. External Review: ARIA Bootstrap Protocol

An external agent reviewed the ARIA bootstrap protocol (the meta-protocol
for standing up agent projects like aq) against our dogfooding data. The
review validates nearly every failure we documented and identifies where
the bootstrap protocol itself is broken. Key findings:

Source: external agent reviewing the ARIA bootstrap protocol gist
against aq dogfooding data. Full analysis confirmed: Phases 1-9 are
well structured, confirmation gate is present, failure handler is
explicit, anti-patterns table is solid. The problems are in what's
missing.

### What the dogfooding broke in the bootstrap protocol

1. **Phase 9 (parallel agents) is dangerously naive.** "Launch parallel
   agents: one per conjecture, one per component" with no mention of the
   bootstrap paradox, TTL cliff, or worktree address instability. Our
   session tested this on itself and failed in three documented ways
   (§1, §2, §4). Phase 9 needs a "known failure modes" subsection
   pointing at our JOURNEY.md patterns.

2. **No presence layer in Phase 1.** The bootstrap sequence sets up `git`,
   `bd`, `sb`, `cprr` but nothing coordinates agent awareness during
   parallel work. Should add `aq init && aq announce` to Phase 1.
   The absence of presence is what produces Humor Log #4 ("Gossip 0,
   Chaos 1").

3. **Phase 6 memory files are pre-JITIR.** `user_role.md`,
   `project_state.md` are hand-written files that go stale. The actual
   L2 memory layer is JITIR (sqlite-vec retrieval). The protocol only
   describes session memory (`.claude/` files), not corpus memory.

4. **No empirical update cycle.** Phase 4 review happens before any code
   runs. There's no "Phase 10: after first dogfooding run, update
   conjecture statuses and open new conjectures for observed failure
   modes." This is the CPRR loop failing to close. We produced C-1
   partially refuted, C-2 refuted, C-4 partially refuted in 58 minutes
   -- and the protocol has no phase for absorbing that.

5. **Conjectures treated as backlog items.** `cprr add` appears alongside
   `bd create` as if they're the same thing. A bead is a task
   (authoritative, L1). A conjecture is an epistemic frame (L3).
   Conjectures are not done when their build step merges; they're done
   when their measurement hook has produced data.

### The anti-pattern we should have named

| Anti-pattern | Description |
|-------------|-------------|
| Reinventing WS-* | If your protocol is structurally equivalent to UDDI (discovery), WS-Notification (broadcast), WS-Coordination (consensus), or WS-AtomicTransaction (distributed commit) — and it probably is — name the prior art and explain why the 2005 version failed. The failure reasons usually still apply. |
| Self-bootstrapping | This protocol cannot bootstrap itself. Gossip was silent during its own construction. The first run is always manual. Phases 1-3 are necessarily human-driven. The automation starts at Phase 4. The ouroboros is the design, not a bug. |

### The protocol has no transport layer

The bootstrap protocol defines what agents should do but not how they
communicate while doing it. No transport or communication layer is
specified. This is the gap aq is supposed to fill — but aq itself isn't
in the bootstrap sequence. The protocol assumes agents coordinate by...
not coordinating. Which is fine until Phase 9 launches parallel agents
and they all touch `main.go`.

### CPRR is too shallow in the protocol

Conjectures should be the epistemic frame for *all* work, not just
backlog items appended to `bd create`. Every build step should have an
associated conjecture. Every merge should update conjecture status.
The protocol currently treats CPRR as a checkbox ("run `cprr add`")
rather than the operating system for epistemic state.

The drift signal: when you start solving infrastructure problems (how
do I transport messages? how do I handle TTL?) instead of coordination
problems (do agents know about each other?), you are reinventing WS-*.
The WS-* specs solved infrastructure beautifully. They solved
coordination not at all. aq should stay on the coordination side of
that line.

### What the review got right about axioms

> "The axiom must survive 8k token context compression, which means it
> should be both short *and* non-obvious. 'Don't build a task queue' is
> a good axiom because it's counterintuitive. 'Build good software' is
> a bad axiom because it compresses to nothing."

Our axiom — "aq is gossip, not coordination" — survives compression
because it's counterintuitive. You'd expect a multi-agent tool to
coordinate. Saying it doesn't is the information-carrying signal.

### Seven Concerns gap

The review notes that an agent building at L5 (SEFACA/control) needs
different framing than one at L1 (bd/work state). CLAUDE.md should have
a one-liner: `## Position in Seven Concerns` with
`L[N]: [Concern] — answers: [question]`. We have this in README.org's
full table but not in the agent-facing CLAUDE.md template.

### Cross-repo and cross-machine: it just works (and it just false-positives)

Tested `aq` across sibling repos (`../sb`, `../cprr`, `../aq`) using the
global `~/.aq/` channel. Three agents in three different repos all announced.
`aq status` showed the full ecosystem at a glance:

```
github.com/jwalsh/aq/main      C-1  [proof]       main.go
github.com/jwalsh/sb/main      C-6  [conjecture]  main.go
github.com/jwalsh/cprr/main    C-4  [proof]       main.go
```

Then `aq check` flagged HIGH between aq and cprr (both proof, shared
`main.go`) — but they're different repos. The file overlap heuristic
can't distinguish `aq/main.go` from `cprr/main.go`. Same C-8 problem,
now cross-repo.

The multi-machine case is trivially supported: `AQ_HOME=/mnt/shared/.aq`
and every machine that can see the mount shares the channel. No code
changes. This is the Tier 0.5 row in TRANSPORTS.org — NFS, SMB, WebDAV,
S3-via-FUSE all work because `announce()` writes a file and `read_active()`
lists a directory.

What you'd hit on a shared mount:

- **TTL cliff is worse**: network latency eats into the 300s window
- **No auth**: filesystem permissions are the only ACL
- **No encryption**: broadcasts are plaintext JSON on the mount
- **Consistency**: NFS close-to-open is fine for 300s TTL; S3 eventual
  consistency could miss a broadcast for 1-2s

**Observation**: The design assumed this from day one — `AQ_HOME` is an
env var, not hardcoded. The filesystem abstraction *is* the transport
abstraction. Cross-machine presence works out of the box. Cross-repo
false positives do too. C-1 gets its real test on a shared mount, not
localhost.

### Stale binary, stale gossip (T+58min)

We tried to run `aq validate` to verify the SOA déjà vu additions.
Got `unknown command 'validate'`. The invariants agent added the
command to `main.go` and it was committed, but we never rebuilt the
binary. The binary on disk was from the pre-invariants build. `go build`
fixed it. `aq validate` then passed 7/7 invariants.

**Observation**: A gossip tool that requires manual compilation after
every code change has the same problem as manual `aq announce` — nobody
remembers to do it. The `printUsage()` help text also didn't list
`validate` even though the command existed. Two invisibility failures:
the binary was stale, and the help text omitted the command. For the
rebuild: `make build` should be a pre-commit hook, and help text should
be generated from the command dispatch table, not hand-maintained.

Protocol gap #8: stale binary = stale capabilities. The tool had
invariants. The running copy didn't know.

### What this means for the rebuild

This session's data — the failures, the humor log, the protocol gaps,
the external review — is the input for the next version of the bootstrap
protocol. The spec (spec.org) was what we thought we wanted to build.
DOGFOODING.md is what we actually learned. The next spec starts here.

## Summary

aq's design is sound for its intended use case (ambient presence in
multi-agent, multi-file development). The dogfooding exposed three real
issues: the bootstrap problem (unavoidable), the single-file granularity
problem (addressable with function-level tracking), and the TTL cliff
(addressable with a heartbeat daemon or longer defaults). The
filesystem-first transport, NDJSON format, and phase-based severity
heuristic all proved their worth. The tool needs to exist before it can
be evaluated fairly -- and now it does.

Post-merge, two more issues surfaced: ghost broadcasts (claims about
non-existent files), the SOA déjà vu (every aq problem has WS-*
prior art), and an agent that forgot to announce while documenting
gossip. The invariant system catches the first. History catches the
second. Irony catches the third.

External review of the bootstrap protocol confirmed: the dogfooding
data is load-bearing for the next version. The spec was pre-empirical.
This document is the empirical record. The rebuild starts from here.

### §12: Build Step 7 — Chaos Test Results (2026-03-14)

Build step 7 acceptance criterion: "10 agents, 100 msg/min, p99 < 500ms."

We built a chaos test suite (`contrib/chaos/`) that shells out to the
`aq` binary for all operations. Six scenarios, all passing:

| Scenario | Result | Key Metric |
|----------|--------|------------|
| Sustained Load (10 agents) | PASS | p99 = 77ms |
| Burst Storm (500 announces) | PASS | 500/500 visible |
| Conflict Detection | PASS | Self-exclusion correct |
| TTL Churn (3s TTL) | PASS | Appeared <2s, expired on time |
| Archive Flood (200 broadcasts) | PASS | 200/200 archived |
| Fan-Out Scaling (2→50 agents) | PASS | p99 = 239ms at N=50 |

**C-1 is not refuted.** Filesystem transport handles 50 concurrent agents
at p99 < 250ms. The refutation criterion (p99 > 500ms at 10 agents) is
not triggered even at 5x the target scale.

Protocol gap #9: **Subprocess overhead dominates latency.** Each `aq announce`
spawns a process (~60ms on M4). The actual file write is ~68μs (measured
by Go benchmarks). If aq becomes a long-running daemon or library, the
500ms budget has ~3 orders of magnitude of headroom.

Protocol gap #10: **`go vet` caught a lock copy bug in benchmark code.**
Agent-generated test code included `_ = mu` which copies a `sync.Mutex`
by value. Always run `go vet` after agent code generation.

Also in this session: DefaultTTL bumped 300s → 3600s to match session
length (see §4, §8 for the "gossip with amnesia" problem), L7 review
fixes (done-status filtering in `checkConflicts`, concurrent archive
safety in `readActive`), and `contrib/` with Postgres transport,
WebSocket dashboard, and mDNS demo.
