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

## Summary

aq's design is sound for its intended use case (ambient presence in
multi-agent, multi-file development). The dogfooding exposed three real
issues: the bootstrap problem (unavoidable), the single-file granularity
problem (addressable with function-level tracking), and the TTL cliff
(addressable with a heartbeat daemon or longer defaults). The
filesystem-first transport, NDJSON format, and phase-based severity
heuristic all proved their worth. The tool needs to exist before it can
be evaluated fairly -- and now it does.
