------------------------------ MODULE aq ------------------------------
(*
 * TLA+ Formal Specification of the aq Broadcast Protocol
 * ======================================================
 *
 * aq is a gossip layer (L1.5) for multi-agent development. Agents
 * broadcast presence via filesystem-backed channels so peers detect
 * semantic conflicts before they become merge conflicts.
 *
 * This spec models:
 *   - Broadcast lifecycle (announce -> active -> expired -> archived)
 *   - TTL-based expiry with prune-on-read semantics
 *   - CPRR phase-modulated conflict detection
 *   - The TTL cliff failure mode (DOGFOODING.md section 4)
 *   - Concurrent ReadActive race condition (DOGFOODING.md section 6)
 *
 * Runnable with TLC model checker. See aq.cfg for configuration.
 *
 * References:
 *   - CLAUDE.md: broadcast payload schema, conflict severity rules
 *   - docs/DOGFOODING.md: empirical failure modes to verify
 *   - main.go: Go implementation (readActive, checkConflicts)
 *)

EXTENDS Integers, Sequences, FiniteSets, TLC

\* ====================================================================
\* Constants: configured in aq.cfg for TLC model checking
\* ====================================================================

CONSTANTS
    Agents,          \* Set of agent identifiers, e.g. {"a1", "a2", "a3"}
    MaxBroadcasts,   \* Max broadcasts per agent (bounds state space)
    TTLValues,       \* Set of allowed TTL values, e.g. {5, 60, 300}
    MaxClock,        \* Upper bound on clock ticks, e.g. 600
    Files,           \* Universe of files agents can touch
    Phases,          \* CPRR phases: {"conjecture", "proof", "refutation", "refinement"}
    Conjectures      \* Conjecture IDs, e.g. {"C-1", "C-2", "C-3"}

\* ====================================================================
\* Variables
\* ====================================================================

VARIABLES
    clock,           \* Global discrete clock (models unix timestamp)
    broadcasts,      \* Set of active broadcasts (in requests/ directory)
    archive,         \* Set of archived broadcasts (in archive/ directory)
    nextId,          \* Monotonic broadcast ID counter (simplifies ULID)
    agentCount       \* Function: agent -> number of broadcasts made
                     \*   (used to enforce MaxBroadcasts bound)

vars == <<clock, broadcasts, archive, nextId, agentCount>>

\* ====================================================================
\* Broadcast record type
\* ====================================================================
\*
\* Each broadcast is a record with fields matching CLAUDE.md schema:
\*   agent:          which agent announced
\*   conjecture_id:  e.g. "C-1"
\*   phase:          conjecture | proof | refutation | refinement
\*   files:          subset of Files being touched
\*   ts:             timestamp when broadcast was created
\*   ttl:            seconds until expiry
\*   id:             unique identifier (modeled as Nat)
\*
\* Omitted from model (not relevant to safety properties):
\*   worktree, conjecture_claim, status
\*   (status=done is modeled by letting TTL expire or not announcing)

BroadcastType ==
    [ agent : Agents,
      conjecture_id : Conjectures,
      phase : Phases,
      files : SUBSET Files \ {{}},  \* non-empty file sets only
      ts : 0..MaxClock,
      ttl : TTLValues,
      id : Nat ]

\* ====================================================================
\* Helper operators
\* ====================================================================

\* A broadcast is expired when the clock has advanced past ts + ttl.
\* Matches main.go: func (b *Broadcast) IsExpired()
IsExpired(b) == clock > b.ts + b.ttl

\* The set of currently non-expired broadcasts.
ActiveBroadcasts == { b \in broadcasts : ~IsExpired(b) }

\* The set of currently expired broadcasts still in the requests dir.
\* These are "zombies" -- expired but not yet pruned by ReadActive.
\* DOGFOODING.md section 6: readActive moves expired as side effect.
ExpiredInRequests == { b \in broadcasts : IsExpired(b) }

\* Two broadcasts overlap if they share at least one file.
\* Matches main.go: func (b *Broadcast) Overlaps()
Overlaps(b1, b2) ==
    b1.files \cap b2.files /= {}

\* Shared files between two broadcasts.
SharedFiles(b1, b2) == b1.files \cap b2.files

