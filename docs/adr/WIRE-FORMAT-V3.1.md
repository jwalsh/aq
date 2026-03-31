# ADR: Wire Format v3.1 — Mnemonic Trie, Not Hash Trie

**Status:** Accepted
**Date:** 2026-03-30
**Conjectures:** C-1, C-3

## Context

aq broadcasts carry agent identity, conjecture, phase, status, files,
and a claim across transports with wildly different payload budgets:

| Transport      | Budget | Constraint                    |
|----------------|--------|-------------------------------|
| Filesystem     | ∞      | None                          |
| UDP multicast  | 1400B  | MTU                           |
| Meshtastic     | 237B   | LoRa frame                    |
| Audible ggwave | 140B   | FSK encoding overhead         |
| Ultrasonic     | 25B    | High-frequency FSK, ~4 B/s   |

The v1 AMTP compact format (`aq:jwalsh/aq/main C-1 [p] main.go`) is
68 bytes for a typical broadcast. This wastes 14 seconds on audible
and doesn't fit ultrasonic at all.

## Decision Drivers

Five agents argued in parallel. Key findings:

- **Lambda (formal):** CRC16 hashes over 616 repos give P(collision) ≈ 1.0
  by birthday bound. 16-bit hashes over a known vocabulary are broken.
- **Sec-research:** Trie indices require a shared codebook. A codebook is
  coordination. Coordination violates the gossip axiom.
- **Frontend:** `jw` beats `d253` at 2am. Mnemonic prefixes are
  tab-completable and self-describing.
- **Performance:** v4 binary (9B) hits ultrasonic; mnemonic v3.1 (35B)
  hits audible in under 4s. Different formats per tier.
- **L6 (integration):** Format translation at the transport boundary.
  Filesystem stays full JSON. Constrained transports use v3.1.

## Scale Constraint

aq is gossip for a 2–5 person team working on code. At that scale:

- Everyone knows each other by initials (`jw`, `sg`, `dt`)
- The repo set is shared and small (`aq`, `bd`, `sb`)
- Listing file names is cheaper than hashing them
- Conjecture numbers are single digits

Beyond ~10 agents, the problem is devops (dashboards, alerting, runbooks),
not gossip. aq does not grow into that. A different tool does.

## Decision

Adopt v3.1 mnemonic format for constrained transports:

```
aq1:jw:aq:1:pw:main.go:wiring audit
```

### Field Layout

| Field    | Example     | Bytes | Description                          |
|----------|-------------|-------|--------------------------------------|
| version  | `aq1`       | 3     | Protocol version, literal            |
| who      | `jw`        | 2-3   | Initials, tab-completable            |
| repo     | `aq`        | 2-10  | Short repo name, everyone knows it   |
| conj     | `1`         | 1-2   | Conjecture number                    |
| ph+st    | `pw`        | 2     | Phase + status (see below)           |
| files    | `main.go`   | 0-50  | Actual file names, comma-separated   |
| claim    | `wiring...` | 0-∞   | Plain text tail, truncated to fit    |

Phase: `c`onjecture, `p`roof, `r`efutation, refi`n`ement
Status: `w`orking, `d`one, `b`locked

### Separator

Colon. Visually scannable, no escaping needed for file paths (which
never contain colons on POSIX or Windows).

### Ultrasonic Mode

Drop the claim. 22 bytes:

```
aq1:jw:aq:1:pw:main.go
```

### Grammar (BNF)

```bnf
<broadcast> ::= "aq1" ":" <who> ":" <repo> ":" <conj> ":" <ps> ":" <files> ":" <claim>
<who>       ::= ALPHA ALPHA           ; initials, tab-completable
<repo>      ::= IDENT                 ; short repo name
<conj>      ::= DIGIT+               ; conjecture number
<ps>        ::= PHASE STATUS          ; 2 chars: phase + status
<phase>     ::= "c" | "p" | "r" | "n" ; conjecture/proof/refutation/refinement
<status>    ::= "w" | "d" | "b"       ; working/done/blocked
<files>     ::= PATH ("," PATH)* | "-"
<claim>     ::= TEXT                   ; everything after 6th colon (IRC trailing rule)
```

Parser: `split(":", 7)`. Last field absorbs colons in the claim.
This is the IRC RFC 1459 trailing parameter rule, battle-tested
for 33 years.

### Multiple Contracts, Lossy Projection

The canonical broadcast is a full struct (JSON on filesystem). Each
fanout target receives a *projection* — lossy, downhill, and that's
fine. Gossip tolerates lossy. The filesystem is always the lossless
store. Received wire-format broadcasts get hydrated back to JSON on
ingest.

```
Broadcast (canonical JSON)
  │
  ├─ filesystem:  full JSON             (lossless, cat-debuggable)
  ├─ UDP/MQTT:    full JSON             (no constraint, jq-friendly)
  ├─ LoRa/mesh:   v3.1 wire format     (35B, fits 237B frame)
  ├─ audible:     v3.1 wire format     (35B, 4s at fastest)
  └─ ultrasonic:  v3.1 sans claim      (22B, fits 25B budget)
```

Information flows downhill through the fanout, never up. Translation
happens once in the Go binary: `Broadcast.ToWire()` /
`Broadcast.FromWire()`. One codec, one place.

| Transport    | Contract    | Budget | Projection loss            |
|--------------+-------------+--------+----------------------------|
| Filesystem   | Full JSON   | n/a    | None (canonical)           |
| UDP / MQTT   | Full JSON   | 1400B+ | None                       |
| Meshtastic   | v3.1 wire   | 237B   | Nested fields flattened    |
| Audible      | v3.1 wire   | 140B   | Nested fields flattened    |
| Ultrasonic   | v3.1 wire   | 25B    | Claim dropped, files maybe |

## Alternatives Rejected

### v1 AMTP Compact (68B)

`aq:jwalsh/aq/main C-1 [p] broadcast wiring audit|main.go,protocol.go`

Too verbose. 14s audible. Doesn't fit ultrasonic. The full GitHub path
is redundant when you're three people in a room.

### v4 Binary Trie (9B)

Bit-packed struct with org/repo/branch as trie indices. Maximum
compression but completely unreadable. Requires a shared codebook,
which is coordination. Base85 encoding (`QK@$c0RmC...`) is noise
in a terminal.

### CRC16 Hashes (v3, 36B)

`aq1:d253:1:pw:51eb,6439:wiring audit`

Hashes are opaque. `d253` requires a lookup table in your head.
Birthday bound on CRC16 at team-repo scale gives unacceptable
collision rates. The mnemonic version is the same size and readable.

## Consequences

- Wire format matches how you'd type it interactively
- No codebook sync, no protocol negotiation
- Format translation is each transport plugin's responsibility
- `Broadcast.ToAMTP()` needed in Go binary to canonicalize encoding
  (currently duplicated in bash and Python)
- At >10 agents, initials collide. That's not our problem.
