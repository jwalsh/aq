# aq Invariants

> Gossip without invariants is rumor. Gossip with invariants is intelligence.
>
> The difference between aq and Slack DMs is that aq broadcasts are
> machine-verifiable. A human saying "I'm working on auth.py" in Slack
> cannot be checked. An aq broadcast claiming files=["auth.py"] CAN be
> checked: does auth.py exist? Has it been modified? Is the agent's
> branch still current?
>
> Invariants don't make gossip authoritative. They make it trustworthy
> enough to act on. The bus metaphor: gossip tells you which direction
> to look; invariants tell you whether to step off the curb.

## Philosophy

Gossip is chatter. Chatter is cheap. But cheap doesn't mean careless.

An agent can announce "I'm working on auth.py" while auth.py has been
deleted. An agent can claim "phase=proof" while its conjecture was
refuted ten minutes ago. An agent can broadcast files it hasn't
touched in hours. The gossip layer trusts everything and verifies
nothing -- by design, because gossip carries no obligation.

But an agent reading its book on the bus still needs to check the
stops. The invariant system is the "look both ways before crossing"
of gossip. It doesn't make you stop walking. It doesn't grab your
arm. It taps your shoulder and says "you might want to look up."

**Invariants are ADVISORY, not AUTHORITATIVE.** They warn, they don't
block. An announce with `--validate` that finds warnings still
announces. A validate command that finds errors returns exit code 1,
but the broadcasts it checked are still valid broadcasts. Gossip
doesn't need permission to exist.

This is the fundamental constraint: invariants cannot violate the
gossip axiom. The moment an invariant blocks a broadcast, aq becomes
a coordinator. The moment an invariant requires external state, aq
becomes a retrieval system. Neither is acceptable.

## Three Layers

### Layer A: Self-checks (am I lying?)

Self-checks verify a broadcast's claims against the broadcaster's
own reality. They run before an announce and answer: "Is what I'm
about to say actually true?"

| Invariant | What it checks | Severity |
|-----------|---------------|----------|
| `files_exist` | Do the `-f` files actually exist on disk? | warning |
| `git_branch_matches` | Does `git branch` match the worktree field? | warning |
| `phase_valid` | Is the phase one of conjecture/proof/refutation/refinement? | error |
| `ttl_reasonable` | Is TTL between 10s and 86400s (24h)? | warning |
| `paths_relative` | Are all file paths relative (no information leakage)? | error |

Self-checks are self-assertions. The broadcaster checks its own claims.
Nobody else can run these checks for you, because nobody else is in
your worktree.

Usage:
```
aq announce -c C-1 -f "main.go" --validate    # Pre-flight checks
aq validate --category self                     # Standalone self-check
```

### Layer B: World-checks (has reality changed?)

World-checks verify that the environment hasn't changed dangerously
since the agent last looked. They answer: "While I was reading my
book, did the bus pass my stop?"

| Invariant | What it checks | Severity |
|-----------|---------------|----------|
| `branch_not_diverged` | Has origin/main moved >50 commits ahead? | warning |
| `no_ghost_broadcasts` | Are my broadcasts about to expire (>80% TTL elapsed)? | warning |
| `disk_space_ok` | Is AQ_HOME under 100MB? | warning |

World-checks are lookups. The agent looks up from its work to check
the street. They can be run periodically, manually, or triggered by
external events (git hooks, editor save, CI pipeline).

Usage:
```
aq validate --category world    # Check the world
```

### Layer C: Protocol-checks (is the system healthy?)

Protocol-checks verify that the gossip protocol's structural
properties hold across all broadcasts. They answer: "Is the
plumbing working?"

| Invariant | What it checks | Severity |
|-----------|---------------|----------|
| `ulid_unique` | No duplicate ULIDs across active + archive | error |
| `no_duplicate_active` | No two active non-done broadcasts from same agent+conjecture | warning |
| `timestamps_sane` | No broadcasts with timestamps >60s in the future | error |
| `all_paths_relative` | No absolute paths in any active broadcast's file list | error |

Protocol-checks are system-level. They don't check one broadcast --
they check the invariants of the channel as a whole. Run them in CI,
in doctor, or before trusting the output of `aq status`.

Usage:
```
aq validate --category protocol    # Protocol health
aq validate                        # All checks (world + protocol)
```

## Relationship to Conjectures

Invariants are micro-conjectures that can be checked automatically.

A conjecture like C-1 ("Filesystem-first transport is sufficient")
requires human judgment and measurement to evaluate. An invariant
like `files_exist` is a miniature conjecture ("the files this agent
claims to be working on actually exist") that can be checked in
milliseconds.

The connection runs deeper: invariants provide evidence for or against
conjectures.

- `disk_space_ok` produces evidence for C-1: if the filesystem fills
  up at scale, that's a refutation signal.
- `no_duplicate_active` produces evidence for C-2: if duplicate
  broadcasts cause false conflict signals, the conjecture identity
  mechanism is insufficient.
- `timestamps_sane` produces evidence for C-3: if clock skew across
  agents makes timestamps unreliable, NDJSON+TTL may not be expressive
  enough.

## Adding Custom Invariants

An invariant is a function that returns an `InvariantResult`:

```go
type InvariantResult struct {
    Name     string `json:"name"`
    Passed   bool   `json:"passed"`
    Message  string `json:"message"`
    Category string `json:"category"`    // "self", "world", "protocol"
    Severity string `json:"severity"`    // "error", "warning", "info"
}
```

To add a new invariant:

1. Write a function that returns `InvariantResult`.
2. Add it to the appropriate `run*Checks` function in main.go.
3. Add tests.
4. The invariant must not block any operation, require network access,
   or depend on external services.

For external integrations (CI/CD, editor plugins, git hooks), consume
the `--json` output:

```bash
# In a git pre-push hook:
results=$(aq validate --json)
errors=$(echo "$results" | jq '[.[] | select(.passed == false and .severity == "error")] | length')
if [ "$errors" -gt 0 ]; then
    echo "aq invariant errors detected — review before pushing"
    echo "$results" | jq '.[] | select(.passed == false)'
fi
```

## The Bus Metaphor

You're reading a book on the bus. The book is your conjecture, your
proof, your code. It's absorbing. You want to keep reading.

The invariant system is not the bus driver announcing stops (that
would be coordination). It's not the other passengers tapping your
shoulder (that would be a message broker). It's your own peripheral
vision -- the part of your brain that notices when the bus slows
down, when the scenery changes, when the door opens.

You can ignore it. Gossip carries no obligation. But if you look up
and the bus has passed your stop, you'll wish you had.

Self-checks: "Am I on the right bus?"
World-checks: "Have I passed my stop?"
Protocol-checks: "Is the bus still running?"

None of these make you get off. They just make sure that when you
decide to look up, you see the truth.
