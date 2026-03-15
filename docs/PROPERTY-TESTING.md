# Property-Based Testing for aq

## What is Property-Based Testing

Property-based testing (PBT) inverts the relationship between test author
and test data. Instead of writing individual example inputs and expected
outputs, the author specifies *invariants* (properties) that must hold for
*all* valid inputs. The framework then generates hundreds or thousands of
random inputs and checks that every property holds.

When a property fails, the framework *shrinks* the failing input to the
smallest reproduction case. This often surfaces edge cases that hand-written
examples miss: empty strings, boundary values, unicode, very large inputs,
and degenerate combinations.

### Go Libraries for PBT

| Library                    | Approach                | Shrinking | Notes                            |
|----------------------------|-------------------------|-----------|----------------------------------|
| `testing/quick` (stdlib)   | Random value generation | No        | No external deps, basic but sufficient |
| `pgregory.net/rapid`       | Integrated shrinking    | Yes       | Best Go PBT library, fast        |
| `leanovate/gopter`         | ScalaCheck-style        | Yes       | Mature, compositional generators  |

For aq, `testing/quick` is preferred because aq has zero runtime dependencies,
and `testing/quick` is in the standard library. Where shrinking matters (complex
struct inputs), `rapid` would be the upgrade path.

## Properties aq Should Satisfy

### Property 1: Broadcast Roundtrip

For any valid Broadcast, marshalling to JSON and unmarshalling back produces
an identical Broadcast.