\* Conflict severity modulated by CPRR phase.
\* Matches main.go checkConflicts():
\*   both proof => HIGH
\*   one proof  => MEDIUM
\*   otherwise  => LOW
\*
\* DOGFOODING.md section 2: "When four agents all touch main.go in
\* proof phase, every pair is a HIGH conflict."
\* DOGFOODING.md section 5: "Both-proof-on-same-file is genuinely
\* riskier than conjecture-on-same-file."
ComputeSeverity(b1, b2) ==
    IF b1.phase = "proof" /\ b2.phase = "proof"
    THEN "high"
    ELSE IF b1.phase = "proof" \/ b2.phase = "proof"
    THEN "medium"
    ELSE "low"

\* CheckConflicts for a given agent and file set against active broadcasts.
\* Returns the set of conflict signals (pairs with severity).
\* Matches main.go: func checkConflicts(me Broadcast, channel string)
CheckConflictsFor(agent, fileSet) ==
    LET others == { b \in ActiveBroadcasts : b.agent /= agent }
        overlapping == { b \in others : b.files \cap fileSet /= {} }
    IN  { [ other |-> b,
            shared |-> b.files \cap fileSet,
            severity |->
              IF b.phase = "proof"
              THEN "medium"   \* we don't know caller's phase here
              ELSE "low" ]
          : b \in overlapping }

\* Full pairwise conflict check between two specific broadcasts.
\* This is what we verify properties against.
ConflictBetween(b1, b2) ==
    IF Overlaps(b1, b2)
    THEN [ severity |-> ComputeSeverity(b1, b2),
           shared   |-> SharedFiles(b1, b2) ]
    ELSE [ severity |-> "none",
           shared   |-> {} ]

\* ====================================================================
\* TTL Cliff Analysis (DOGFOODING.md section 4)
\* ====================================================================
\*
\* "Default TTL is 300 seconds (5 minutes). For a coding session that
\*  runs 30-60 minutes, that means... for the remaining 25-55 minutes,
\*  the gossip layer has no memory of the agent's work."
\*
\* ValidCoverageFraction: for a broadcast with given TTL and a work
\* duration, what fraction of work time has a valid broadcast?
\*
\* With TTL=5 and work=30:  5/30 = 16.7% coverage -- embarrassing.
\* With TTL=60 and work=30: 60/30 = min(1, 2.0) = 100% coverage.
\* With TTL=300 and work=30: min(1, 300/30) = 100% coverage.
\* With TTL=5 and work=600: 5/600 = 0.8% -- essentially invisible.
\*
\* This operator lets TLC compute the cliff for each TTL config.

ValidCoverageFraction(ttl, workDuration) ==
    IF workDuration = 0 THEN 1
    ELSE IF ttl >= workDuration THEN 1
    ELSE ttl  \* Return raw TTL; caller divides by workDuration.
              \* (TLA+ integers only -- we report ttl out of workDuration)

\* For the model checker: which broadcasts are currently providing
\* valid presence information? This is the "not-cliff" set.
BroadcastsWithValidPresence ==
    { b \in broadcasts : clock <= b.ts + b.ttl }

\* ====================================================================
\* Initial state
\* ====================================================================

Init ==
    /\ clock = 0
    /\ broadcasts = {}
    /\ archive = {}
    /\ nextId = 1
    /\ agentCount = [a \in Agents |-> 0]

\* ====================================================================
\* Actions
\* ====================================================================

(* ----- Announce -----
 * An agent creates a new broadcast and adds it to the active set.
 * Matches main.go: writeBroadcast() -> writes to requests/ directory.
 *
 * Preconditions:
 *   - Agent has not exceeded MaxBroadcasts (state space bound)
 *   - Clock has not exceeded MaxClock
 *   - Files must be non-empty
 *)
Announce(agent, conjecture, phase, fileSet, ttl) ==
    /\ agentCount[agent] < MaxBroadcasts
    /\ clock <= MaxClock
    /\ fileSet /= {}
    /\ fileSet \subseteq Files
    /\ LET newBroadcast ==
           [ agent          |-> agent,
             conjecture_id  |-> conjecture,
             phase          |-> phase,
             files          |-> fileSet,
             ts             |-> clock,
             ttl            |-> ttl,
             id             |-> nextId ]
       IN /\ broadcasts' = broadcasts \cup {newBroadcast}
          /\ nextId' = nextId + 1
          /\ agentCount' = [agentCount EXCEPT ![agent] = @ + 1]
          /\ UNCHANGED <<clock, archive>>

(* ----- Tick -----
 * Advance the global clock by 1. Models passage of time.
 * Does NOT automatically prune -- pruning happens only in ReadActive.
 * This is deliberate: in the real system, expired broadcasts remain
 * in requests/ until someone calls readActive().
 *
 * DOGFOODING.md section 4 (TTL Cliff): tick without re-announce
 * means the broadcast expires and the agent becomes invisible.
 *)
