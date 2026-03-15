# 10-Agent Simplification Swarm Experiment

Date: 2026-03-15
Conjecture: C-9 (concept-level conflict detection)

## Setup

10 agents launched in parallel isolated worktrees, all tasked with
simplifying different aspects of `main.go`. Each announced via
`aq announce -c C-10 --claim "<intent>" --phase proof` before starting.

## The Agents

| # | Worktree | Claim | File | Tests Pass? |
|---|----------|-------|------|-------------|
| 1 | agent-a413c867 | CLI dispatch to table-driven | main.go | YES |
| 2 | agent-a787692f | Broadcast struct typed constants | main.go, main_test.go | YES |
| 3 | agent-aed758a6 | Conflict detection helpers | main.go | YES |
| 4 | agent-ab8e6d1d | Unified flag parser | main.go, main_test.go | YES |
| 5 | agent-a000ddf2 | File I/O streamlining | main.go | YES |
| 6 | agent-a292f91f | Validate command table-driven | main.go | YES |
| 7 | agent-a2451823 | Test helper consolidation | main_test.go | YES |
| 8 | agent-acd010f6 | ULID generation cleanup | main.go | YES |
| 9 | agent-ac2aee73 | Doctor command cleanup | main.go | YES |
| 10 | agent-a6b79c85 | TTL/expiry consolidation | main.go | YES |

## Broadcast Activity

- 22 broadcasts active at peak (20 on C-10, 1 on C-9 coordinator, 1 on C-TEST)
- All agents visible via `aq status`
- Gossip layer working: every agent could see every other agent's intent

## Conflict Detection Results

**Conflicts detected by aq: 0**
**Actual conflicts: 10/10**

Every single agent modified `main.go`. Several pairs have direct
architectural conflicts:

### Direct Conflicts (would break each other's code)

| Agent A | Agent B | Why |
|---------|---------|-----|
| 1 (dispatch table) | 4 (flag parsing) | Both restructure how commands are parsed and dispatched |
| 2 (typed constants) | 3 (conflict logic) | Agent 2 changes Phase/Status types; Agent 3 rewrites functions using them |
| 2 (typed constants) | 6 (validate) | Agent 2 changes types; Agent 6 rewrites invariant checks using them |
| 5 (file I/O) | 10 (TTL/expiry) | Both modify readActive and archive logic |
| 1 (dispatch) | 9 (doctor) | Agent 1 rewrites dispatch; Agent 9 rewrites what gets dispatched to |

### Why Zero Were Detected

The current conflict detector requires **file overlap** between broadcasts.
No agent passed `-f main.go` in their announcement (they used `--claim`
only, as instructed by the "feels not files" reframing). Result:

- 20 broadcasts on the same conjecture (C-10) = not a signal
- 10 agents with architecturally overlapping claims = not a signal
- All touching the same file = not a signal (because not declared)

Even if they HAD passed `-f main.go`, the single-file-repo problem from
JOURNEY.md would have triggered: every agent would conflict with every
other agent. Signal-to-noise ratio: zero. The file overlap heuristic
can't distinguish "both touching main.go but in unrelated functions"
from "both rewriting the same function."

## What C-9 Would Have Caught

With concept-level conflict detection (proposed in docs/CONCEPT-CONFLICTS.md):

**P0: Conjecture ID matching** — All 10 agents share C-10. Immediate
HIGH signal. This alone would have flagged all 10 as potentially
conflicting. Simple, zero-cost, and correct here.

**P1: Claim similarity (Jaccard on word tokens)** — Pairwise comparison:

| Agent A claim | Agent B claim | Jaccard | Signal? |
|---------------|---------------|---------|---------|
| "simplifying CLI dispatch" | "simplifying flag parsing" | 0.33 | YES |
| "simplifying file I/O" | "simplifying TTL and expiry handling" | 0.20 | MAYBE |
| "simplifying Broadcast struct" | "simplifying conflict detection" | 0.22 | MAYBE |

Word overlap on "simplifying" alone creates baseline similarity. The
shared intent verb is doing real semantic work here — these agents are
all in the same conceptual space.

## Lessons

1. **The "feels not files" reframing created a detection gap.** By
   removing `-f` from the examples and making files optional, we also
   removed the only conflict signal the detector currently uses. The
   claims carry the intent, but nothing reads them yet.

2. **Conjecture ID is the cheapest win.** All 10 agents on C-10 should
   have been flagged immediately. This is a 1-line change to
   `checkConflicts`: `if other.ConjectureID == my.ConjectureID { HIGH }`.

3. **The swarm proved C-9.** Concept-level detection is not theoretical
   — this experiment produced 10 real conflicts, zero detected, with
   21 active broadcasts providing full visibility. The gossip layer
   saw everything and understood nothing.

4. **Single-file repos remain the worst case.** Even with file lists,
   `main.go` vs `main.go` is always HIGH. Function-level granularity
   (C-8) would help, but concept-level (C-9) helps more.

5. **Each agent produced good work in isolation.** All 10 pass tests
   independently. The conflict only manifests at merge time — which is
   exactly the gap aq is supposed to fill.

## Worktree Branches

Changes live in these branches (uncommitted in worktrees):

```
worktree-agent-a413c867  # dispatch table
worktree-agent-a787692f  # typed constants (edited main directly)
worktree-agent-aed758a6  # conflict helpers
worktree-agent-ab8e6d1d  # unified parser
worktree-agent-a000ddf2  # file I/O
worktree-agent-a292f91f  # validate table-driven
worktree-agent-a2451823  # test helpers
worktree-agent-acd010f6  # ULID cleanup
worktree-agent-ac2aee73  # doctor cleanup
worktree-agent-a6b79c85  # TTL/expiry
```

## Merge Strategy

These cannot be merged as-is. A rebuild from spec-v2.org incorporating
the best ideas from each agent is the right approach:

- **Keep**: dispatch table (1), typed constants (2), `invariantDef` (6),
  `writeBroadcastOrFail` test helper (7), `RemainingTTL()` (10)
- **Keep**: `archiveExpiredBroadcast()` (10), `conflictSeverity()` (3),
  `computeSharedFiles()` (3), unified `parseFlags` (4)
- **Skip**: redundant simplifications where two agents did the same thing
  differently (5 vs 10 on readActive)

## Protocol Gap

This experiment documents **Protocol Gap #11**: concept-level conflict
detection is load-bearing, not optional. The gossip layer can see all
broadcasts but cannot interpret semantic overlap between claims. C-9
is promoted from "proposed" to "required."
