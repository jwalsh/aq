# Beyond File Locking: Concept-Level Conflict Detection in aq

Date: 2026-03-14
Author: Research agent (deep dive on conflict detection evolution)
Conjectures: C-2, C-4, C-8, C-9 (proposed)
Status: Research document, not specification

---

## Abstract

aq's conflict detection currently keys on **file-name intersection** between
broadcasts, modulated by CPRR phase. This is `checkConflicts()` in main.go
(lines 907-964) and `check_conflicts()` in conflict.py --- identical logic in
both implementations:

```
shared = set(me.files) & set(other.files)
if not shared: continue     # <-- the entire gate
severity = f(both_proof, one_proof)
```

The problem: if `len(shared) == 0`, no conflict signal fires, regardless of
what the agents are *doing*. Two agents pursuing "add OAuth" and "remove
authentication middleware" in different files register zero conflict. This is
a false negative on the most dangerous kind of collision --- architectural
contradiction.

The CPRR payload already carries semantic data (`conjecture_id`,
`conjecture_claim`, `phase`), but `checkConflicts()` only uses file
intersection as the gate and phase as the severity modifier. The conjecture
fields are write-only: they appear in the broadcast, they appear in `aq
status` output, but they never influence conflict detection.

This document argues that file-level overlap is necessary but insufficient,
traces the historical evidence for why file locking fails, proposes a
taxonomy of collaboration conflicts, and outlines concrete mechanisms for
concept-level conflict detection that respect aq's foundational axiom:
gossip, not coordination.

---

## 1. History of File Locking --- What Failed and Why

### 1.1 Unix V4 (1973) --- No Locking at All

Ken Thompson's original Unix filesystem had no file locking mechanism. The
philosophy was simple: the filesystem is a shared namespace, and if two
processes write to the same file, the last writer wins. There was no
`flock()`, no `fcntl()` locking, no advisory locks. Those came later (flock
in 4.2BSD, 1983; fcntl locking in System V).

This was not an oversight. The PDP-11 systems running Unix V4 typically had
one or two users. The design assumption was that collisions were rare enough
to handle socially ("Hey Ken, are you editing passwd?") rather than
mechanically. The Unix culture that emerged from this --- text files,
pipelines, small tools --- assumed that coordination was a human problem, not
a system problem.

The contrast with mainframe culture is instructive. IBM's MVS (1974) had
elaborate dataset locking, VSAM record-level locking, and enqueue/dequeue
primitives for serialized access. Mainframes assumed multiple batch jobs
competing for the same files. Unix assumed one person in a terminal.

**Lesson for aq**: The original Unix model is aq's spiritual ancestor. No
locks, no prevention, just a shared namespace. The difference: aq adds
*awareness* ("someone else is in this namespace") without adding *prevention*
("you cannot enter this namespace"). Thompson's design worked because the
team was small enough for social coordination. aq extends that model to
agents who cannot coordinate socially.

### 1.2 RCS/SCCS (1982) --- Exclusive Checkout

Walter Tichy's RCS (Revision Control System, 1982) and Marc Rochkind's SCCS
(Source Code Control System, 1972) introduced the exclusive checkout model:
one writer at a time per file. To edit a file, you ran `co -l` (RCS) or `get
-e` (SCCS), which placed a lock. Anyone else attempting to lock the same file
would be rejected until you checked it back in.

This solved the last-writer-wins problem but introduced a new one: **lock
contention**. If developer A locked `config.h` and went to lunch, developer B
could not edit `config.h` until A returned. The solution was usually social:
walk to A's desk, ask them to unlock. In distributed teams, this became email
or phone calls. The lock was a coordination mechanism that required
coordination to manage.

RCS locks were per-file and binary: locked or unlocked. There was no concept
of "I locked it but I'm only changing the header comment" versus "I locked it
and I'm rewriting the API." The granularity of the lock (the file) did not
match the granularity of the conflict (the intent).

### 1.3 Visual SourceSafe (1994-2005) --- File Locking as Product

Microsoft Visual SourceSafe (VSS) made file-level exclusive locking the
primary collaboration model for an entire generation of Windows developers.
The workflow:

1. "Check out" a file (acquires exclusive lock)
2. Edit the file locally
3. "Check in" the file (releases lock, merges changes)

What went wrong:

- **Lock contention at scale**: In teams of 10+, the "I need that file"
  conversation became a daily ritual. Developers would check out files
  preemptively ("I might need this later"), creating phantom locks.
- **The "checked out by" dance**: VSS showed who had a file locked. This
  turned into a human coordination protocol: walk to their desk, send an IM,
  wait for them to finish. The tool that was supposed to eliminate
  coordination created more of it.
- **Binary thinking**: A file was either locked or available. There was no
  concept of shared/concurrent editing, no severity levels, no "I'm only
  touching the header." The lock carried no semantic information about what
  the locker intended to do.
- **Corruption**: VSS famously corrupted its database under concurrent
  access. Microsoft's own internal teams migrated away. The tool's own
  implementation could not handle the concurrency model it imposed on users.
- **Microsoft abandoned it**: Microsoft replaced VSS with Team Foundation
  Server (2005), which supported both exclusive and shared checkouts, and
  eventually moved to Git (Azure DevOps, 2018). The company that built
  file-level locking ultimately rejected it.

**What VSS got right**: The *awareness* component. When you saw "checked out
by jsmith," you knew someone else was in that file. The information was
valuable. The *enforcement* (you cannot edit) was the problem, not the
*notification* (someone is editing).

**Lesson for aq**: aq is VSS's awareness model without VSS's enforcement
model. A broadcast is "checked out by agent-alpha" without the lock. This is
correct. But aq's current conflict detection *also* inherits VSS's failure
mode: it treats the file as the unit of conflict. Two agents on the same file
= conflict. Two agents on different files = no conflict. This is exactly
VSS's model, minus the lock. The awareness is better than nothing, but the
granularity is wrong.

### 1.4 CVS/SVN (1990s-2000s) --- Optimistic Concurrency