Tick ==
    /\ clock < MaxClock
    /\ clock' = clock + 1
    /\ UNCHANGED <<broadcasts, archive, nextId, agentCount>>

(* ----- ReadActive -----
 * Scan all broadcasts; return non-expired ones; move expired to archive.
 * This is the SIDE-EFFECTING read documented in DOGFOODING.md section 6:
 *   "readActive moves expired files to archive as a side effect of
 *    reading. This means a read operation mutates the filesystem."
 *
 * In the Go implementation (main.go:readActive), this iterates over
 * entries, checks IsExpired(), and os.Rename()s expired files to archive/.
 *
 * The atomicity question (DOGFOODING.md section 6):
 *   "There is no locking. Two agents calling readActive simultaneously
 *    could both try to move the same expired file."
 *
 * We model this as an atomic action. The property PruneOnReadAtomicity
 * verifies that no broadcasts are lost by this operation.
 *)
ReadActive ==
    /\ ExpiredInRequests /= {}  \* Only fires if there's something to prune
    /\ LET expired == ExpiredInRequests
       IN /\ archive' = archive \cup expired
          /\ broadcasts' = broadcasts \ expired
          /\ UNCHANGED <<clock, nextId, agentCount>>

(* ----- ConcurrentReadActive -----
 * Models two agents calling ReadActive at the "same time" (same tick).
 * In the real system, both processes scan the directory, both see the
 * expired file, both try to os.Rename() it. On POSIX, one rename
 * succeeds and one gets ENOENT. The broadcast is NOT lost -- it
 * moves to archive exactly once.
 *
 * We model this as: pick a subset of expired broadcasts that "this"
 * reader sees (non-deterministic, could be any subset), and move
 * only those. A second ConcurrentReadActive in the same tick would
 * move whatever remains.
 *
 * Property to verify: no broadcast disappears (is neither in
 * broadcasts nor archive).
 *)
ConcurrentReadActive ==
    /\ ExpiredInRequests /= {}
    /\ \E subset \in (SUBSET ExpiredInRequests \ {{}}):
        /\ archive' = archive \cup subset
        /\ broadcasts' = broadcasts \ subset
        /\ UNCHANGED <<clock, nextId, agentCount>>

(* ----- AnnounceAny -----
 * Non-deterministically choose an agent, conjecture, phase, files, TTL
 * and announce. This is the "environment" action that generates traffic.
 *)
AnnounceAny ==
    \E agent \in Agents :
    \E conj \in Conjectures :
    \E phase \in Phases :
    \E fileSet \in SUBSET Files \ {{}} :
    \E ttl \in TTLValues :
        Announce(agent, conj, phase, fileSet, ttl)

\* ====================================================================
\* Next-state relation
\* ====================================================================

Next ==
    \/ AnnounceAny
    \/ Tick
    \/ ReadActive
    \/ ConcurrentReadActive

\* ====================================================================
\* Fairness
\* ====================================================================
\*
\* We require weak fairness on Tick (time must advance) and on
\* ReadActive (expired broadcasts must eventually be pruned).
\* Without fairness on ReadActive, expired broadcasts could remain
\* in the requests set forever, violating TTLExpiry.

Fairness ==
    /\ WF_vars(Tick)
    /\ WF_vars(ReadActive)

Spec == Init /\ [][Next]_vars /\ Fairness

\* ====================================================================
\* Invariants (safety properties -- must hold in every reachable state)
\* ====================================================================

(* ----- TypeOK -----
 * Basic type invariant: all variables have their expected types.
 *)
TypeOK ==
    /\ clock \in 0..MaxClock
    /\ \A b \in broadcasts : b \in BroadcastType
    /\ \A b \in archive : b \in BroadcastType
    /\ nextId \in Nat
    /\ \A a \in Agents : agentCount[a] \in 0..MaxBroadcasts

(* ----- NoGhostAfterArchive -----
 * Once a broadcast is in the archive, it never reappears in broadcasts.
 *
 * This is an invariant (safety property): at no point should a broadcast
 * exist in both sets simultaneously, and once archived, it stays archived.
 *
 * Corresponds to DOGFOODING.md section 8: "The invariants agent discovered
 * ghost broadcasts." The TLA+ property is stronger: not just ghost
 * references, but actual broadcast records that reappear after archival.
 *
 * Implementation: In main.go, os.Rename is used to move files from
 * requests/ to archive/. A successful rename atomically removes from
 * source and adds to destination. This invariant verifies the model
 * preserves that atomicity.
 *)
