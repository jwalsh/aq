# Cross-Worktree Validation Results

Date: 2026-03-15
Setup: 5 worktree agents exercising aq+sb+cprr interlock

## Agents

| Agent | Worktree | Mission |
|-------|----------|---------|
| A | worktree-af70259b | Cross-worktree conflict detection |
| B | worktree-a876070a | Cross-worktree conflict detection (overlapping claims) |
| X | worktree-ab672a47 | Overlapping file claims (both SolarPowered + WaveEnergy) |
| bd+aq | worktree-a397cee4 | bd issue pickup → aq announce → conflict check |
| interlock | worktree-a625f17d | Three-primitive interlock (sb/cprr/aq) |

## Results

### Cross-Worktree Conflict Detection

Agent A:
- `aq check` on SolarPoweredTransport: conflict detected with main's C-100 (exit 1)
- `aq check` on NeuroplasticityOptimizer: conflict detected with main's C-105
- `aq check` on AICodeReviewer: no conflicts (nobody claimed it)

Agent B:
- `aq check` on WaveEnergyConverter: 1 HIGH conflict with main's C-103
- `aq check` on SolarPoweredTransport: 2 HIGH conflicts — main's C-100 AND Agent A's C-200

Agent X (deliberate overlap):
- Claimed BOTH SolarPoweredTransport AND WaveEnergyConverter
- `aq check` correctly detected 2 conflicts per file (main + other worktree agents)

**Verdict**: Cross-worktree detection works. Agents in separate worktrees
correctly detect each other's broadcasts. File overlap heuristic functions
when agents declare files.

### bd + aq Interplay

- Successfully picked 3 ready issues from bd, announced via aq, confirmed conflict detection
- Finding: `aq validate` checks protocol invariants (ULID uniqueness,
  timestamps, well-formedness) — NOT file conflicts. Conflict detection
  is a separate concern via `aq check`. This is correct separation: validate
  ensures the gossip protocol is healthy, check interprets the gossip content.

### Three-Primitive Interlock

- `sb list`: 5 worktrees (1 main + 4 agents) — spatial dimension working
- `cprr`: 172+ open conjectures — epistemic dimension working
- `aq status`: 22 broadcasts across 5 unique agent addresses — temporal dimension working

Finding: `sb detect` is referenced in CLAUDE.md build step 6
("sb detect → aq announce") but does not exist as a subcommand. `sb list`
is what works. This is a gap: sb has no command that auto-detects the
current worktree context and feeds it to aq.

## Final Numbers

| Metric | Count |
|--------|-------|
| Projects with spec.org | 973 |
| Projects with CLAUDE.md | 973 |
| cprr conjectures | 173 |
| bd issues | 80 (55 ready, 25 blocked) |
| aq broadcasts | 22 (across 5 worktrees) |
| Contested files | 3 |
| Unique agent identities | 5 |
| aq validate invariants | 7/7 pass, 103 unique ULIDs |

## Design Insight

aq conflict detection is worktree-scoped (same worktree = self, different
worktree = potential conflict) and file-granular (not project-granular).
This means it works correctly even when 973 projects share a single repo,
as long as agents use separate worktrees.

This validates Protocol Gap #12's proposed fix: identity comes from the
broadcast (worktree address), and separate worktrees produce separate
identities. The monorepo pathology only manifests when agents share a
single worktree.

## Gaps Identified

1. **`sb detect` does not exist** — Build step 6 references it, but sb
   only has `sb list`. Need a command that outputs the current worktree
   context in a format aq can consume.

2. **`aq validate` vs `aq check` confusion** — Users expect validate to
   catch conflicts. It doesn't — it checks protocol health. The distinction
   is correct but needs clearer documentation.
