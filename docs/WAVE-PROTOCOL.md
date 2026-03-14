# Google Wave Federation Protocol: Reference for aq

> **Purpose**: This document reconstructs the Google Wave Federation Protocol
> specification from primary whitepapers, secondary sources, and archived
> materials. It serves as a lineage reference for `aq`, which draws on Wave's
> presence-as-stream semantics while deliberately rejecting its complexity.
>
> **Source quality**: Sections are annotated with source provenance.
> "Primary" means official Google/Apache whitepapers. "Secondary" means
> Wikipedia, news coverage, or retrospective analysis.

---

## Table of Contents

1. [Timeline](#1-timeline)
2. [Core Protocol Architecture](#2-core-protocol-architecture)
3. [The Presence and Awareness Model](#3-the-presence-and-awareness-model)
4. [Wire Format](#4-wire-format)
5. [Federation Protocol](#5-federation-protocol)
6. [Operational Transformation](#6-operational-transformation)
7. [The Conversation Model](#7-the-conversation-model)
8. [What Killed It](#8-what-killed-it)
9. [The Apache Wave Continuation](#9-the-apache-wave-continuation)
10. [What aq Takes from Wave](#10-what-aq-takes-from-wave)
11. [Sources](#11-sources)

---

## 1. Timeline

*Source: secondary (Wikipedia, news coverage, Apache records)*

| Date | Event |
|------|-------|
| May 28, 2009 | Lars and Jens Rasmussen demo Wave at Google I/O keynote |
| Jul 2009 | Federation Prototype Server source released (Apache 2.0, Mercurial) |
| Sep 30, 2009 | Limited public preview (invite-only, ~100k users) |
| Oct 2009 | Conversation Model and OT whitepapers published |
| May 19, 2010 | Wave opened to general public |
| Aug 4, 2010 | Google announces Wave will be discontinued |
| Apr 30, 2012 | Wave service shut down; read-only access expires |
| Dec 2010 | Apache Software Foundation accepts Wave into Incubator |
| Oct 10, 2014 | Last release candidate: 0.4-rc10 |
| Jan 15, 2018 | Apache Wave retired; never left incubator status |

Wave was public for approximately three months before Google announced its
cancellation. The Apache incubation lasted seven years without an official
release.

---

## 2. Core Protocol Architecture

*Source: primary (Google Wave Federation Architecture whitepaper, Apache SVN)*

### 2.1 Data Model Hierarchy

```
Wave
 └── Wavelet (unit of OT, access control, federation)
      ├── Participant List
      └── Document (XML-like structured content + annotations)
```

- **Wave**: A container identified by a globally unique ID (`domain$wave-id`).
  Contains one or more wavelets.
- **Wavelet**: The fundamental unit. Each wavelet has its own participant list,
  document collection, and is the domain where operational transformation
  applies. Different wavelets within the same wave can have different
  participants.
- **Participant**: Identified by email-format addresses (`user@domain`). Can be
  a user, group, or robot (automated participant).
- **Document**: XML-like structured content with "stand-off" annotations
  (formatting, links, metadata stored separately from document structure).
- **Wave View**: A user-specific subset of accessible wavelets within a wave.

### 2.2 Server Components

A **wave provider** operates a wave service, identified by an Internet domain
name. The architecture has four core server-side components:

1. **Wave Store**: Persists wavelet operations (the operation log, not
   snapshots).
2. **Wave Server**: Resolves concurrent operations via OT, validates
   operations, manages read/write to the wave store.
3. **Federation Gateway**: Outbound federation. Pushes local wavelet operations
   to remote providers, serves historical operation requests, processes
   incoming submission requests.
4. **Federation Proxy**: Inbound federation. Receives pushed operations from
   remote gateways, requests historical data, submits local user operations
   to remote hosting providers.

### 2.3 Local vs. Remote Wavelets

- **Local wavelets**: Created by users at the hosting provider. The host
  performs OT and is the authoritative source.
- **Remote wavelets**: Cached copies at downstream providers. Read access is
  local (no round-trip to host). Write operations are submitted upstream to
  the hosting provider.

This means every provider with a participant in a wavelet maintains its own
copy of the wavelet state, updated via the federation protocol.

---

## 3. The Presence and Awareness Model

*Source: primary (Conversation Model whitepaper), secondary (I/O demo coverage,
product retrospectives)*

This is the section most relevant to `aq`. Wave's presence model operated at
three levels:

### 3.1 Character-by-Character Live Typing

Wave's signature feature: when a user typed a message, every other participant
saw the characters appear in real-time, keystroke by keystroke. This was not
"user is typing..." indicators -- it was the actual content streaming live.

From Lars Rasmussen's I/O keynote: "When you type a message to a friend, he
or she sees what you're typing as you type it. You can jump in and start
drafting a reply before the initial message is complete."

This was implemented via the OT system: each keystroke generated a document
operation (insert character at position N), which was transformed, applied to
the server's copy, and broadcast to all connected clients. The granularity of
OT was per-character.

Users could disable live typing visibility, falling back to conventional
"send when done" behavior.

### 3.2 Cursor and Selection Tracking

The Conversation Model whitepaper defines session-based user annotations for
presence tracking within documents:

| Annotation Key | Purpose |
|----------------|---------|
| `user/d/<session_id>` | Document-level: user ID and cursor timestamp |
| `user/r/<session_id>` | Selection range: highlighted text region |
| `user/e/<session_id>` | Cursor position extending to document end |

These annotations enabled clients to display each user's cursor position and
text selection in real-time. The whitepaper notes that timestamps "may be used
by clients to stop displaying the user's caret after a period of inactivity"
-- an early form of TTL-based presence expiry.

Key design detail: contributors were "responsible for adding themselves" to the
contributor list. Participation was voluntary and self-declared. Robots could
choose to remain unlisted.

### 3.3 Participant Awareness at Wavelet Level

Each wavelet maintained an explicit participant list. Adding or removing a
participant was itself a wavelet operation, subject to OT. Every provider with
a participant received all operations, meaning presence was implicit in the
operation stream: if you're receiving operations from a user, that user is
active.

### 3.4 What Wave Got Right About Presence

Wave treated presence as a **side effect of the operation stream**, not as a
separate system. There was no dedicated "presence service" -- the fact that
operations were flowing *was* the presence signal. Cursor annotations were
just another document mutation. The entire system was unified: editing,
presence, and awareness were all wavelet operations.

This is the insight `aq` inherits: presence should be embedded in the data
stream, not bolted on as a separate service.

### 3.5 What Wave Got Wrong About Presence

Wave's presence was **too tightly coupled to OT**. You couldn't have presence
without the full operational transformation machinery. You couldn't broadcast
"I'm working on auth.py" without first creating a wave, joining a wavelet,
and having an OT-capable client. The presence semantics were excellent; the
implementation prerequisites were excessive.

---

## 4. Wire Format

*Source: primary (Federation Protocol Over XMPP whitepaper, Client-Server
Protocol whitepaper)*

Wave used two different wire formats for two different communication paths:

### 4.1 Federation (Server-to-Server): Protocol Buffers over XMPP

Server-to-server federation used XMPP as the transport layer with Protocol
Buffer payloads Base64-encoded inside XMPP stanzas.

Core protobuf message types:

```
ProtocolWaveletDelta
  ├── hashed_version    (version + history hash)
  ├── author            (participant address)
  └── operation[]       (list of ProtocolWaveletOperation)

ProtocolWaveletOperation
  ├── add_participant   (string)
  ├── remove_participant (string)
  ├── mutate_document   (ProtocolDocumentOperation)
  └── no_op

ProtocolDocumentOperation
  └── component[]       (ordered sequence of mutation components)
       ├── retain
       ├── characters          (insert text)
       ├── element_start       (insert opening tag)
       ├── element_end         (insert closing tag)
       ├── delete_characters
       ├── delete_element_start
       ├── delete_element_end
       ├── replace_attributes
       ├── update_attributes
       └── annotation_boundary

ProtocolSignedDelta
  ├── delta             (serialized ProtocolWaveletDelta)
  └── signature[]       (cryptographic signatures)

ProtocolSignerInfo
  ├── hash_algorithm
  ├── domain
  └── certificate[]     (X.509 certificate chain, PEM-encoded)
```

The protobuf definitions lived in `src/main/proto/` in the wave-protocol
repository and were compiled via `protoc` into Java sources.

### 4.2 Client-Server: JSON over WebSocket

Client-server communication used a simpler format: JSON serializations of
protocol buffers over WebSocket connections.

Key message types:

| Message | Direction | Purpose |
|---------|-----------|---------|
| `ProtocolOpenRequest` | Client -> Server | Start monitoring a wave |
| `ProtocolWaveletUpdate` | Server -> Client | Push state snapshot or deltas |
| `ProtocolSubmitRequest` | Client -> Server | Submit a mutation |
| `ProtocolSubmitResponse` | Server -> Client | Acknowledge mutation |

JSON serialization rules:
- Root level as JSON object
- Repeated fields as arrays
- Booleans as `1` (true) or `0` (false)
- Enums as JSON strings
- Bytes as hex-encoded strings
- One message per WebSocket frame

Constraint: only one outstanding `ProtocolSubmitRequest` per wavelet was
allowed. Clients had to wait for acknowledgment before sending the next
operation.

### 4.3 XMPP Stanza Structure

Federation messages were carried in two XMPP stanza types:

- **Message stanzas**: Used for wavelet updates. Contained `<wavelet-update>`
  elements with `<applied-delta>` (Base64 protocol buffer) and optional
  `<commit-notice>` elements.
- **IQ (PubSub) stanzas**: Used for everything else -- history requests,
  submit requests, signer/certificate exchange.

### 4.4 Wavelet Naming in URIs

Wavelets were addressed as URIs:

```
wave://host/[domain$]wave-id/wavelet-id
```

Example: `wave://initech-corp.com/acmewave.com$w+4Kl2/conv+3sG7` identifies
a wavelet hosted at `initech-corp.com` for a wave originated at
`acmewave.com`.

### 4.5 Version Tracking

Versions used rolling history hashes:

```
H(0) = SHA256(wavelet-name)[0..20]
H(n) = SHA256(H(n-1) + delta)[0..20]
```

This allowed any provider to verify operation history integrity without
storing the full operation log.

---

## 5. Federation Protocol

*Source: primary (Federation Architecture whitepaper, Federation Over XMPP
whitepaper)*

### 5.1 Discovery

Wave providers discovered each other using standard XMPP mechanisms:
- **DNS SRV records** for IP address and port resolution
- **TLS** for transport security (mandatory; non-identity cipher required)
- Standard XMPP server-to-server connection establishment

### 5.2 Push Model

Federation was push-based. The hosting provider's **federation gateway**
maintained a queue of outgoing operations for each remote domain with
participants. Operations were queued until receipt was acknowledged by the
remote provider's **federation proxy**.

On connection failure: exponential backoff retry. Operations accumulated in
the queue until the connection was re-established.

### 5.3 History Requests

Remote providers could request historical operations they missed. The
federation gateway served these from the wave store. This enabled recovery
after network partitions or provider restarts.

### 5.4 Submit Path (Remote User Editing)

When a user at `remote.com` edited a wavelet hosted at `host.com`:

1. Remote client submits operation to `remote.com`'s wave server
2. `remote.com`'s federation proxy forwards the submission to `host.com`'s
   federation gateway
3. `host.com`'s wave server applies OT, transforms the operation
4. `host.com`'s federation gateway pushes the transformed result back to
   `remote.com` and all other providers

### 5.5 Cryptographic Authentication

Each provider signed its users' operations with its own X.509 certificate.
The signature chain allowed downstream providers to verify:

- That the operation originated from the claimed provider
- That the hosting provider did not tamper with forwarded operations
- That no intermediate provider spoofed content

Signer information (certificate chains) was exchanged via dedicated
`signer-request` and `signer-post` XMPP stanzas.

---

## 6. Operational Transformation

*Source: primary (Google Wave Operational Transformation whitepaper)*

### 6.1 Core Algorithm

Wave's OT enabled lock-free, non-blocking concurrent editing:

1. Local edits apply immediately (optimistic UI)
2. Operations transmit to server
3. Server transforms concurrent operations against each other
4. Server applies the transformed operation to its authoritative copy
5. Server broadcasts the transformed operation to all other clients

### 6.2 The Server Acknowledgment Innovation

Wave modified classical OT with a critical constraint: **clients must wait for
server acknowledgment before sending the next operation.**

This meant:
- The server maintained only a single state space (the operation history),
  not per-client state spaces
- The server transformed incoming operations solely against this linear
  history
- Clients maintained an "inferred server path" by tracking acknowledgments

Trade-off: clients perceived updates from other clients in chunks
corresponding to approximately one round-trip time. The Wave team considered
this acceptable.

### 6.3 Composition

While waiting for acknowledgment, clients composed pending local operations
into a single operation. This reduced both transformation cost and
transmission overhead. Composition preserved the invariant:
`(B . A)(d) = B(A(d))` for all documents where A applies.

### 6.4 Document Operations as Streams

Operations were modeled as streaming cursors moving left-to-right through the
document. The transformer processed two streaming operations in parallel,
producing two transformed streams. This enabled efficient transformation of
large operations without buffering entire documents in memory.

Example operation sequence:
```
retain 3
insert element_start <p>
insert characters "Hi there!"
insert element_end
retain 5
delete characters 4
retain 2
```

### 6.5 Why OT Is Not What aq Needs

OT solves **concurrent document editing** -- maintaining consistency when
multiple users modify the same character-level content simultaneously. This
requires:

- A shared document model
- Character-level operation granularity
- Server-side transformation logic
- Strict operation ordering guarantees
- Acknowledgment round-trips

`aq` broadcasts **file-level presence** ("I am touching auth.py"), not
character-level mutations. The granularity difference is several orders of
magnitude. File overlap detection is a simple set intersection, not an OT
transformation. `aq` does not need OT, and adopting it would be a category
error.

---

## 7. The Conversation Model

*Source: primary (Google Wave Conversation Model whitepaper, Oct 2009)*

### 7.1 Structure

Waves organized conversation content into:

- **Blips**: Individual messages. The atomic unit of conversation. Each blip
  was a document with contributor metadata, body content, and optional
  embedded replies.
- **Threads**: Sequences of blips. All sibling blips were replies to their
  parent. Threads could be inline (anchored to specific text positions in a
  parent blip) or non-inline (standard reply threading).
- **Conversation Manifest**: A document (`conversation` namespace) maintaining
  the logical structure -- which blips exist, their thread relationships, and
  ordering.

### 7.2 Private Replies

Separate wavelets with restricted participants, anchored to parent
conversations through explicit references:
- `anchorWavelet`, `anchorBlip`, `anchorManifestOffset`, `anchorVersion`,
  `anchorOffset`

This allowed confidential sub-conversations within a wave.

### 7.3 Incomplete Specification

The Oct 2009 whitepaper explicitly marked several features as "undocumented":
cursors, selections, annotations, RTL text, images, form elements, blip
submission signaling, modification timestamps, and rich text formatting. The
specification was a work-in-progress when Wave was cancelled.

---

## 8. What Killed It

*Source: secondary (TechCrunch, eWeek, Taskade retrospective, Thought Shrapnel)*

Wave failed for reasons that are instructive for `aq`:

### 8.1 Unclear Value Proposition

Users did not know when to use Wave instead of email, chat, or Google Docs.
Wave combined email, instant messaging, wikis, document editing, and social
features into one interface. The response was not "this is amazing" but "what
do I use this for?" Gina Trapani published a 195-page tutorial. A product
that requires a 195-page tutorial has a positioning problem.

### 8.2 Complexity as a Feature

Wave treated complexity as a feature rather than a cost:

- Character-by-character live typing was technically impressive but
  psychologically uncomfortable (users saw typos, false starts, half-formed
  thoughts)
- OT gave perfect consistency but required a heavyweight client, a stateful
  server, and round-trip acknowledgments
- Federation required XMPP infrastructure, X.509 certificates, DNS SRV
  records, and a custom protobuf-over-XMPP wire format
- The data model (waves, wavelets, blips, threads, documents, annotations)
  introduced five new concepts where email had two (messages and threads)

Each piece was well-engineered. The assembly was overwhelming.

### 8.3 Anti-Network-Effect Launch

Google throttled invitations to individual users, not teams. A collaboration
tool without collaborators is an empty room. By the time Wave opened to the
public in May 2010, its reputation as confusing had calcified. Fewer than one
million people actively used it.

### 8.4 Timing

Wave arrived in 2009. Slack launched in 2013. Google Docs gained real-time
collaboration in 2010. The concepts Wave pioneered (real-time collaborative
editing, presence indicators, inline replies) succeeded -- just not in Wave.
Wave was early, not wrong.

### 8.5 The Coupling Problem

This is the deepest lesson for `aq`. Wave coupled three things that should
have been independent:

1. **Presence/awareness** (who is here, what are they doing)
2. **Content editing** (the actual document mutations)
3. **Consistency** (OT ensuring everyone sees the same state)

You could not get Wave's excellent presence semantics without also buying its
OT system, its XMPP federation, its protobuf wire format, and its five-layer
data model. The presence signal was trapped inside the editing machinery.

`aq` exists because Wave proved that **presence-as-stream** is valuable, and
also proved that **coupling presence to editing** is fatal.

---

## 9. The Apache Wave Continuation

*Source: secondary (Apache WAVE wiki, Thought Shrapnel, Encyclopedia MDPI)*

### 9.1 Apache Incubation (2010-2018)

After Google's cancellation, the Wave codebase was donated to the Apache
Software Foundation Incubator in December 2010. The proposal positioned it as
a reference implementation enabling developers to run their own wave servers.

The project never graduated from incubation:
- Never produced an official release (stuck at 0.4-rc10 since October 2014)
- Insufficient committer activity to sustain development
- The codebase was large, complex Java requiring significant expertise to
  modify

Apache Wave was retired on January 15, 2018.

### 9.2 SwellRT (2014-present)

SwellRT re-engineered Wave's core into a backend-as-a-service for real-time
collaboration. Key evolution:

- **2016**: Replaced XMPP-based federation with the Matrix.org protocol
- **2017**: Implemented end-to-end encryption for OT collaborative documents
- Positioned as an API/SDK rather than a user-facing product
- Funded through Berkman Klein Center / Google Summer of Code

SwellRT's decision to replace XMPP with Matrix is notable: it validated that
Wave's transport choice (XMPP) was a weak point, while Wave's OT core had
lasting value.

### 9.3 Surviving Code

- **wave-protocol/wave** on GitHub: The reference implementation
  (Java, Gradle build, Apache 2.0 license)
- **Apache SVN whitepapers**: The protocol specifications survive at
  `svn.apache.org/repos/asf/incubator/wave/whitepapers/`
- **Google Code Archive**: `code.google.com/archive/p/wave-protocol`
  (read-only archive of the original Mercurial repository)

### 9.4 Wave's Surviving Legacy in Other Products

Wave's OT implementation directly influenced Google Docs' real-time
collaboration. The concepts Wave pioneered are now standard:

- Real-time collaborative editing (Google Docs, Notion, Figma)
- Presence indicators (Google Docs cursors, Figma avatars)
- Inline replies (Slack threads, Google Docs comments)
- Live typing indicators (Slack, Discord, iMessage)

Wave failed as a product. Its ideas won.

---

## 10. What aq Takes from Wave

### 10.1 The Inheritance

`aq` inherits Wave's insight that **presence is a stream, not a request**.
In Wave, you did not ask "who is editing this document?" -- you simply
received operations from everyone who was. Presence was ambient, continuous,
and zero-cost to consume.

`aq` applies this to multi-agent development: agents broadcast what they are
working on, and peers detect conflicts by observing the stream. No polling, no
requests, no coordination.

### 10.2 The Rejection

`aq` explicitly rejects everything else about Wave:

| Wave | aq | Why |
|------|-----|-----|
| OT for consistency | No consistency model | File overlap is set intersection, not OT |
| XMPP federation | Filesystem broadcast | No network service required |
| Protocol Buffers | NDJSON | Human-readable, zero dependencies |
| Wavelet data model | Flat broadcast messages | No hierarchy, no documents |
| Server acknowledgments | TTL-based expiry | No round-trips, no state |
| X.509 certificates | None (local trust) | Filesystem permissions suffice |
| Persistent operation log | Ephemeral broadcasts | Silence is normal; messages expire |

### 10.3 The Design Principle

Wave proved that presence-as-stream works. Wave also proved that coupling
presence to a consistency model, a transport protocol, a data model, and a
federation system creates a product nobody can use.

`aq` takes the presence semantics and drops everything else. This is not
a simplification of Wave. It is a recognition that Wave's presence layer was
the valuable part, and it was buried under machinery that served a different
purpose (collaborative document editing) that `aq` does not need.

The CLAUDE.md axiom captures this precisely: "Wave's value was the ambient
presence stream, not the data model. `aq` takes the presence semantics and
drops the complexity."

---

## 11. Sources

### Primary Sources (Official Protocol Whitepapers)

These are the surviving official specifications, hosted on Apache SVN:

- [Google Wave Federation Architecture](https://svn.apache.org/repos/asf/incubator/wave/whitepapers/google-wave-architecture/google-wave-architecture.html)
- [Google Wave Federation Protocol Over XMPP](https://svn.apache.org/repos/asf/incubator/wave/whitepapers/federation/wavespec.html)
- [Google Wave Operational Transformation](https://svn.apache.org/repos/asf/incubator/wave/whitepapers/operational-transform/operational-transform.html)
- [Google Wave Conversation Model](https://svn.apache.org/repos/asf/incubator/wave/whitepapers/conversation/convspec.html)
- [Google Wave Client-Server Protocol](https://svn.apache.org/repos/asf/incubator/wave/whitepapers/client-server-protocol/client-server-protocol.html)

### Code Repositories

- [wave-protocol/wave on GitHub](https://github.com/wave-protocol/wave) -- Reference implementation
- [Wave Protocol on Google Code Archive](https://code.google.com/archive/p/wave-protocol) -- Original repository (read-only)
- [pires/wave on GitHub](https://github.com/pires/wave) -- Fork with protobuf module documentation
- [anorth/wave on GitHub](https://github.com/anorth/wave) -- Another fork of the Apache Wave codebase

### Secondary Sources

- [Google Wave Federation Protocol -- Wikipedia](https://en.wikipedia.org/wiki/Google_Wave_Federation_Protocol)
- [Google Wave -- Wikipedia](https://en.wikipedia.org/wiki/Google_Wave)
- [Google Wave Federation Protocol -- HandWiki](https://handwiki.org/wiki/Google_Wave_Federation_Protocol)
- [Google Wave's Architecture -- InfoQ (2009)](https://www.infoq.com/news/2009/06/wave/)
- [Apache Wave -- retired project notice](https://cwiki.apache.org/confluence/display/WAVE/)
- [Apache Wave Incubator Proposal](https://cwiki.apache.org/confluence/display/INCUBATOR/WaveProposal)

### Retrospectives and Analysis

- [Google Wave's Failure: Lessons for Modern Collaboration -- Taskade](https://www.taskade.com/blog/google-wave-lessons)
- [On the Death of Google/Apache Wave -- Thought Shrapnel](https://thoughtshrapnel.com/2018/02/19/on-the-death.html)
- [Google Wave Drips With Ambition -- TechCrunch (2009)](https://techcrunch.com/2009/05/28/google-wave-drips-with-ambition-can-it-fulfill-googles-grand-web-vision/)
- [The Google Wave That Crashed -- TechCrunch (2010)](https://techcrunch.com/2010/08/10/google-wave-death/)
- [Google Wave's Failure: 10 Reasons Why -- eWeek](https://www.eweek.com/cloud/google-waves-failure-10-reasons-why/)
- [The Genius Brothers Behind Google Wave -- CNN (2009)](https://www.cnn.com/2009/TECH/10/27/rasmussen.brothers.google.wave/index.html)
- [Google Wave Developer Blog -- Federation Protocol Updates (2009)](https://googlewavedev.blogspot.com/2009/07/google-wave-federation-protocol-and.html)

### Continuation Projects

- [SwellRT -- Wikipedia](https://en.wikipedia.org/wiki/SwellRT)
- [SwellRT -- Berkman Klein / Google Summer of Code](https://cyber.harvard.edu/gsoc/SwellRT)
- [Apache Wave -- Encyclopedia MDPI](https://encyclopedia.pub/entry/30913)

### Original waveprotocol.org

The original protocol website at `https://www.waveprotocol.org/` is offline
as of 2026. The Wayback Machine has snapshots but they are not consistently
accessible. The Apache SVN whitepapers listed above contain the substantive
protocol specifications that were originally linked from waveprotocol.org.