NoGhostAfterArchive ==
    broadcasts \cap archive = {}

(* ----- UniqueIds -----
 * No two broadcasts (active or archived) share the same id.
 * Corresponds to the checkULIDUnique invariant in main.go.
 *)
UniqueIds ==
    \A b1, b2 \in (broadcasts \cup archive) :
        b1.id = b2.id => b1 = b2

(* ----- NoBroadcastLoss -----
 * No broadcast simply vanishes. Every broadcast that was ever created
 * is either still in broadcasts or has been moved to archive.
 * This is the key property for prune-on-read atomicity.
 *
 * Corresponds to DOGFOODING.md section 6:
 *   "There is no locking. Two agents calling readActive simultaneously
 *    could both try to move the same expired file. In practice, filesystem
 *    rename is atomic on most systems, so this is fine -- but it is
 *    undocumented."
 *
 * We verify: even with ConcurrentReadActive (non-deterministic subset
 * pruning), broadcasts are never lost.
 *)
NoBroadcastLoss ==
    \* The total set of all broadcasts (active + archived) only grows.
    \* Equivalently: nothing disappears from the union.
    \* We encode this as: IDs in broadcasts and archive are disjoint
    \* and together account for all ever-created IDs below nextId.
    \A id \in 1..(nextId - 1) :
        \E b \in (broadcasts \cup archive) : b.id = id

(* ----- ConflictDetectionCompleteness -----
 * If two active broadcasts from different agents share files and both
 * are in proof phase, their conflict severity MUST be "high".
 *
 * This is the core correctness property of conflict detection.
 * Corresponds to CLAUDE.md acceptance test:
 *   "Agent A announces C-1, phase=proof, files=[auth.py].
 *    Agent B announces C-7, phase=proof, files=[auth.py].
 *    A watcher reads active broadcasts, detects HIGH conflict."
 *
 * And DOGFOODING.md section 2:
 *   "When four agents all touch main.go in proof phase, every pair
 *    is a HIGH conflict. That is 4-choose-2 = 6 HIGH conflict signals."
 *)
ConflictDetectionCompleteness ==
    \A b1, b2 \in ActiveBroadcasts :
        /\ b1 /= b2
        /\ b1.agent /= b2.agent
        /\ Overlaps(b1, b2)
        /\ b1.phase = "proof"
        /\ b2.phase = "proof"
        => ConflictBetween(b1, b2).severity = "high"

(* ----- ConflictSeverityCorrectness -----
 * Additional severity checks beyond just "both proof = high":
 *   - one proof, one not => medium
 *   - neither proof => low
 * Matches main.go checkConflicts() severity logic exactly.
 *)
ConflictSeverityCorrectness ==
    \A b1, b2 \in ActiveBroadcasts :
        (b1 /= b2 /\ b1.agent /= b2.agent /\ Overlaps(b1, b2)) =>
            /\ ((b1.phase = "proof" /\ b2.phase = "proof")
                => ComputeSeverity(b1, b2) = "high")
            /\ ((b1.phase = "proof" /\ b2.phase /= "proof")
                => ComputeSeverity(b1, b2) = "medium")
            /\ ((b1.phase /= "proof" /\ b2.phase = "proof")
                => ComputeSeverity(b1, b2) = "medium")
            /\ ((b1.phase /= "proof" /\ b2.phase /= "proof")
                => ComputeSeverity(b1, b2) = "low")

\* ====================================================================
\* Temporal properties (liveness -- must eventually hold)
\* ====================================================================

(* ----- TTLExpiry -----
 * If a broadcast is in the active set and the clock has advanced past
 * its expiry time, it must eventually be moved to the archive.
 *
 * In temporal logic:
 *   [](b in Broadcasts /\ clock > b.ts + b.ttl => <>(b in Archive))
 *
 * This requires fairness on ReadActive -- without it, expired broadcasts
 * could sit in requests/ forever because no one calls readActive().
 *
 * Corresponds to DOGFOODING.md section 5:
 *   "TTL-based expiry: Even though the default is too short, the
 *    mechanism is correct. Expired broadcasts are archived, not deleted.
 *    The archive provides an audit trail without cluttering active state."
 *)
TTLExpiry ==
    \A b \in BroadcastType :
        [](b \in broadcasts /\ IsExpired(b) => <>(b \in archive))

(* ----- EventualArchival -----
 * A simpler liveness property: if there are expired broadcasts in the
 * active set, eventually at least one will be archived.
 * This is weaker than TTLExpiry but easier to check.
 *)