CVS (Concurrent Versions System, Dick Grune, 1986; Brian Berliner's
rewrite, 1989) and SVN (Subversion, CollabNet, 2000) shifted the model from
"prevent conflict" to "detect and resolve conflict." Both allowed multiple
developers to edit the same file simultaneously. Conflicts were detected at
commit/update time and resolved by the developer via three-way merge.

This was a philosophical breakthrough: **conflict is not a bug, it's a
feature.** The system's job is not to prevent overlap but to make overlap
visible and resolvable. The developer is trusted to handle the resolution.

CVS/SVN still tracked conflicts at the file level (and, in practice, at the
line level during merge). But the key insight was that most concurrent edits
to the same file are to *different parts* of the file and merge cleanly.
The file is the wrong unit --- the *change* is the right unit.

**Lesson for aq**: CVS/SVN's optimistic model is aq's model. Broadcasts
carry no obligation. Conflicts are advisory. But aq should learn from the
next step CVS/SVN took: they stopped treating "same file" as the conflict
signal and started looking at *what changed within the file*. aq's conflict
detection has not yet made this transition (still pending; see conjecture C-8 for function-level granularity proposal).

### 1.5 Git (2005) --- No Locking, Branch Isolation

Linus Torvalds designed Git with no locking mechanism at all. The model:

1. Every developer works on their own branch (full copy of the repository)
2. Changes are committed locally with no coordination
3. Conflict detection happens at merge time, not edit time
4. Merge conflicts are resolved by the person doing the merge

Git's philosophical contribution: **the unit of collaboration is the branch,
not the file.** Two developers editing the same file on different branches
is not a conflict until someone tries to merge. And even then, most merges
succeed automatically because the changes are to different hunks.

Git has no concept of "developer A is editing auth.py right now." There is
no presence awareness. This is a deliberate design choice: Torvalds argued
that knowing what someone is working on creates a false sense of
coordination. You do not need to know; you need to handle the merge.

**What Git lacks**: The thing aq provides --- ambient presence. Git's merge
conflicts are discovered late (at merge time), after significant work has
diverged. aq's value proposition is detecting *potential* conflicts early
(at work time), before divergence accumulates. Git is reactive; aq is
proactive. Both are optimistic; neither locks.

### 1.6 Git LFS File Locking (2017) --- Locking Returns

Git Large File Storage added file locking in 2017, twelve years after Git's
creation. The motivation: binary files (images, models, compiled assets)
cannot be merged. When two people edit the same PNG, there is no three-way
merge --- one change must be discarded.

Git LFS locking is explicitly scoped: it applies only to files that cannot
be merged. Text files, source code, configuration --- all remain unlocked.
The lesson is precise: **locking is needed only when merge is impossible.**
For mergeable content, optimistic concurrency is strictly better.

**Lesson for aq**: aq should never lock (axiom: gossip, not coordination).
But the Git LFS precedent clarifies *why*: source code is mergeable. The
risk is not that two agents edit the same file --- git handles that. The
risk is that two agents pursue *incompatible goals* --- and that is not
detectable at the file level.

### 1.7 The Historical Arc

```
1973  Unix V4      No locking.           Social coordination.
1982  RCS/SCCS     Exclusive file lock.  Prevents concurrent edit.
1994  VSS          Exclusive file lock.  "Checked out by jsmith."
1990  CVS          Optimistic merge.     Same file OK, detect at commit.
2000  SVN          Optimistic merge.     Same file OK, detect at update.
2005  Git          No locking.           Branch isolation, merge at will.
2017  Git LFS      Locking returns.      But only for unmergeable binaries.
2026  aq           Broadcast presence.   Same file = awareness, not lock.
```

The trajectory is clear: **the industry moved from file-level prevention to
intent-level awareness.** RCS locked files. CVS detected file conflicts.
Git detected hunk conflicts. aq should detect *concept conflicts*.

---

## 2. Unix V4 and Username-as-Directory --- Namespace as Identity

### 2.1 `/usr/{username}` as Constraint Model

In early Unix (V4-V6), user home directories lived at `/usr/{username}`.
The user's identity was embedded in the filesystem namespace. There was no
separate user database (that came later with `/etc/passwd`); the directory
*was* the identity. If `/usr/ken` existed, Ken Thompson existed.

This pattern --- identity embedded in namespace --- is a powerful constraint
model. It means:

- **Discovery is trivial**: `ls /usr/` shows all users. No query needed.
- **Isolation is structural**: Ken's files are in Ken's directory. No ACL
  needed for basic separation.
- **Collision is visible**: If two users need the same path, the namespace
  conflict is immediately apparent.

### 2.2 How This Maps to aq's Agent Field

aq's `agent` field (`{remote}/{branch}` or worktree address) is the
namespace identity. It answers "who is broadcasting?" the same way
`/usr/ken` answers "whose files are these?" The agent address IS the
namespace.

Currently, broadcasts are stored flat in
`~/.aq/channels/broadcast/requests/`. All agents' broadcasts share one
directory. The Unix V4 analogy would suggest a different structure:

```
~/.aq/agents/
    github.com-jwalsh-aq-feature-auth/
        C-1-proof-auth.py.json
        C-1-proof-session.py.json
    github.com-jwalsh-aq-feature-db/
        C-3-conjecture-schema.py.json
```

In this model, each agent has its own directory --- its own namespace.
"Conflict" is not about file overlap within a flat directory; it is about
*namespace intersection at the concept level*. Two agents with overlapping
concepts exist in overlapping namespaces, regardless of which files they
touch.

### 2.3 The Plan 9 Extension --- Per-User Namespaces

Plan 9 from Bell Labs (Rob Pike, Ken Thompson, et al., 1992) extended the
Unix namespace model radically. Every process has its own namespace.
`/mnt` is a union mount point where each user can bind different
filesystems. The same path (`/dev/draw`) can resolve to different devices
for different users.

The relevance to aq: Plan 9's per-process namespaces mean that "the same
file" is a relative concept. `/dev/draw` for user A is not `/dev/draw` for
user B. Similarly, `auth.py` in worktree A is not `auth.py` in worktree B
--- they are separate files in separate branches. aq's current conflict
detection treats them as the same file because the *name* matches, even
though they are different objects in different namespaces.

