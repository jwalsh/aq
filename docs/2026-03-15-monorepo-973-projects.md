# Experience Report: aq in a 973-Project Monorepo

Date: 2026-03-15
Context: builder-workspace, a monorepo containing 973 projects across 39 categories.
Tools: aq 4d18774, bd (beads), cprr 6164b48, sb v0.1.0-5

## Why This Is Useful

This is a pathological case. Nobody runs 973 projects in one repo with
60 simultaneous aq broadcasts. But pathological cases expose design
assumptions that normal usage hides. Every issue found here maps to a
milder version that will surface in real 3-5 project workflows.

## Setup

Starting from a monorepo with 973 project directories, each containing
`config/project_info.json` and `config/tasks.json`, we:

1. Generated `spec.org` for all 973 projects (batch script)
2. Generated `CLAUDE.md` for all 973 projects (batch script)
3. Registered 3,209 cprr conjectures
4. Created 3,802 bd issues with dependency chains (steps 1-4 per project)
5. Labeled all issues across 5 teams (ALPHA through ECHO)
6. Launched 5 team agents in parallel worktrees to pull issues and do work
7. Ran up to 60 concurrent aq broadcasts

## What Worked

### Broadcast mechanics are solid

60 concurrent broadcasts, 163 total ULIDs across the session. `aq validate`
passed 7/7 invariant checks every time we ran it. No data corruption, no
races, no ULID collisions. The file-based storage model handles high
broadcast volume without issue.

### Cross-worktree conflict detection works correctly

When agents run in separate worktrees (via `sb` / Claude Code `--worktree`),
`aq check` correctly identifies file-level conflicts:

```
[HIGH] .../worktrees/worktree-agent-ab672a47 (C-?) <-> .../main (C-100)
       -- shared: projects/SolarPoweredTransport/CLAUDE.md
[HIGH] .../worktrees/worktree-agent-ab672a47 (C-?) <-> .../worktrees/worktree-agent-af70259b (C-200)
       -- shared: projects/SolarPoweredTransport/CLAUDE.md
```

Three-way conflicts (3 agents claiming the same file) correctly produce
two `[HIGH]` lines from the checker's perspective. Exit code 1 on conflict
makes it usable as a CI gate.

### Multi-file claims work cleanly

`aq announce -c C-1 -f "file1.md,file2.md"` stores files as a JSON array.
Team CHARLIE announced claims across two projects in one broadcast. Status
display shows both files on one line. The file list is the right abstraction.

### The three-primitive interlock conceptually works

`sb list` -> where am I, `cprr list` -> why am I here, `aq status` ->
who else knows. All three produced useful output when run from worktree
agents. The mental model is sound.

### TTL-based expiry is the right default

Broadcasts from early in the session expired naturally while later ones
stayed live. No cleanup needed. Silence after expiry is a feature, not
a bug -- it correctly represents "that agent is probably gone."

## Issues Found

### Issue 1: Intra-worktree conflict blindness

**Severity**: Design gap, not a bug.

Two broadcasts from the same worktree claiming the same file do NOT
trigger a conflict in `aq check`. This is because aq treats same-worktree
broadcasts as "self" -- which makes sense for the normal case (one agent
per worktree). But in a monorepo where multiple logical agents share the
main worktree, this means zero conflict detection.

**Reproduction**:
```bash
aq announce -c C-100 -f "projects/Foo/CLAUDE.md" --phase proof
aq announce -c C-200 -f "projects/Foo/CLAUDE.md" --phase proof
aq check -f "projects/Foo/CLAUDE.md"
# -> "no conflicts detected"
```

Both C-100 and C-200 claim the same file from `main`, but `aq check`
sees them as the same agent.