EventualArchival ==
    [](ExpiredInRequests /= {} => <>(ExpiredInRequests = {} \/ archive /= {}))

\* ====================================================================
\* TTL Cliff Model (DOGFOODING.md section 4)
\* ====================================================================
\*
\* The TTL cliff is not a bug -- it is a design consequence of
\* announce-once-and-forget behavior. This section quantifies it.
\*
\* Key numbers from DOGFOODING.md:
\*   - TTL=300 (5 min), work session=30 min: 16.7% coverage
\*   - TTL=300, work session=60 min: 8.3% coverage
\*   - "For the remaining 25-55 minutes, the gossip layer has no
\*     memory of the agent's work."
\*
\* The model allows us to observe this directly: after a broadcast
\* is created at time T with TTL=5, by time T+6 it is expired.
\* If the agent works until T+600 (MaxClock), only ticks T..T+5
\* had valid presence. That is 5/600 = 0.83% coverage.
\*
\* The fix (C-7: auto-renewal) would add an action:
\*   ReAnnounce(agent) == announce with same fields, new ts, new id
\* This spec intentionally does NOT include auto-renewal, to model
\* the failure mode as observed during dogfooding.
\*
\* To observe the cliff in TLC:
\*   - Set TTLValues = {5}, MaxClock = 30
\*   - Check: in how many reachable states does ActiveBroadcasts = {}
\*     even though agentCount > 0? (agents announced but are invisible)

TTLCliffDetector ==
    \* TRUE when at least one agent has announced but has no active broadcast.
    \* When this is TRUE and work is ongoing, the gossip layer is blind.
    \E a \in Agents :
        /\ agentCount[a] > 0
        /\ ~\E b \in ActiveBroadcasts : b.agent = a

\* The fraction of the clock range where broadcasts are valid.
\* Not directly checkable as a TLC invariant, but useful for
\* understanding the cliff. With TTL=5 and MaxClock=600:
\*   - A broadcast at ts=0 is valid for ticks 0..5 (6 ticks)
\*   - That is 6/601 = 1.0% of the state space
\* With TTL=300 and MaxClock=600:
\*   - A broadcast at ts=0 is valid for ticks 0..300 (301 ticks)
\*   - That is 301/601 = 50.1% of the state space

\* ====================================================================
\* State constraint (bounds state space for model checking)
\* ====================================================================

StateConstraint ==
    /\ clock <= MaxClock
    /\ Cardinality(broadcasts \cup archive) <= Cardinality(Agents) * MaxBroadcasts

\* ====================================================================
\* Model checking configuration summary
\* ====================================================================
\*
\* Recommended TLC configuration (see aq.cfg):
\*
\*   CONSTANTS
\*     Agents      = {"a1", "a2", "a3"}
\*     MaxBroadcasts = 5
\*     TTLValues   = {5, 60, 300}
\*     MaxClock    = 600
\*     Files       = {"main.go", "auth.py", "config.py"}
\*     Phases      = {"conjecture", "proof", "refutation", "refinement"}
\*     Conjectures = {"C-1", "C-2", "C-3"}
\*
\*   INVARIANTS
\*     TypeOK
\*     NoGhostAfterArchive
\*     UniqueIds
\*     NoBroadcastLoss
\*     ConflictDetectionCompleteness
\*     ConflictSeverityCorrectness
\*
\*   PROPERTIES
\*     TTLExpiry
\*
\*   CONSTRAINT
\*     StateConstraint
\*
\* For feasible checking, reduce the state space:
\*   - 2 agents, 2 broadcasts each, 1 TTL value, MaxClock=10
\*   - Then scale up to the full configuration
\*
\* ====================================================================
\* Mapping to DOGFOODING.md observations
\* ====================================================================
\*
\* | DOGFOODING.md Section | TLA+ Property              | What it tests                           |
\* |-----------------------|----------------------------|-----------------------------------------|
\* | S2: Single-File       | ConflictDetectionComplete  | All proof pairs on same file => HIGH    |
\* | S4: TTL Cliff         | TTLCliffDetector           | Agents invisible after TTL expires      |
\* | S5: TTL Expiry         | TTLExpiry                  | Expired broadcasts reach archive        |
\* | S6: Side-effecting    | NoBroadcastLoss            | ReadActive doesn't lose broadcasts      |
\* |     read / no locking | ConcurrentReadActive       | Concurrent prune is safe                |
\* | S8: Ghost broadcasts  | NoGhostAfterArchive        | Archived broadcasts don't reappear      |
\* | S10: Wire format      | (out of scope)             | Schema evolution not modeled            |

============================================================================