```go
func TestProperty_BroadcastRoundtrip(t *testing.T) {
    f := func(agent, worktree, cid, claim, phase, status string,
              files []string, ts float64, ttl int, id string) bool {
        b := Broadcast{
            Agent: agent, Worktree: worktree,
            ConjectureID: cid, ConjectureClaim: claim,
            Phase: phase, Status: status,
            Files: files, Ts: ts, TTL: ttl, ID: id,
        }
        j, err := b.ToJSON()
        if err != nil {
            return true // unmarshalable input, skip
        }
        restored, err := BroadcastFromJSON(j)
        if err != nil {
            return false
        }
        return reflect.DeepEqual(b, restored)
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: JSON field naming (`json:"conjecture_id"` vs Go field
`ConjectureID`) is a common source of roundtrip failures. This property
ensures the wire format is lossless.

### Property 2: TTL Monotonicity

For any Broadcast with TTL > 0, `IsExpired()` returns false at creation time
and true after TTL seconds have elapsed.

```go
func TestProperty_TTLMonotonicity(t *testing.T) {
    f := func(ttl uint16) bool {
        if ttl == 0 {
            return true // TTL=0 has undefined expiry behavior, skip
        }
        b := Broadcast{
            Ts:  float64(time.Now().Unix()),
            TTL: int(ttl),
        }
        if b.IsExpired() {
            return false // should not be expired at creation
        }
        // Simulate time advancement by backdating the timestamp
        b.Ts = float64(time.Now().Unix()) - float64(ttl) - 1
        return b.IsExpired()
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: TTL is the heartbeat of the gossip layer. If expiry is
non-monotonic, broadcasts either linger forever (ghost broadcasts, C-7) or
disappear prematurely (information loss).

### Property 3: Conflict Symmetry

`checkConflicts(a, b)` detects a conflict if and only if `checkConflicts(b, a)`
detects a conflict. This property was proven in Lean 4 for the abstract model;
PBT validates the implementation matches the proof.

```go
func TestProperty_ConflictSymmetry(t *testing.T) {
    // This requires filesystem setup, so we test the Overlaps method
    // which is the pure function underlying conflict detection.
    f := func(filesA, filesB []string) bool {
        a := Broadcast{Files: filesA}
        b := Broadcast{Files: filesB}
        return a.Overlaps(&b) == b.Overlaps(&a)
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: Asymmetric conflict detection means Agent A sees a
conflict with Agent B but Agent B does not see it with Agent A. This would
undermine the gossip model -- both peers must agree on overlap.

### Property 4: Conflict Severity Determinism

Same inputs always produce the same severity level. No randomness, no
time-dependence in severity computation.

```go
func TestProperty_ConflictSeverityDeterminism(t *testing.T) {
    phases := []string{"conjecture", "proof", "refutation", "refinement"}
    f := func(phaseIdxA, phaseIdxB uint8) bool {
        pA := phases[int(phaseIdxA)%len(phases)]
        pB := phases[int(phaseIdxB)%len(phases)]
        // Compute severity twice with same inputs
        s1 := computeSeverity(pA, pB)
        s2 := computeSeverity(pA, pB)
        return s1 == s2
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}

// computeSeverity mirrors the logic in checkConflicts
func computeSeverity(phaseA, phaseB string) string {
    bothProof := phaseA == "proof" && phaseB == "proof"
    oneProof := phaseA == "proof" || phaseB == "proof"
    if bothProof {
        return "high"
    } else if oneProof {
        return "medium"
    }
    return "low"
}
```

**Why it matters**: Severity drives agent behavior. Non-deterministic severity
would cause agents to disagree on urgency, breaking the gossip contract.

### Property 5: ULID Uniqueness

For any N calls to `generateULID()`, all results are unique.

```go
func TestProperty_ULIDUniqueness(t *testing.T) {
    f := func(n uint8) bool {
        count := int(n) + 1 // at least 1
        seen := make(map[string]struct{}, count)
        for i := 0; i < count; i++ {
            id := generateULID()
            if _, dup := seen[id]; dup {
                return false
            }
            seen[id] = struct{}{}
        }
        return true
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: ULIDs are broadcast identifiers. Collisions cause data
loss (overwritten files) or phantom duplicates (invariant check failures).

### Property 6: ULID Ordering

For ULIDs generated at time T1 < T2, the T1 ULID's timestamp prefix sorts
lexicographically before T2's.

```go
func TestProperty_ULIDOrdering(t *testing.T) {
    // testing/quick can't easily generate time-separated pairs,
    // so this is best tested with rapid. Using a direct test:
    f := func(delayMs uint8) bool {
        delay := time.Duration(delayMs+1) * time.Millisecond
        id1 := generateULID()
        time.Sleep(delay)
        id2 := generateULID()
        // Timestamp portion (first 12 chars) should be ordered
        return id1[:12] <= id2[:12]
    }
    cfg := &quick.Config{MaxCount: 20} // limited due to sleep
    if err := quick.Check(f, cfg); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: ULID ordering enables chronological file listing without
parsing timestamps. If ordering breaks, `readActive` returns broadcasts in
wrong order.

### Property 7: Prune-on-Read Idempotency

Calling `readActive()` twice in succession returns the same active set
(modulo time advancement between calls).

```go
func TestProperty_PruneOnReadIdempotency(t *testing.T, home string) {
    // Requires filesystem, tested with a helper
    active1, err1 := readActive("broadcast")
    active2, err2 := readActive("broadcast")
    if err1 != nil || err2 != nil {
        t.Fatalf("readActive errors: %v, %v", err1, err2)
    }
    if len(active1) != len(active2) {
        t.Errorf("idempotency violation: %d vs %d active", len(active1), len(active2))
    }
}
```

**Why it matters**: If pruning is not idempotent, successive reads produce
different views of the world. Agents would see flickering state.

### Property 8: Archive Completeness

Every broadcast that expires is eventually archived (no lost files). The
total of active + archived broadcasts equals the total written.

```go
func TestProperty_ArchiveCompleteness(t *testing.T) {
    // Write N broadcasts, wait for expiry, verify all archived
    // This is inherently stateful -- see the chaos test
    // TestChaos_ArchiveRace for the concurrent version.
}
```

**Why it matters**: Lost broadcasts mean lost provenance. The archive is the
audit trail of agent activity.

### Property 9: No False Negatives on Conjecture Match

If two broadcasts share a `conjecture_id` and overlapping files with
different agents, `checkConflicts` MUST return a signal.

```go
func TestProperty_NoFalseNegatives(t *testing.T) {
    f := func(sharedFile string) bool {
        if sharedFile == "" {
            return true // empty filename, skip
        }
        home := t.TempDir()
        t.Setenv("AQ_HOME", home)

        other := Broadcast{
            Agent: "agent-other", Worktree: "main",
            ConjectureID: "C-1", Phase: "proof",
            Status: "prosecuting",
            Files: []string{sharedFile},
            Ts: float64(time.Now().Unix()), TTL: 3600,
            ID: generateULID(),
        }
        writeBroadcast(other, "broadcast")

        me := Broadcast{
            Agent: "agent-me", Worktree: "main",
            ConjectureID: "C-2", Phase: "proof",
            Files: []string{sharedFile},
        }
        signals, err := checkConflicts(me, "broadcast")
        if err != nil {
            return false
        }
        return len(signals) > 0
    }
    cfg := &quick.Config{MaxCount: 50}
    if err := quick.Check(f, cfg); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: False negatives are the primary failure mode for a
conflict detection system. If overlapping proof-phase work goes undetected,
the merge wall arrives without warning. This is the core value proposition
of aq (C-2).

### Property 10: Phase Severity Ordering

For any file overlap:
`severity(both_proof) >= severity(one_proof) >= severity(neither_proof)`

```go
func TestProperty_PhaseSeverityOrdering(t *testing.T) {
    f := func(phaseA, phaseB uint8) bool {
        phases := []string{"conjecture", "proof", "refutation", "refinement"}
        pA := phases[int(phaseA)%len(phases)]
        pB := phases[int(phaseB)%len(phases)]

        s := computeSeverity(pA, pB)
        sHigh := computeSeverity("proof", "proof")
        sLow := computeSeverity("conjecture", "conjecture")

        // high is most severe, low is least severe
        return severityRank(sHigh) <= severityRank(s) &&
               severityRank(s) <= severityRank(sLow)
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: Validates conjecture C-4 -- CPRR phase modulates conflict
severity. If the ordering breaks, agents over- or under-react to conflicts.

### Property 11: Done-Status Exclusion

A broadcast with `status=done` never appears in conflict signals.

```go
func TestProperty_DoneStatusExclusion(t *testing.T) {
    // For any set of broadcasts where one has status=done,
    // that broadcast never appears as B in any ConflictSignal.
    // This is validated by TestCheckConflicts_SkipsDoneStatus
    // and would be strengthened by PBT with random file/phase combos.
}
```

**Why it matters**: Done broadcasts are noise. Including them in conflict
signals would cause false alarms after agents complete their work.

### Property 12: File List Commutativity

Conflict detection is independent of file list ordering. The files
`["a.go", "b.go"]` and `["b.go", "a.go"]` produce identical conflict
signals.

```go
func TestProperty_FileListCommutativity(t *testing.T) {
    f := func(files []string) bool {
        if len(files) == 0 {
            return true
        }
        a := Broadcast{Files: files}
        // Reverse the file list
        reversed := make([]string, len(files))
        for i, f := range files {
            reversed[len(files)-1-i] = f
        }
        b := Broadcast{Files: reversed}
        other := Broadcast{Files: files[:1]} // single shared file candidate

        return a.Overlaps(&other) == b.Overlaps(&other)
    }
    if err := quick.Check(f, nil); err != nil {
        t.Error(err)
    }
}
```

**Why it matters**: File lists come from CLI arguments, git status, or
agent introspection. Ordering should never affect conflict detection.

## Shrinking and Edge Cases

### What PBT finds that example tests miss

PBT generators will probe boundaries that humans forget:

- **Empty strings**: Agent name `""`, conjecture ID `""`, file path `""`.
  Does `checkConflicts` handle empty agents (would match other empty agents)?
  Does `Overlaps` return true for two broadcasts both containing `""`?

- **Unicode in claims**: ConjectureClaim containing `"\x00"`, emoji, RTL
  characters, or multi-byte UTF-8 sequences. JSON roundtrip must preserve
  these exactly.

- **Very long file lists**: 10,000 files in a broadcast. Does `Overlaps`
  degrade to O(N*M)? (Current implementation uses a map, so it is O(N+M).)

- **TTL=0**: Is this "immediately expired" or "never expires"? The current
  implementation treats it as "expires when `now > Ts+0`", so it expires
  within 1 second. PBT would catch if any code path assumes TTL>0.

- **TTL=MaxInt**: Overflow risk in `b.Ts + float64(b.TTL)`. With float64,
  values above 2^53 lose precision. MaxInt (2^63-1) would cause silent
  truncation.

- **Negative timestamps**: `Ts = -1.0`. The `IsExpired` check computes
  `now > -1.0 + float64(TTL)`. With TTL=3600, this means expired at
  timestamp 3599 (year 1970). Not a realistic input, but PBT would find it.

- **Duplicate files in a list**: `["a.go", "a.go"]`. Does `Overlaps` count
  this as 1 or 2 shared files? Current implementation: 1 (map deduplication
  on one side).

- **NaN and Inf timestamps**: `Ts = math.NaN()` or `Ts = math.Inf(1)`.
  JSON roundtrip behavior for these is implementation-defined. Go's
  `json.Marshal` returns an error for NaN/Inf, so `ToJSON()` would fail.

### How `rapid.Check` shrinks failing cases

`rapid` (if adopted) uses integrated shrinking: when a generated input fails
a property, the framework binary-searches for smaller inputs that still fail.
For example:

1. Generated input: `files = ["alpha.go", "beta.go", ..., "omega.go"]`
   (26 files) causes failure.
2. Shrinking removes files one at a time: `["alpha.go"]` still fails?
   No. `["alpha.go", "beta.go"]` still fails? Yes.
3. Shrinks string content: `["a", "b"]` still fails? Yes.
4. Final minimal reproduction: `["a", "b"]` -- the smallest input
   triggering the bug.

This is vastly more efficient than manual debugging. For aq, shrinking
would be most useful for:
- Finding the minimum number of broadcasts that triggers a race condition
- Finding the shortest file path that causes a false negative
- Finding the minimum TTL value that triggers ghost broadcasts

## Concrete Implementation Sketch

The following tests use `testing/quick` from the Go standard library and
can be added directly to `main_test.go`. No external dependencies required.

```go
package main

import (
    "reflect"
    "testing"
    "testing/quick"
    "time"
)

// Property: Broadcast JSON roundtrip is lossless for all string fields.
func TestQuick_BroadcastRoundtrip(t *testing.T) {
    f := func(agent, worktree, cid, claim, id string, ttl uint16) bool {
        b := Broadcast{
            Agent:           agent,
            Worktree:        worktree,
            ConjectureID:    cid,
            ConjectureClaim: claim,
            Phase:           "proof",
            Status:          "prosecuting",
            Files:           []string{"main.go"},
            Ts:              1700000000,
            TTL:             int(ttl),
            ID:              id,
        }
        j, err := b.ToJSON()
        if err != nil {
            // NaN/Inf in Ts would cause this; skip
            return true
        }
        restored, err := BroadcastFromJSON(j)
        if err != nil {
            return false
        }
        return b.Agent == restored.Agent &&
            b.Worktree == restored.Worktree &&
            b.ConjectureID == restored.ConjectureID &&
            b.ConjectureClaim == restored.ConjectureClaim &&
            b.Phase == restored.Phase &&
            b.Status == restored.Status &&
            b.TTL == restored.TTL &&
            b.ID == restored.ID &&
            len(b.Files) == len(restored.Files)
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
        t.Error(err)
    }
}

// Property: Overlaps is symmetric (conflict symmetry).
func TestQuick_OverlapsSymmetric(t *testing.T) {
    f := func(filesA, filesB []string) bool {
        a := Broadcast{Files: filesA}
        b := Broadcast{Files: filesB}
        return a.Overlaps(&b) == b.Overlaps(&a)
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
        t.Error(err)
    }
}

// Property: TTL expiry is monotonic -- not expired at creation,
// expired after TTL seconds.
func TestQuick_TTLMonotonic(t *testing.T) {
    f := func(ttl uint16) bool {
        if ttl == 0 {
            return true // edge case: TTL=0 means immediate expiry
        }
        now := float64(time.Now().Unix())
        b := Broadcast{Ts: now, TTL: int(ttl)}
        if b.IsExpired() {
            return false // must not be expired at creation
        }
        // Backdate to simulate passage of time
        b.Ts = now - float64(ttl) - 1
        return b.IsExpired() // must be expired after TTL+1 seconds
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
        t.Error(err)
    }
}

// Property: ULID uniqueness -- N generated ULIDs are all distinct.
func TestQuick_ULIDUnique(t *testing.T) {
    f := func(n uint8) bool {
        count := int(n)%50 + 2 // 2..51
        seen := make(map[string]struct{}, count)
        for i := 0; i < count; i++ {
            id := generateULID()
            if _, dup := seen[id]; dup {
                return false
            }
            seen[id] = struct{}{}
        }
        return true
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
        t.Error(err)
    }
}
```

These four tests cover the highest-value properties: data integrity
(roundtrip), correctness (symmetry, monotonicity), and identity (uniqueness).
They run in milliseconds, require no filesystem setup, and use only stdlib.