**Impact in practice**: Low for repo-per-project setups. Significant when
subagents share a worktree (e.g., Claude Code background agents that
don't use `--worktree`).

**Possible fix**: Compare by conjecture ID when worktree matches, not
just by worktree address. If two different conjecture IDs from the same
worktree claim the same file, that is at minimum a `[MEDIUM]` conflict.

### Issue 2: `aq status` has no filtering

With 60 broadcasts, `aq status` dumps all of them. There is no:
- `aq status --conjecture C-100`
- `aq status --file projects/Foo/CLAUDE.md`
- `aq status --worktree agent-abc123`

Every consumer must pipe through jq or python. The `--json` flag helps,
but CLI-level filtering would reduce friction significantly.

**Impact**: Grows linearly with broadcast count. At 5 broadcasts, not
noticeable. At 60, painful.

### Issue 3: No `--channel` for team scoping

All broadcasts are global. Team CHARLIE's energy project announcements
are visible to Team DELTA's security agents. In a 5-team, 60-broadcast
scenario, agents see ~50 irrelevant broadcasts for every relevant one.

The `--channel` flag exists in help text but only as the default
`broadcast` channel. Supporting `aq announce --channel team-charlie`
with `aq status --channel team-charlie` would make teams viable.

### Issue 4: No explicit revoke/cancel

If an agent realizes it claimed the wrong file, the only option is
waiting for TTL to expire. A `aq revoke <broadcast-id>` or
`aq revoke -c C-100` would be useful for correcting mistakes without
waiting up to 3600 seconds.

### Issue 5: `aq announce` without `-f` succeeds silently

Omitting the `--files` flag creates a broadcast with an empty file list.
This broadcast provides zero conflict detection value since there are no
files to check against. A warning ("no files specified, broadcast will
not participate in conflict detection") would prevent accidental
no-op broadcasts.

Team DELTA discovered this during edge case probing.

### Issue 6: No link to bd issues

aq broadcasts reference conjectures (`-c`) and files (`-f`) but not
bd issues. When an agent announces work on a file, there is no way to
connect that broadcast to the specific bd issue being worked on.

This was the #1 cross-tool gap reported by all 5 teams. An optional
`--issue <bd-id>` flag on `aq announce` (stored in the broadcast JSON
but not required) would close the gap without breaking existing behavior.

### Issue 7: `sb detect` referenced in quickstart but doesn't exist

`aq quickstart` documents the three-primitive interlock as:
```
sb detect    -> where am I?
cprr list    -> why am I here?
aq status    -> who else knows?
```

But `sb detect` is not a valid subcommand. `sb list` is the closest
equivalent. Either implement `sb detect` or update the quickstart text.

### Issue 8: `[MEDIUM]` severity semantics unclear

When checking a file claimed by a `--phase conjecture` broadcast against
a `--phase proof` broadcast, the result is `[MEDIUM]` instead of `[HIGH]`.
The severity reduction makes intuitive sense (different phases = less
likely to conflict) but the logic is not documented. Users need to know:
when is it HIGH vs MEDIUM vs LOW?

## Metrics

| Metric | Value |
|--------|-------|
| Total broadcasts created | 163 ULIDs |
| Peak concurrent broadcasts | 60 |
| Unique worktree addresses | 12 |
| Contested files (multi-claim) | 4 |
| `aq validate` failures | 0 |
| Duplicate conjecture ID broadcasts | 2 (caught by validate, advisory) |
| ULID collisions | 0 |
| Data corruption incidents | 0 |

## Verdict

aq's core mechanics -- file-based broadcast, ULID ordering, TTL expiry,
cross-worktree conflict detection -- are solid under stress. The protocol
invariants held across 163 broadcasts with zero corruption.

The gaps are all in the UX/filtering layer and cross-tool integration,
not in the protocol itself. The most impactful fix would be intra-worktree
conflict detection (issue 1), since it unlocks the monorepo and
shared-worktree use cases without changing the broadcast format.

The second most impactful would be `--channel` support (issue 3), since
team-scoped broadcasts are the natural unit of coordination once you have
more than ~10 concurrent agents.
