# The aq Dogfooding Journey

> "We're building the gossip tool by gossiping about building the gossip tool."

## The Setup

Date: 2026-03-13

Four agents. Four worktrees. One ambient gossip layer that doesn't work yet.
The task: build aq using aq. The meta-circularity is the point.

## The Cast

| Agent | Worktree | Mission | Conjecture |
|-------|----------|---------|------------|
| Alpha | `worktrees/aq-alpha` | Go port core: types, ULID, storage, broadcast I/O | C-1 (filesystem transport) |
| Beta | `worktrees/aq-beta` | Conflict detection + CLI commands (announce, check, status) | C-4 (phase severity) |
| Gamma | `worktrees/aq-gamma` | Operational commands (init, doctor, quickstart, whisper) + UX review | C-3 (Wave semantics) |
| Delta | `worktrees/aq-delta` | Tests in main_test.go + dogfooding integration | C-2 (conjecture identity) |

## Known Issues (Pre-Flight)

### Issue #1: The Bootstrap Paradox
We want to use `aq announce` to coordinate building `aq announce`.
The Python prototype exists but has zero tests and no watch mode.
The Go port is 65 lines of stub.

**Resolution**: Use the Python prototype as training wheels. If it breaks,
that's data for C-1.

### Issue #2: The Heartbeat Problem
Default TTL is 300 seconds. A focused coding session is 30-60 minutes.
Without `aq watch` or auto-heartbeat, every agent will go invisible
after 5 minutes. By the time anyone checks `aq status`, everyone's
broadcasts have expired. It's gossip with amnesia.

**Prediction**: This will be the first thing that annoys us.

**Confirmed at T+7min**: All 4 broadcasts expired and archived while
agents were still actively working. `aq status` shows "no active
broadcasts". The system designed to track agent presence has no idea
any agents exist. The archive has 4 corpses. The agents are alive
and coding.

### Issue #3: The File List Problem
`aq announce -c C-1 -f "main.go"` — but all four agents are touching
main.go. That's the single-file convention. So every conflict check
will return HIGH for everyone. Signal-to-noise ratio: zero.

**Prediction**: We'll discover that single-file repos are the worst case
for file-overlap conflict detection. The tool is designed for repos with
hundreds of files, not one.

### Issue #4: The Merge Wall
Four agents writing to the same `main.go`. Classic.
aq can detect the conflict but can't prevent it.
Gossip without teeth is just... gossip.

**Resolution**: Each worktree branch. Merge conflicts are git's problem.
aq's job is to make agents *aware* before they diverge too far.

## Timeline

(to be filled in as we go)

### Hour 0: Planning & Worktree Setup
- [x] Create 4 worktrees via `sb add` — all 4 created successfully
- [x] Bootstrap Python `aq announce` in each — all 4 announced
- [x] Discover first hilarious failure — see Humor Log #1

### Hour 1: Parallel Implementation
- [x] Alpha: types + ULID + storage — 360 lines, compiles, core foundation
- [x] Beta: conflict + CLI — 642 lines, announce/whisper/check/status work
- [x] Gamma: operational commands + UX — 1351 lines, init/doctor/quickstart + UX-REVIEW.md
- [x] Delta: tests + integration — 901+823 lines, 32 passing tests + DOGFOODING.md

All 4 agents created their own worktree branches (worktree-agent-*) instead
of using the feat/aq-* branches we set up. See Humor Log #4.

Each agent independently re-implemented the full main.go because they all
needed a compiling binary to test their piece. So we got 4 complete
implementations instead of 4 composable slices. The single-file convention
forced each agent to write the whole thing.

### Hour 2: The Merge
- [ ] Pick Delta as base (has tests)
- [ ] Cherry-pick Gamma's richer doctor/quickstart/UX improvements
- [ ] Apply originating agent's advice: aqHome() checks .aq/ in cwd first
- [ ] Verify all 32 tests still pass
- [ ] Commit with git notes per CLAUDE.md mandate

## Humor Log

> Things that made us laugh, groan, or question our life choices.

### #1: "Everything is on fire" (T+0min)

First `aq check` after all 4 agents announced:

```
[HIGH] aq-alpha (C-1) ↔ aq-beta (C-4) — shared: main.go
[HIGH] aq-alpha (C-1) ↔ aq-delta (C-2) — shared: main.go
[MEDIUM] aq-alpha (C-1) ↔ aq-gamma (C-3) — shared: main.go
```

Three conflicts, two HIGH, one MEDIUM. For a tool designed to reduce
merge anxiety, it immediately caused maximum anxiety. Every agent is
touching `main.go` because the ecosystem convention is a single-file
monolith. The gossip layer's only semantic signal (file overlap) is
saturated. It's like an mDNS broadcast where every service is on port 80.

**Lesson**: File-overlap conflict detection assumes file-level isolation.
Single-file repos need a different heuristic — maybe function-level or
line-range based detection. Or maybe the answer is: "yes, you all know
you're conflicting, now coordinate like adults."

### #3: "Git notes ARE gossip" (T+5min)