This is the cross-repo false positive documented in DOGFOODING.md section
11: `aq/main.go` and `cprr/main.go` are different files in different
repositories, but `aq check` flags them as conflicting because the basename
matches. The Plan 9 solution: namespace-qualified paths. Not `main.go` but
`github.com/jwalsh/aq:main.go`.

### 2.4 Implications for aq's Namespace Design

The Unix V4 and Plan 9 models suggest that aq's conflict detection should
be namespace-aware:

1. **File paths should be repository-qualified**. `auth.py` is ambiguous.
   `github.com/jwalsh/aq:auth.py` is not.
2. **Agent identity should be structural**, not just a string field.
   An agent's broadcasts should live in an agent-namespaced directory.
3. **Conflict is namespace intersection**, not name collision. Two agents
   touching `auth.py` in different repos is not a conflict. Two agents
   touching the same *concept* across different repos might be.

---

## 3. Taxonomy of Collaboration Conflicts

File-level conflict detection exists at the bottom of a seven-level
hierarchy. Each level subsumes the one below and detects a broader class
of collaboration problems.

| Level | What Conflicts | Example | Detection Method | False Positive Rate | False Negative Rate |
|-------|---------------|---------|-----------------|-------------------|-------------------|
| **File** | Same filename modified | Both edit `auth.py` | Filename intersection (current aq) | High (single-file repos = 100%) | High (different files, same concept = 0%) |
| **Function** | Same function/symbol modified | Both edit `authenticate()` | AST/treesitter parse, `file:symbol` in broadcast | Medium | Medium (same concept in different functions) |
| **Module** | Same package/directory modified | Both touch the `auth/` package | Directory-prefix grouping | Medium | Medium |
| **Interface** | Contract/schema changed | A adds a field, B validates schema | Schema diff, wire format comparison | Low | Low-Medium |
| **Concept** | Same idea, different approach | "add OAuth" vs "remove auth middleware" | Claim similarity, tag overlap | Low | Low (if claims are descriptive) |
| **Architecture** | Structural direction conflict | "monolith" vs "microservices" | Conjecture-level incompatibility | Very Low | Depends on conjecture quality |
| **Goal** | Opposing objectives | "optimize for speed" vs "optimize for safety" | Claim contradiction detection | Very Low | Depends on claim expressiveness |

### 3.1 Why File-Level Is Both Necessary and Insufficient

File-level detection is necessary because it is the cheapest signal with the
highest true-positive rate for the specific case of "two agents editing the
same function in the same file." It requires no parsing, no NLP, no external
dependencies. It is a set intersection on strings.

It is insufficient because:

**False positives in single-file repos**: The dogfooding data (DOGFOODING.md
section 2) demonstrated a 100% false positive rate when four agents all
touched `main.go`. Every pair was flagged HIGH. The signal was constant and
therefore carried zero information. A conflict detector that always says
"conflict" is equivalent to no conflict detector.

**False negatives on concept collisions**: Consider two agents:

- Agent A: conjecture "OAuth support needed," phase=proof, files=[`oauth.py`, `config.py`]
- Agent B: conjecture "Remove authentication, use API gateway," phase=proof, files=[`gateway.py`, `deploy.yaml`]

Zero file overlap. Zero conflict signal. But these agents are pursuing
**mutually exclusive architectural goals**. When both merge, one will be
reverted. aq's current detection is blind to this.

**False positives across repos**: The dogfooding data (DOGFOODING.md section
11) showed that `aq/main.go` and `cprr/main.go` triggered a HIGH conflict
despite being in different repositories. The filename `main.go` is common
enough that cross-repo false positives are the default.

### 3.2 The Unit of Conflict Is Not the File --- It Is the Intent

Git's history teaches this lesson: the merge algorithm operates on *hunks*
(contiguous changed lines), not files. Two changes to the same file merge
cleanly if they touch different hunks. The hunk, not the file, is the unit
of conflict in git.

aq should learn the same lesson at a higher level of abstraction. The
broadcast payload already carries intent in two fields:

- `conjecture_id`: what hypothesis is being tested
- `conjecture_claim`: human-readable description of the work

These fields encode *why* the agent is working, not just *where*. A conflict
detection algorithm that ignores them is throwing away its best signal.

### 3.3 The Specific False Negative aq Must Fix

The problem statement from the introduction, made concrete:

```json
// Agent A's broadcast
{
  "agent": "github.com/jwalsh/aq/feature-oauth",
  "conjecture_id": "C-12",
  "conjecture_claim": "Adding OAuth2 support for API authentication",
  "phase": "proof",
  "files": ["oauth.py", "config.py", "requirements.txt"]
}

// Agent B's broadcast
{
  "agent": "github.com/jwalsh/aq/simplify-auth",
  "conjecture_id": "C-13",
  "conjecture_claim": "Removing custom auth in favor of API gateway",
  "phase": "proof",
  "files": ["gateway.py", "deploy.yaml", "terraform/main.tf"]
}
```

Current `checkConflicts()` result: **no conflict** (zero file overlap).

Correct result: **HIGH conflict** (both proof, contradictory architectural
intent on the same system boundary --- authentication).

The information needed to detect this conflict is *already in the payload*.
The conjecture claims share the concept "authentication" and the verbs are
contradictory ("adding" vs "removing"). The detection algorithm just does
not look at it.

---

## 4. How aq Should Detect Concept-Level Conflicts

### 4.1 Conjecture ID Matching (Already Available, Underused)

The cheapest concept-level signal is already in the wire format:
`conjecture_id`. Two agents with the **same conjecture_id** are, by
definition, working on the same hypothesis. This is always a conflict,
regardless of file overlap.

Current behavior: `conjecture_id` is displayed in `aq status` and included
in the `ConflictSignal` summary, but it is **never used as a conflict gate**.
The gate is `len(shared) == 0 → continue`. Two agents with the same
`conjecture_id` but different files produce no signal.

Proposed behavior: same `conjecture_id` = automatic conflict signal.

```go
// In checkConflicts(), after self-skip and done-skip:
if other.ConjectureID == me.ConjectureID && other.ConjectureID != "" {
    // Same conjecture = automatic conflict, severity by phase
    severity := "low"
    if me.Phase == "proof" && other.Phase == "proof" {
        severity = "high"
    } else if me.Phase == "proof" || other.Phase == "proof" {
        severity = "medium"
    }
    signals = append(signals, ConflictSignal{
        A:           me,
        B:           other,
        SharedFiles: shared, // may be empty --- that's fine
        Severity:    severity,
        Reason:      "same_conjecture",
    })
    continue // already counted, don't double-count for file overlap
}
```

This adds zero complexity. It uses a field that already exists. It has zero
false positives (same conjecture ID is always relevant). And it catches the
case where two agents are assigned the same conjecture but work on different
files.

**Cost**: One string comparison per broadcast pair.
**Benefit**: Eliminates an entire class of false negatives.
**Risk**: None. The field is already in the payload and already populated.

### 4.2 Claim Similarity (New Mechanism)

The `conjecture_claim` field is a human-readable string describing what the
agent is working on. Currently it is write-only: written into the broadcast,
displayed in status, never analyzed.

**Proposal**: Compute word-level similarity between claims. No ML, no
embeddings, no external dependencies. Bag-of-words Jaccard similarity on
lowercased, stop-word-filtered tokens.

```go
func claimSimilarity(a, b string) float64 {
    tokensA := tokenize(a) // lowercase, split on whitespace/punctuation, remove stop words
    tokensB := tokenize(b)
    if len(tokensA) == 0 || len(tokensB) == 0 {
        return 0.0
    }
    setA := toSet(tokensA)
    setB := toSet(tokensB)
    intersection := 0
    for tok := range setA {
        if _, ok := setB[tok]; ok {
            intersection++
        }
    }
    union := len(setA) + len(setB) - intersection
    if union == 0 {
        return 0.0
    }
    return float64(intersection) / float64(union)
}
```

Stop words for development claims (a small, hardcoded list):

```go
var stopWords = map[string]bool{
    "the": true, "a": true, "an": true, "is": true, "are": true,
    "in": true, "for": true, "to": true, "of": true, "and": true,
    "with": true, "that": true, "this": true, "it": true, "be": true,
}
```

Examples:

| Claim A | Claim B | Similarity | Interpretation |
|---------|---------|------------|----------------|
| "Adding OAuth support for authentication" | "Implementing OAuth2 authentication flow" | ~0.40 (shared: oauth, authentication) | HIGH concept overlap |
| "Adding OAuth support for authentication" | "Refactoring database connection pooling" | ~0.00 (no shared tokens) | No concept overlap |
| "Remove authentication middleware" | "Adding OAuth2 authentication flow" | ~0.17 (shared: authentication) | MEDIUM concept overlap --- same domain, possibly contradictory |
| "Filesystem transport is sufficient" | "Adding Redis transport support" | ~0.25 (shared: transport) | MEDIUM concept overlap --- same subsystem |

**Threshold proposal**: Jaccard similarity > 0.20 = concept overlap worth
reporting. This is conservative --- it requires at least one non-trivial
shared term.

**Integration into conflict score**:

```go
if similarity := claimSimilarity(me.ConjectureClaim, other.ConjectureClaim); similarity > 0.20 {
    // Concept overlap detected even without file overlap
    severity := "low"
    if me.Phase == "proof" && other.Phase == "proof" {
        severity = "medium" // elevated because of concept match, but no file overlap
    }
    signals = append(signals, ConflictSignal{
        A:           me,
        B:           other,
        SharedFiles: shared,
        Severity:    severity,
        Reason:      fmt.Sprintf("claim_similarity=%.2f", similarity),
    })
}
```

**Cost**: O(n * m) where n and m are token counts per claim. Claims are
typically 5-15 words. This is negligible.
**Benefit**: Catches concept collisions across different files.
**Risk**: False positives from common domain terms. Mitigated by the
conservative threshold and LOW default severity.
**Constraint**: No external dependencies. The tokenizer is 20 lines of
string splitting. This respects the zero-dependency axiom.

### 4.3 Tag/Topic Matching (New Field Proposal)

Claims are free-form text. Tags are structured. Adding an optional `tags`
field to the broadcast payload would enable cheaper, more precise concept
matching.

**Wire format addition**:

```json
{
  "agent": "github.com/jwalsh/aq/feature-oauth",
  "conjecture_id": "C-12",
  "conjecture_claim": "Adding OAuth2 support",
  "phase": "proof",
  "tags": ["auth", "security", "api"],
  "files": ["oauth.py", "config.py"],
  "ts": 1741824000.0,
  "ttl": 3600,
  "id": "01jd5abc12defg3456hi"
}
```

**Detection**:

```go
func tagOverlap(a, b []string) []string {
    setA := make(map[string]struct{}, len(a))
    for _, t := range a {
        setA[strings.ToLower(t)] = struct{}{}
    }
    var shared []string
    for _, t := range b {
        if _, ok := setA[strings.ToLower(t)]; ok {
            shared = append(shared, t)
        }
    }
    return shared
}
```

Two agents with overlapping tags + both in proof phase = concept conflict,
regardless of file overlap.

**Advantages over claim similarity**:
- Deterministic (no threshold tuning)
- Faster (set intersection on short lists)
- Agent-controlled (the agent decides which concepts it is touching)

**Disadvantages**:
- Requires agents to populate the field (they forget to announce; they will
  forget tags too)
- No standard vocabulary (is it "auth" or "authentication" or "authn"?)
- New field in wire format (requires protocol version bump per L7 review)

**Recommendation**: Add `tags` as an optional field. `json.Unmarshal` in Go
ignores unknown fields by default, so v1 readers will not break on v2
broadcasts that include tags. This is the forward-compatible approach noted
in the L7 review.

### 4.4 Dependency Graph Awareness (Ambitious, Deferred)

If agent A modifies package `auth` and agent B imports from `auth`, that is
a concept conflict --- A's changes may break B's assumptions.

Detection requires understanding the import/dependency graph:

1. Parse imports from modified files
2. Build a reverse-dependency map
3. If agent A's files are imported by agent B's files, flag a dependency
   conflict

This is powerful but violates the filesystem-first constraint. The import
graph is language-specific. Parsing Go imports is different from parsing
Python imports is different from parsing Rust use statements. This adds
complexity that exceeds aq's scope.