The originating agent left instructions via `git notes show HEAD` — a
whisper (TTL: 86400, priority: whisper) containing build guidance. It's
aq's gossip protocol instantiated in git's own metadata layer:

- Broadcast medium: git notes (not a file in the worktree)
- TTL: 86400s (24h, a whisper)
- Payload: plain text advice about the Go port
- No obligation to read it
- Addressed to `aygp-dr` but anyone can see it

The originating agent is dogfooding aq's *concept* through git's
existing infrastructure, while we're trying to build aq's *tool*.
Inception-level meta-circularity.

Key quote from the note:
> "The temptation will be to improve the protocol while porting it.
> Don't. Port it verbatim first."

Also: `aqHome()` should check `.aq/` in cwd before `~/.aq/` (like
cprr's local-first store). This is the one deliberate improvement
over the Python prototype. Good to know.

### #4: "The Worktree Mixup" (T+3min)

We carefully created 4 worktrees with `sb add` (feat/aq-alpha through
feat/aq-delta). We announced them via `aq announce`. We checked for
conflicts. The gossip layer was working perfectly.

Then we launched the agents with `isolation: "worktree"` and they
created 4 DIFFERENT worktrees (worktree-agent-{hash}). The agents
didn't use the worktrees we set up. They made their own.

This is the coordination problem aq was designed to solve, and aq
couldn't solve it because:
1. aq broadcasts are per-directory (tied to git context)
2. The new worktrees have different git contexts
3. The original broadcasts (feat/aq-*) expire in 5min anyway
4. Nobody re-announced from the new locations

**Score**: Gossip 0, Chaos 1.

The irony: we used sb to create worktrees, aq to announce them,
then the agents created a parallel set of worktrees that nobody
announced. The gossip layer had perfect information about the
*wrong* worktrees.

### #2: "Gossip with amnesia" (T+0min, predicted)

TTL=300s. Our agents will take 20-40 minutes each. Without heartbeat,
after 5 minutes the gossip layer forgets everyone exists. The broadcasts
expire, status shows empty, conflict checks find nothing. Agents proceed
in blissful ignorance, exactly the failure mode aq was designed to prevent.

The Python prototype has no `aq watch` or `aq heartbeat`.
We are flying blind after minute 5.

## Lessons Learned

### The single-file convention defeats composable agents
Each agent was given a slice (types, CLI, ops, tests) but needed the whole
file to compile. Result: 4 complete implementations instead of 4 pieces.
If aq had been split into packages (protocol.go, conflict.go, cli.go), each
agent could have owned its file without overlap. But the ecosystem convention
says "single main.go" — and that's the convention for a reason (deployment
simplicity). The tension between composable development and single-artifact
deployment is real.

### Isolation creates duplication
The `isolation: "worktree"` parameter gave each agent its own git branch.
Good for safety, bad for composition. Four agents independently wrote
ULID generation, sandbox detection, and conflict logic. Total lines across
all agents: ~3154 main.go + 823 test. Final merged result will be ~900+800.
That's 3.5x duplication. Not wasteful per se — each agent tested its own
logic — but a very different pattern from human collaboration.

### Gossip can't coordinate its own construction
The bootstrap problem is real and unavoidable. aq was silent during its own
build because aq didn't exist yet. The originating agent's git notes were
more useful than aq's broadcasts — git notes persisted, aq broadcasts expired.

### TTL 300s is correct, but heartbeat is mandatory
Every observation converges: the TTL is fine for the wire protocol, but
without auto-renewal it's useless for real sessions. Build step 5 (daemon)
isn't optional — it's load-bearing.

## Protocol Gaps Discovered

(every time something breaks or surprises us, log it here)

| # | Gap | Discovered By | Severity | Resolution |
|---|-----|---------------|----------|------------|
| 1 | Single-file repos saturate file-overlap heuristic | All agents, T+0 | HIGH (ironic) | Need function/section-level granularity or section annotations |
| 2 | No heartbeat = invisible after 5min | Predicted pre-flight | HIGH | Need `aq watch` or auto-heartbeat daemon |
| 3 | `aq check` only runs on-demand, caller-side | Alpha checking | MEDIUM | Need push notification or watcher mode |
| 4 | Agent addresses include "worktrees/" prefix — long and noisy in output | Status display | LOW | Truncate or alias agent addresses |
| 5 | No local-first .aq/ in cwd (Python only checks ~/.aq) | Originating agent via git notes | MEDIUM | Go port should check .aq/ in cwd first, fall back to ~/.aq |
| 6 | Git notes are a gossip channel we didn't design for | Originating agent whisper | FUN | Git notes = out-of-band gossip with built-in persistence |

## Metrics We're Watching

- **Broadcasts written**: total across all agents
- **Broadcasts expired before anyone read them**: the "shouting into void" metric
- **Conflicts detected**: signal
- **Conflicts that were actually real**: signal-to-noise
- **Merge conflicts at integration**: the ground truth
- **Time from broadcast to detection**: gossip latency
- **Times an agent forgot to re-announce**: the "oops" metric