**Recommendation**: Defer to C-8 (function-level granularity). If C-8 is
implemented via treesitter or AST parsing, dependency graph awareness
becomes a natural extension. Do not build it independently.

---

## 5. Prior Art in Concept-Level Collaboration

### 5.1 Google's Rosie (Large-Scale Changes)

Google's internal tool for large-scale code changes across the monorepo
(google3) detects semantic conflicts by tracking *change chains* --- a
sequence of edits that must be applied together to maintain consistency.
If two change chains modify overlapping API surfaces, Rosie flags the
conflict before either chain is submitted.

Rosie's insight: the unit of change is not the file or the commit but
the *intent chain* --- a set of coordinated edits that implement a single
semantic change. aq's conjecture maps to Rosie's intent chain: a
conjecture is the reason for a set of changes, just as a change chain
is the container for a set of edits.

What aq can learn: Rosie's conflict detection is *scope-based*, not
*file-based*. Two change chains that modify the same API are in conflict
regardless of whether they touch the same files. The API surface is the
conflict domain, not the file list.

### 5.2 Semantic Merge (PlasticSCM/Codice Software)

SemanticMerge (Codice Software, later acquired by Unity as Plastic SCM,
now part of Unity Version Control) was the most ambitious attempt to
move merge conflict detection from the text level to the AST level.
Instead of comparing lines, it compared syntax trees. Renaming a function
and moving it to a different file? SemanticMerge understood that as a
rename+move, not a delete+add.

What SemanticMerge proved: AST-level merge is *possible* and *useful*
for reducing false positives. What it also proved: the engineering cost
is enormous. Language-specific parsers, IDE integration, server-side
processing --- the tool was complex enough that it eventually became a
feature of a commercial VCS rather than a standalone tool.

**Lesson for aq**: aq should not attempt AST-level merge (anti-goal: not
an orchestrator, not a merge tool). But aq can borrow the *insight* that
semantic units (functions, types, interfaces) are better conflict markers
than files. The C-8 conjecture proposes exactly this: `file:function` in
the broadcast instead of just `file`. This is the lightweight version of
SemanticMerge's insight.

### 5.3 Architectural Decision Records (ADRs)

Michael Nygard's ADR format (2011) established the practice of recording
architectural decisions as first-class artifacts:

```
# ADR 0012: Use OAuth2 for API Authentication

Status: Accepted
Date: 2026-03-14

## Context
We need to authenticate API clients...

## Decision
We will use OAuth2 with PKCE...

## Consequences
- auth.py will be added
- All API endpoints will require bearer tokens
```

ADRs are intent declarations. They say *what* will change and *why*.
If two ADRs contradict each other (one says "use OAuth2" and another says
"use API keys"), the contradiction is visible before any code is written.

aq's `conjecture_claim` is a lightweight ADR. The claim "Filesystem-first
transport is sufficient" is an architectural decision. If another agent
broadcasts "Redis transport is required," those two claims are in tension.
The current system does not detect this tension because it only looks at
file overlap.

**What aq can learn from ADRs**: ADRs have a `Status` field (proposed,
accepted, deprecated, superseded). aq's broadcasts have a `status` field
(prosecuting, done, blocked). Adding `superseded_by` or
`contradicts` fields would make claim-level conflicts explicit, but this
moves toward coordination (agents would need to reference each other's
broadcasts). The gossip axiom says no. Instead, aq should detect
contradiction *passively* via claim similarity, not *actively* via
cross-references.

### 5.4 Lakatos and Competing Research Programs

Imre Lakatos's *Proofs and Refutations* (1976) --- already the philosophical
foundation of CPRR --- provides the clearest model for concept-level
conflict. In Lakatos's framework:

- A **conjecture** is a claim about the world.
- A **proof** is an attempt to establish the claim.
- A **refutation** is a counterexample that challenges the claim.
- **Refinement** adjusts the conjecture in light of refutation.

Two agents pursuing contradictory conjectures are, in Lakatos's terms,
pursuing **competing research programs**. This is not a failure --- it is
how science works. But it IS information that should be surfaced.

The key insight from Lakatos: **conflict at the conjecture level is more
important than conflict at the data level.** Two experiments using the
same equipment (file overlap) is a scheduling conflict. Two experiments
testing contradictory hypotheses (concept conflict) is a scientific
conflict. aq currently detects the scheduling conflict and misses the
scientific one.

CPRR phase already modulates severity: both in proof = HIGH. But this only
triggers when there is file overlap. The Lakatos model says: two agents
both in proof phase for *contradictory conjectures* should be HIGH severity
even without file overlap. The contradictory conjectures are the conflict,
not the shared files.

### 5.5 Wardley Mapping and Strategic Conflicts

Simon Wardley's mapping framework (2005-present) positions capabilities on
an evolution axis (genesis, custom-built, product, commodity). A strategic
conflict occurs when one team treats a capability as custom-built ("we need
to build our own auth") while another treats it as commodity ("just use
Auth0").

This is relevant to aq because architectural conflicts often manifest as
build-vs-buy disagreements. Two agents can touch zero shared files but be
in fundamental strategic conflict. Wardley mapping suggests that conflict
detection should include a *positioning* dimension: not just *what* are you
building, but *where* on the evolution axis do you place it?

**Recommendation**: Not actionable for aq today. But the `tags` field
could carry positioning hints: `["auth", "custom-build"]` versus
`["auth", "commodity"]` would enable strategic conflict detection.

### 5.6 Facebook/Meta's Sapling and Stacked Diffs

Sapling (Meta, 2022) implements stacked diffs --- a workflow where changes
are organized as a stack of dependent commits. Each commit in the stack
has a clear dependency on the one below it. Conflict detection in Sapling
is stack-aware: if commit 3 conflicts with an external change, commits 4-N
are automatically flagged as potentially affected.

What this teaches aq: **dependency between changes matters more than
co-occurrence**. Two agents editing unrelated files that happen to be in
the same directory is not a conflict. Two agents editing files where one
depends on the other (imports, schema references, configuration) IS a
conflict, even if the file names do not overlap.

---

## 6. The VSS Anti-Pattern --- What aq Must Avoid

### 6.1 What Makes File Locking Bad

| Property | VSS (Bad) | Why It Fails |
|----------|-----------|-------------|
| **Pessimistic** | Assumes conflict, blocks work | Most concurrent edits are to different parts of the file. Blocking is overkill. |
| **File-granular** | The file is the lock unit | Two developers editing different functions in the same file are not in conflict. The file is too coarse. |
| **Synchronous** | Requires real-time coordination | "Can you unlock that file?" requires the other person to be present and responsive. |
| **Centralized** | Single server holds all locks | Server goes down, all locks are lost (or worse, all locks persist). |
| **Binary** | Locked or unlocked, no middle ground | "I'm editing the header comment" and "I'm rewriting the API" are treated identically. |

### 6.2 What aq Must Be Instead

| Property | aq (Good) | Why It Works |
|----------|-----------|-------------|
| **Optimistic** | Assumes no conflict, broadcasts presence | Work is never blocked. Awareness is informational. |
| **Concept-granular** (proposed) | Conjecture + claim + phase + tags is the unit | "Adding OAuth" and "adding logging" in the same file are distinguished by conjecture, not lumped by filename. |
| **Asynchronous** | Broadcasts carry no obligation | Nobody needs to respond. Broadcasts expire via TTL. Silence is normal. |
| **Distributed** | Filesystem-first, no central authority | Each agent writes to a shared directory. No server, no broker, no coordinator. |
| **Graduated** | LOW / MEDIUM / HIGH severity, not locked/unlocked | Phase modulation (C-4) provides nuance. Conjecture-phase + file-overlap + claim-similarity provides a score, not a boolean. |

### 6.3 The Specific VSS Pattern aq Must Not Repeat

aq's current `checkConflicts()` has a structural similarity to VSS that
should be addressed:

```
VSS:  if file_locked_by_other(file) → BLOCK
aq:   if file_in_other_broadcast(file) → WARN
```

The enforcement is different (block vs warn). The granularity is the same
(the file). aq has improved on VSS's *response* (warn instead of block)
but not on VSS's *analysis* (file is the unit).

The fix is not to abandon file-level detection --- it is to make file overlap
one input among several:

```
VSS:  conflict = file_overlap (binary)
aq:   conflict = f(file_overlap, conjecture_match, claim_similarity, tag_overlap, phase)
```

---

## 7. Concrete Proposal: Multi-Signal Conflict Scoring

### 7.1 The Scoring Model

Replace the current single-gate model (`file_overlap > 0 → compute
severity`) with a multi-signal model where any sufficiently strong signal
can trigger a conflict, and signals compound:

```
conflict_score =
    file_overlap_score        (current, keep as one input)
  + conjecture_match_score    (same C-ID = automatic)
  + claim_similarity_score    (word overlap in claims)
  + tag_overlap_score         (shared tags, if present)
  + phase_severity_modifier   (both proof = amplifier)
```

### 7.2 Signal Definitions

**file_overlap_score**:
- 0 shared files: 0.0
- 1+ shared files: 0.4
- (Unchanged from current behavior, but now contributes to a score instead
  of being the only gate.)

**conjecture_match_score**:
- Same non-empty `conjecture_id`: 0.8
- Different `conjecture_id`: 0.0
- (Highest weight single signal. Same conjecture = you are working on the
  same thing.)

**claim_similarity_score**:
- Jaccard similarity * 0.5
- Example: 0.40 Jaccard = 0.20 contribution to score
- (Weighted below conjecture match because claims are fuzzy.)

**tag_overlap_score**:
- 0 shared tags: 0.0
- 1+ shared tags: 0.1 per shared tag, max 0.3
- (Low weight per tag because tags are broad categories.)

**phase_severity_modifier** (multiplier, not additive):
- Both proof: 1.5x
- One proof: 1.0x (no modification)
- Neither proof: 0.5x (reduce severity --- both planning is low risk)

### 7.3 Severity Thresholds

| Score | Severity | Meaning |
|-------|----------|---------|
| >= 0.6 | HIGH | Active conflict. Both agents should be aware. |
| >= 0.3 | MEDIUM | Potential conflict. Worth noting. |
| >= 0.1 | LOW | Informational. Same general area. |
| < 0.1 | NONE | No conflict detected. |

### 7.4 Worked Examples

**Example 1: Current true positive (file overlap + both proof)**

- Agent A: C-1, phase=proof, files=[auth.py]
- Agent B: C-7, phase=proof, files=[auth.py]
- file_overlap: 0.4 (1 shared file)
- conjecture_match: 0.0 (different C-IDs)
- claim_similarity: varies (probably low --- C-1 and C-7 are different topics)
- tag_overlap: 0.0 (no tags)
- phase_modifier: 1.5x (both proof)
- Score: 0.4 * 1.5 = **0.60 = HIGH**
- Result: Same as current behavior. No regression.

**Example 2: Current false negative (concept conflict, no file overlap)**

- Agent A: C-12, claim="Adding OAuth2 support for authentication", phase=proof, files=[oauth.py, config.py]
- Agent B: C-13, claim="Removing custom auth in favor of API gateway", phase=proof, files=[gateway.py, deploy.yaml]
- file_overlap: 0.0 (no shared files)
- conjecture_match: 0.0 (different C-IDs)
- claim_similarity: ~0.17 Jaccard ("authentication"/"auth" overlap) -> 0.085
- tag_overlap: 0.0 (no tags)
- phase_modifier: 1.5x (both proof)
- Score: 0.085 * 1.5 = **0.13 = LOW**
- Result: Detected as LOW, which is better than the current NONE. If tags
  were ["auth", "security"], tag_overlap would add 0.2, giving (0.085 + 0.2) * 1.5 = **0.43 = MEDIUM**.

**Example 3: Same conjecture, different files**

- Agent A: C-1, phase=proof, files=[protocol.py]
- Agent B: C-1, phase=proof, files=[cli.py]
- file_overlap: 0.0
- conjecture_match: 0.8 (same C-1)
- claim_similarity: 1.0 (identical claims) -> 0.5
- Score without phase: 0.8 + 0.5 = 1.3
- phase_modifier: 1.5x
- Score: min(1.3, 1.0) * 1.5 = **1.0+ = HIGH** (capped)
- Result: Correctly flagged as HIGH. Current system: NONE (no file overlap).

**Example 4: Current false positive (same file, orthogonal work)**

- Agent A: C-1, claim="Filesystem transport benchmarks", phase=proof, files=[main.go], tags=["transport", "perf"]
- Agent B: C-4, claim="Phase severity modulation", phase=proof, files=[main.go], tags=["conflict", "severity"]
- file_overlap: 0.4
- conjecture_match: 0.0
- claim_similarity: ~0.0 (no shared terms)
- tag_overlap: 0.0 (no shared tags)
- phase_modifier: 1.5x
- Score: 0.4 * 1.5 = **0.60 = HIGH**
- Result: Still HIGH. File overlap dominates. This is the single-file repo
  problem that C-8 (function-level granularity) must solve. Multi-signal
  scoring cannot fix it because the file overlap signal is real --- they ARE
  touching the same file. The distinction (different functions within the
  file) requires finer-grained file analysis.

**Example 5: Cross-repo false positive (current bug)**

- Agent A: aq repo, files=[main.go]
- Agent B: cprr repo, files=[main.go]
- With namespace-qualified paths: files=["github.com/jwalsh/aq:main.go"] vs ["github.com/jwalsh/cprr:main.go"]
- file_overlap: 0.0 (different qualified paths)
- Result: No conflict. Current system: HIGH (basename collision).

### 7.5 Impact on Existing Conjectures

**C-2 (Conjecture identity prevents semantic conflicts)**: Multi-signal
scoring directly tests this. If conjecture_match_score eliminates false
negatives that file-only detection misses, C-2 is strengthened. Measurement:
track the rate of conflicts detected by conjecture match vs file overlap.

**C-4 (CPRR phase modulates conflict severity)**: Phase becomes a multiplier
rather than the primary severity determinant. This is a refinement, not a
refutation. Measurement: compare alert distribution (LOW/MEDIUM/HIGH) between
the current two-input model (file + phase) and the five-input model.

**C-8 (Function-level granularity)**: Multi-signal scoring does NOT replace
C-8. Example 4 shows that file overlap in single-file repos still produces
false positives even with claim similarity = 0. C-8 addresses the
file_overlap_score input specifically; multi-signal scoring addresses the
other inputs. They are complementary.

**C-9 (proposed): Concept-level conflict detection reduces false negatives
without increasing false positives.** Refutation criterion: concept-level
signals (claim similarity, tag overlap) produce more false positives than
they eliminate false negatives. Measurement: in a controlled dogfooding
session, compare the alert sets of file-only vs multi-signal detection.
Count true positives, false positives, and false negatives for each.

### 7.6 Implementation Priorities

| Priority | Change | Complexity | Benefit |
|----------|--------|-----------|---------|
| **P0** | Conjecture ID matching (same C-ID = conflict) | Trivial (1 string comparison) | Eliminates a class of false negatives with zero risk |
| **P1** | Claim similarity (Jaccard on word tokens) | Small (30 lines, no dependencies) | Catches concept overlap across different files |
| **P1** | Add `Reason` field to `ConflictSignal` | Trivial | Explains *why* a conflict was detected, not just *that* |
| **P2** | `tags` field in broadcast payload | Small (optional field, backward compatible) | Structured concept matching without NLP |
| **P2** | Namespace-qualified file paths | Medium (changes to detectSandbox, announce, check) | Eliminates cross-repo false positives |
| **P3** | Composite scoring model | Medium (replace boolean gate with float score) | Unifies all signals into a single framework |

### 7.7 What NOT to Build

Respecting the foundational axiom (gossip, not coordination) and the
anti-goals:

- **No NLP models or embeddings**: Jaccard similarity on word tokens is
  sufficient. Adding word2vec, BERT, or any ML model violates the
  zero-dependency constraint and the filesystem-first architecture.
- **No active negotiation**: Agents should not negotiate conflict resolution.
  aq detects and reports. Resolution is the agent's (or human's) problem.
- **No concept ontology**: Do not build a taxonomy of development concepts
  ("auth" is-a "security" is-a "infrastructure"). The tag vocabulary is
  agent-defined, not system-defined. Ontologies create coupling.
- **No cross-reference between broadcasts**: Agent A should not need to know
  agent B's broadcast ID to declare a contradiction. Detection must be
  passive (computed by the reader from the broadcast contents), not active
  (declared by the broadcaster).
- **No centralized concept registry**: Tags and claims are in the broadcast.
  There is no "tag server" or "concept database." The filesystem is the
  only storage. This is the UDDI lesson from WS-* (DOGFOODING.md section 8):
  centralized registries go stale.

---

## 8. The Reason Field --- Explaining Why, Not Just That

### 8.1 The Current Problem

`ConflictSignal.Summary()` outputs:

```
[HIGH] github.com/jwalsh/aq/feature-auth (C-1) <-> github.com/jwalsh/aq/feature-db (C-7) -- shared: auth.py
```

This tells you *that* there is a conflict and *which files* overlap. It does
not tell you *why* the system considers it a conflict. With multi-signal
scoring, the "why" becomes important: was it file overlap? Same conjecture?
Claim similarity? Tag overlap?

### 8.2 Proposed Addition

Add a `Reason` field to `ConflictSignal`:

```go
type ConflictSignal struct {
    A           Broadcast `json:"a"`
    B           Broadcast `json:"b"`
    SharedFiles []string  `json:"shared_files"`
    Severity    string    `json:"severity"`
    Reason      string    `json:"reason"` // "file_overlap", "same_conjecture", "claim_similarity=0.35", "tag_overlap=[auth]"
    Score       float64   `json:"score"`  // composite score before thresholding
}
```

This is a wire format addition (new fields). Per the L7 review's guidance on
forward compatibility, existing readers will ignore unknown fields. No
breaking change.

The `Reason` field makes the conflict signal *actionable*. "You conflict
because you share files" suggests splitting work. "You conflict because your
claims overlap" suggests discussing intent. "You conflict because you have
the same conjecture" suggests one of you should switch.

---

## 9. Relationship to the Gossip Axiom

### 9.1 Does Concept-Level Detection Violate "Gossip, Not Coordination"?

No. Concept-level detection is still gossip. The mechanism:

1. Agent A broadcasts its conjecture, claim, phase, tags, and files.
2. Agent B broadcasts its conjecture, claim, phase, tags, and files.
3. A reader (could be A, B, or a watcher) computes similarity between the
   two broadcasts and reports a conflict signal.

No agent is required to *do* anything with the conflict signal. The signal
carries no obligation. It expires via TTL. Silence is normal. This is
gossip --- richer gossip, but gossip.

The line aq must not cross: **the conflict signal must never block work.**
`aq check` returns an exit code (0 = no conflicts, 1 = conflicts detected)
but this is informational. The agent decides what to do. If it ignores the
signal, that is valid. Gossip without obligation.

### 9.2 Does Claim Similarity Add Too Much Complexity?

The Jaccard similarity function is 15 lines of Go with no external
dependencies. The stop word list is a hardcoded map of 15 words. The
tokenizer is `strings.Fields()` + `strings.ToLower()`. This is less code
than the existing ULID generator.

The total addition to `checkConflicts()` is approximately 40 lines:
conjecture ID check (5 lines), claim similarity (15 lines), integration
into scoring (20 lines). The function is currently 57 lines. This is a
~70% increase in the conflict detection function, which is modest for a
significant improvement in detection quality.

### 9.3 Does the Tags Field Violate Zero Dependencies?

No. Tags are an optional JSON array. They are serialized with
`encoding/json` (stdlib). They are compared with map lookup (stdlib).
No external packages, no network calls, no databases. The tags live in
the broadcast payload, which lives in a JSON file on the filesystem.
Filesystem-first constraint satisfied.

---

## 10. Summary

### The Historical Argument

File-level conflict detection was the state of the art in 1994 (VSS). The
industry spent 30 years moving beyond it: CVS introduced optimistic merging,
SVN refined it, Git eliminated locking entirely, and Git LFS brought locking
back only for unmergeable binaries. The trajectory is from file-level to
hunk-level to intent-level. aq is currently at file-level (1994) while its
payload already carries intent-level data (2026). The payload is ahead of the
algorithm.

### The Empirical Argument

The dogfooding data is conclusive:
- DOGFOODING.md section 2: 100% false positive rate in single-file repos
- DOGFOODING.md section 10: file overlap detected the *existence* of a
  conflict but not its *nature* (wire format change vs strict validation)
- DOGFOODING.md section 11: cross-repo false positives on basename collision
- The intentional collision test showed that `aq check` correctly flagged
  HIGH but could not explain *why* the conflict mattered

### The Proposal

Make file overlap **one signal among five** instead of **the only gate**:

1. **Conjecture ID matching** --- same C-ID = automatic conflict (P0, trivial)
2. **Claim similarity** --- word overlap in claims (P1, 30 lines)
3. **Tag overlap** --- shared tags (P2, optional field)
4. **Namespace-qualified paths** --- eliminate cross-repo false positives (P2)
5. **Composite scoring** --- weighted sum of all signals (P3)

Phase severity remains as a multiplier across all signals, consistent with
C-4.

### What Does Not Change

- Gossip axiom: broadcasts carry no obligation
- Filesystem-first: no new dependencies, no servers, no databases
- TTL expiry: all signals are computed from active broadcasts
- Wire format: new fields are additive and backward compatible
- Exit conditions: `status=done` or TTL expiry clears conflicts

### The New Conjecture

**C-9**: Concept-level conflict detection (conjecture match + claim
similarity + tag overlap) reduces false negatives without proportionally
increasing false positives.

Refutation criterion: In a controlled multi-agent session, concept-level
signals produce more false positive alerts than the false negatives they
eliminate.

Measurement: Compare alert sets between file-only and multi-signal detection
across 10+ agent sessions. Track true positive, false positive, and false
negative counts. If false_positives_added > false_negatives_eliminated,
C-9 is refuted.

---

## References

1. Thompson, K. and Ritchie, D. (1973). "The UNIX Time-Sharing System."
   *Bell System Technical Journal*.

2. Tichy, W. (1985). "RCS --- A System for Version Control."
   *Software: Practice and Experience*, 15(7).

3. Rochkind, M. (1975). "The Source Code Control System."
   *IEEE Transactions on Software Engineering*, SE-1(4).

4. Microsoft (2005). "Migrating from Visual SourceSafe to Team Foundation
   Server." MSDN Library.

5. Torvalds, L. (2005). "Git --- A Stupid Content Tracker."
   Linux kernel mailing list.

6. GitHub (2017). "Git LFS File Locking."
   https://github.blog/2017-01-05-git-lfs-2-0-0-released/

7. Pike, R. et al. (1995). "Plan 9 from Bell Labs."
   *Computing Systems*, 8(3).

8. Lakatos, I. (1976). *Proofs and Refutations: The Logic of Mathematical
   Discovery*. Cambridge University Press.

9. Nygard, M. (2011). "Documenting Architecture Decisions."
   https://cognitivity.com/blog/2011/11/15/documenting-architecture-decisions/

10. Codice Software (2013). "SemanticMerge: Language-Aware Merge Tool."
    https://semanticmerge.com/

11. arXiv 2508.01531 (2025). "Revisiting Gossip Protocols: A Vision for
    Emergent Coordination in Agentic Multi-Agent Systems."

12. arXiv 2512.03285 (2025). "Gossip-Enhanced Agentic Coordination Layer
    (GEACL)."

13. Wardley, S. (2016). "Wardley Maps." https://learnwardleymapping.com/

14. Meta (2022). "Sapling: A Scalable, User-Friendly Source Control System."
    https://engineering.fb.com/2022/11/15/developer-tools/sapling-source-control-scalable/

15. aq DOGFOODING.md (2026-03-13). Internal project document. Sections 2,
    10, 11.

16. aq L7 Engineer Review (2026-03-13). Internal project document.
    "CPRR-as-gossip-payload is genuinely novel" assessment.
