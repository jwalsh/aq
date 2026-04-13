# Transport Research: Alternative Channels for aq

> **Navigation**: This is the deep-dive research document on gossip transport
> theory. For the user-facing transport reference, see
> [TRANSPORTS.org](../TRANSPORTS.org).

> **Purpose**: Comprehensive research into alternative transports, persistence
> mechanisms, gossip protocols, and distribution channels for `aq`. This document
> is the deep version of what was planned for `TRANSPORTS.org`. It evaluates each
> option against aq's foundational axiom: gossip, not coordination. Filesystem is
> the only *required* transport; everything else is a tier.
>
> **Audience**: Someone who has read CLAUDE.md, WAVE-PROTOCOL.md, and the
> PRESENTATION.org deck and wants the full picture before implementing pluggable
> channels.
>
> **Date**: 2026-03-14

---

## Table of Contents

1. [Formal Gossip Theory](#1-formal-gossip-theory)
2. [Gossip Protocol Implementations](#2-gossip-protocol-implementations)
3. [Lightweight Message Transports](#3-lightweight-message-transports)
4. [Federated and Decentralized Protocols](#4-federated-and-decentralized-protocols)
5. [Enterprise and Historical Precedents](#5-enterprise-and-historical-precedents)
6. [Email as Transport](#6-email-as-transport)
7. [Wave Protocol Lessons](#7-wave-protocol-lessons)
8. [Config-Driven Channel Abstraction](#8-config-driven-channel-abstraction)
9. [Transport Comparison Matrix](#9-transport-comparison-matrix)
10. [Recommendations](#10-recommendations)
11. [References](#11-references)

---

## 1. Formal Gossip Theory

### 1.1 The Demers Foundation

Everything in gossip protocols traces back to Demers et al., "Epidemic
Algorithms for Replicated Database Maintenance" (1987, Xerox PARC). The paper
establishes three dissemination strategies:

1. **Direct mail**: Each update is immediately sent from entry site to all other
   sites. Simple, but requires knowing every peer and assumes reliable delivery.
2. **Anti-entropy**: Every site regularly chooses another site at random,
   exchanges database contents, and resolves differences. Highly reliable but
   expensive--O(n) per exchange. Equivalent to the SI model from epidemiology
   (simple epidemic): once infected, always infected.
3. **Rumor mongering**: A newly received update becomes a "hot rumor." The
   holder periodically chooses a random peer and shares it. If the peer already
   has the update, there is a 1/k chance of stopping propagation. Equivalent to
   the SIR model (susceptible-infected-removed). Fast and cheap, but a nonzero
   probability of nodes never receiving the update.

**Implication for aq**: aq currently implements something closest to *direct
mail* via filesystem writes (every broadcast is written once, every reader
scans all files). There is no randomized peer selection, no convergence
protocol, no anti-entropy. This works because aq operates on a single
filesystem with a small number of agents. It would not scale to a distributed
setting without adopting at least rumor mongering.

### 1.2 Push vs Pull vs Push-Pull

Anti-entropy comes in three flavors:

| Mode      | Initiator sends | Initiator receives | Convergence speed |
|-----------|-----------------|--------------------|--------------------|
| Push      | Its updates     | Nothing            | Slow (logarithmic) |
| Pull      | Request         | Missing updates    | Faster             |
| Push-Pull | Its updates     | Missing updates    | Fastest (O(log n) rounds) |

**Key result from theory**: In a uniform random topology, *pull converges
noticeably more quickly than push* (Demers et al.). Push-pull converges in
O(log n) rounds with high probability. Pure push has a "last few" problem
where the final uninfected nodes are hard to reach.

**Implication for aq**: aq's filesystem model is pull-only: readers pull by
scanning the broadcast directory. This is fine for a single machine. For
multi-machine, push-pull would be optimal: broadcast to known peers (push) and
periodically scan for missed messages (pull).

### 1.3 Convergence Guarantees

Anti-entropy guarantees eventual consistency with probability 1 (given infinite
time). Rumor mongering guarantees eventual consistency with probability
1 - 1/e^(k+1) where k is the cessation threshold.

The practical hybrid: **use rumor mongering for fast dissemination, and run
anti-entropy infrequently in the background to catch stragglers.** This is
exactly what Cassandra does.

**Implication for aq**: aq's TTL-based expiry means convergence is not a
correctness requirement. A missed broadcast simply expires. The cost of missing
a broadcast is a potential merge conflict, not data inconsistency. This relaxed
requirement means aq can use the cheapest dissemination strategy and tolerate
message loss--which is exactly the gossip axiom.

### 1.4 Epidemic Broadcast Trees (PlumTree)

PlumTree (Leitao, Pereira, Rodrigues, 2007) hybridizes gossip and spanning
trees to minimize redundant message transmissions:

- **Eager peers**: receive the full message payload immediately. These form a
  spanning tree.
- **Lazy peers**: receive only message digests (IDs) at periodic intervals.
  These are backup paths for tree repair.
- **Tree construction**: When a node receives a duplicate message (via a cycle
  in the initial random overlay), it sends a PRUNE to the sender, demoting
  that link from eager to lazy. Over time, cycles are pruned into a tree.
- **Tree repair**: If a lazy peer's digest reveals a message the node has not
  received (a gap), it sends a GRAFT to promote the lazy link back to eager and
  requests redelivery.

PlumTree typically runs atop HyParView for membership management. HyParView
maintains a partial view of the cluster: each node connects to only a small
subset (~log n) of the total membership.

**Implication for aq**: PlumTree's eager/lazy distinction maps to aq's
potential transport tiers. The filesystem is the "eager" path (always present,
low latency on localhost). A network transport (MQTT, NATS) would be the
"lazy" backup path for cross-machine scenarios. The GRAFT/PRUNE semantics
suggest how aq could dynamically switch between local and remote transports
based on message flow.

---

## 2. Gossip Protocol Implementations

### 2.1 Cassandra Gossip

**How it works**: Cassandra nodes gossip every second. Each gossip round picks a
random peer and exchanges state digests. If a digest reveals new information, a
full state exchange follows. Gossip carries:

- Application state (tokens, schema version, load)
- Heartbeat state (generation, version)
- Endpoint state (alive, dead, bootstrapping)

**Failure detection**: Cassandra uses the Phi Accrual Failure Detector
(Hayashibara et al., 2004)--a probabilistic detector that outputs a continuous
suspicion level (phi) instead of a binary alive/dead determination. The phi
value is computed from a sliding window of inter-arrival times of gossip
messages. When phi exceeds `phi_convict_threshold` (default 8), the node is
marked DOWN. This adapts automatically to network jitter without manual
timeout tuning.

**What aq can learn**:
- The phi accrual approach could replace aq's hard TTL with adaptive expiry.
  Instead of "broadcast expires in 300 seconds," use "broadcast expires when
  the probability that the agent is still active drops below a threshold." This
  directly addresses the TTL cliff problem (DOGFOODING.md failure #3).
- Cassandra gossips *every second*. aq broadcasts once and expects the message
  to survive on TTL. A periodic re-announcement (heartbeat) at Cassandra's
  cadence would prevent TTL expiry entirely--at the cost of more filesystem
  writes. C-7 (heartbeat conjecture) should study Cassandra's 1-second cadence
  as a design point.

### 2.2 HashiCorp Serf / Memberlist

**Architecture**: Serf is built on `memberlist`, a Go library implementing a
modified SWIM protocol. The hierarchy is:

```
Consul
  └── Serf (membership, events, Vivaldi coordinates, Lamport clocks)
       └── memberlist (SWIM implementation: ping, ping-req, suspect, state sync)
```

**SWIM modifications in memberlist**:
- Increased propagation speed via compound messages (multiple state changes
  per packet)
- Piggyback dissemination: membership updates are attached to ping, ping-req,
  and ack messages rather than sent separately
- Lifeguard enhancement: situational awareness to prevent false failure
  detection when the local node is under resource pressure (CPU/network
  exhaustion)

**Two gossip pools in Consul**:
- **LAN pool** (port 8301): All nodes in a single datacenter. Full membership.
  Sub-second convergence.
- **WAN pool** (port 8302): One server per datacenter. Federates across
  datacenters. Tolerates higher latency.

**Go API surface** (from memberlist):
```go
list, err := memberlist.Create(memberlist.DefaultLocalConfig())
n, err := list.Join([]string{"1.2.3.4"})
for _, member := range list.Members() {
    fmt.Printf("Member: %s %s\n", member.Name, member.Addr)
}
```

**Delegate interface** for custom metadata:
```go
type Delegate interface {
    NodeMeta(limit int) []byte
    NotifyMsg([]byte)
    GetBroadcasts(overhead, limit int) [][]byte
    LocalState(join bool) []byte
    MergeRemoteState(buf []byte, join bool)
}
```

**What aq can learn**:
- The `Delegate` interface is the exact pattern aq needs for pluggable
  transports. Each transport implements `GetBroadcasts` (publish) and
  `NotifyMsg` (subscribe). The metadata piggybacks on existing protocol
  messages.
- The LAN/WAN pool split is the model for aq's tier system: filesystem is the
  LAN pool (local, fast, always available). A network transport is the WAN pool
  (cross-machine, higher latency, optional).
- Memberlist's `DefaultLocalConfig()` vs `DefaultWANConfig()` shows how to
  tune gossip parameters (probe interval, suspicion multiplier, retransmit
  limit) for different network conditions. aq should expose similar knobs.

### 2.3 SWIM Protocol (Original)

The SWIM paper (Das, Gupta, et al., 2002, Cornell) defines:

**Failure detection**:
1. Each protocol period, node Mi picks a random member Mj and sends `ping`.
2. If Mj responds with `ack`, done.
3. If no `ack` within timeout, Mi selects k random members and sends
   `ping-req(Mj)` to each. These k nodes relay a ping to Mj and relay any ack
   back.
4. If no ack after this indirect probe, Mj is marked *suspected*.
5. Suspected nodes remain in the membership list for a configurable timeout. If
   no rebuttal, they are marked *dead*.

**Dissemination**: Membership updates (join, leave, suspect, dead) piggyback on
ping/ack messages. No separate dissemination protocol. This is the key insight:
*reuse failure detection traffic for information dissemination*.

**Convergence**: With round-robin probe target selection (visiting each member
once per round before revisiting), worst-case detection time is
`probe_interval * member_count`. In practice, random selection converges faster.

**What aq can learn**:
- SWIM's piggybacking is elegant but assumes a constantly running protocol
  with periodic probes. aq has no such protocol--broadcasts are one-shot writes.
  A daemon (`aq watch`) could implement SWIM-like periodic probes, but this
  violates the "no daemon required" constraint. The compromise: piggyback
  conflict checks on announce operations. When you announce, also check for
  conflicts. This is already the CLAUDE.md protocol.

---

## 3. Lightweight Message Transports

### 3.1 IRC (Internet Relay Chat)

**Protocol**: RFC 1459 / RFC 2812. Plain ASCII over TCP. Channels as named
groups. Messages broadcast to all channel members.

**How aq broadcasts would work over IRC**:
```
JOIN #aq-broadcast
PRIVMSG #aq-broadcast :{"v":3,"agent":"origin/feat-auth","cid":"C-1","phase":"p","files":["auth.py"],"ttl":300,...}
```
Presence is implicit: `JOIN` = alive, `PART`/`QUIT` = gone. The IRC server
maintains the channel membership list. `WHO #aq-broadcast` returns all active
agents.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Good. Fire-and-forget `PRIVMSG`. Channel = broadcast group. |
| Operational overhead | Moderate. Requires an IRC server (ircd). |
| Filesystem-first | Does not preserve. Replaces filesystem with network. |
| Coexistence | Yes. IRC as Tier 2 alongside filesystem Tier 0. |
| Failure mode | Server down = no broadcast. Degrades to filesystem. |
| TTL | Not native. Client-side expiry only. |

**Verdict**: Surprisingly natural fit. IRC was designed for ephemeral presence
broadcasts. The main drawback is operational: running an IRC daemon is more
overhead than aq should require by default. However, many development teams
already have IRC infrastructure (or a bridge to Slack/Discord). An aq IRC
transport would be ~100 lines of Python using the `irc` library.

### 3.2 MQTT (Message Queuing Telemetry Transport)

**Protocol**: OASIS standard. Pub/sub over TCP. Three QoS levels:
- **QoS 0**: Fire-and-forget. No ack, no retry, no storage. The sender
  publishes and immediately forgets.
- **QoS 1**: At-least-once. Ack required, retransmit on timeout.
- **QoS 2**: Exactly-once. Four-step handshake.

**How aq broadcasts would work over MQTT**:
```
Topic: aq/broadcast/{worktree}
QoS: 0
Payload: {"v":3,"agent":"origin/feat-auth","cid":"C-1",...}
Retained: false
```
QoS 0 is the perfect match for gossip semantics: fire-and-forget, no
obligation, missed messages are tolerable. The retained message feature
(last message on a topic survives for new subscribers) could serve as a
lightweight "current state" view, but aq's TTL semantics mean retained
messages would go stale.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Excellent. QoS 0 is literally fire-and-forget. |
| Operational overhead | Low-moderate. Mosquitto is a single binary. |
| Filesystem-first | Does not preserve. Network-dependent. |
| Coexistence | Yes. MQTT as Tier 2 or Tier 3. |
| Failure mode | Broker down = no broadcast. Graceful: QoS 0 already tolerates loss. |
| TTL | Not native at message level. Could use `$SYS` or message expiry (MQTT 5.0). |

**Verdict**: Strong candidate for Tier 2 (local network). MQTT 5.0 added
message expiry intervals, which map directly to aq's TTL. Mosquitto runs on
everything and is near-zero config. The topic hierarchy (`aq/broadcast/+`)
maps naturally to aq's channel structure. The main concern: MQTT is designed
for IoT sensor data, not developer tooling. The cultural mismatch may confuse
users expecting a more "developer-native" transport.

### 3.3 NATS

**Protocol**: Proprietary but open-source. Subjects (topics) with wildcard
matching. Core NATS is ephemeral: messages with no subscribers are discarded
immediately. JetStream adds persistence.

**How aq broadcasts would work over NATS**:
```
Subject: aq.broadcast.{worktree}
Payload: {"v":3,"agent":"origin/feat-auth","cid":"C-1",...}
```
NATS subjects are ephemeral resources that disappear when unused. Messages are
dropped if nobody is listening. This is exactly gossip semantics: broadcast
carries no obligation. NATS wildcard subscriptions (`aq.broadcast.*` or
`aq.broadcast.>`) allow subscribing to all broadcasts without knowing specific
worktree names.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Excellent. Ephemeral by design. |
| Operational overhead | Low. Single binary, ~10MB, zero config. |
| Filesystem-first | Does not preserve. Network-dependent. |
| Coexistence | Yes. NATS as Tier 2. |
| Failure mode | Server down = no broadcast. Degrades to filesystem. |
| TTL | Not native. Client-side only (or use JetStream message TTL). |

**Verdict**: Top candidate for Tier 2. NATS is the closest thing to "UDP for
microservices"--fire-and-forget, subject-based routing, zero persistence by
default. The `nats-server` binary is smaller and simpler than Mosquitto.
Subject wildcards map perfectly to aq's channel model. The Go client library
(`nats.go`) is well-maintained and idiomatic--important since aq's Go port is
the primary implementation.

### 3.4 Redis Pub/Sub

**Protocol**: Redis RESP protocol. `PUBLISH channel message` / `SUBSCRIBE
channel`. No persistence: messages not stored. If subscriber is offline, message
is lost.

**How aq broadcasts would work over Redis**:
```
PUBLISH aq:broadcast '{"v":3,"agent":"origin/feat-auth","cid":"C-1",...}'
```

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Good. Ephemeral pub/sub, sub-ms latency. |
| Operational overhead | Moderate. Requires Redis server. |
| Filesystem-first | Does not preserve. |
| Coexistence | Yes. Redis as Tier 2. |
| Failure mode | Server down = no broadcast. |
| TTL | Redis has native TTL on keys (not pub/sub messages). Could use Redis Streams with MAXLEN + TTL for persistence. |

**Verdict**: Acceptable but not optimal. Redis is overkill for gossip--it is a
full data structure server. If you already have Redis running, it is an easy
transport to add. But NATS or MQTT are better fits for aq's use case because
they are purpose-built for pub/sub without the operational overhead of a
general-purpose cache.

### 3.5 SQLite (Local Persistence)

**Mechanism**: Single-file database in WAL (Write-Ahead Logging) mode.
Concurrent readers and a single writer. Local only (WAL does not work over
network filesystems).

**How aq broadcasts would work in SQLite**:
```sql
CREATE TABLE broadcasts (
    id TEXT PRIMARY KEY,
    agent TEXT NOT NULL,
    worktree TEXT NOT NULL,
    cid TEXT NOT NULL,
    phase TEXT NOT NULL,
    status TEXT NOT NULL,
    files TEXT NOT NULL,  -- JSON array
    ts REAL NOT NULL,
    ttl INTEGER NOT NULL DEFAULT 300
);

-- Announce
INSERT INTO broadcasts VALUES (...);

-- Read active
SELECT * FROM broadcasts WHERE ts + ttl > unixepoch();

-- Archive expired
DELETE FROM broadcasts WHERE ts + ttl <= unixepoch();
```

WAL mode enables concurrent reads while a write is in progress. The
`.db-wal` and `.db-shm` sidecar files are managed automatically.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Adequate. Not fire-and-forget (writes to durable storage). |
| Operational overhead | Zero. sqlite3 is already installed everywhere. |
| Filesystem-first | Preserves. Single file replaces NDJSON directory. |
| Coexistence | Yes. SQLite as Tier 0 alternative to NDJSON. |
| Failure mode | File corruption = lost state. WAL mitigates. |
| TTL | Easy. SQL WHERE clause on timestamp. |

**Verdict**: Strong candidate as a Tier 0 *alternative* to the current NDJSON
directory. Advantages over NDJSON: atomic writes, no filesystem scan needed
(SQL query instead of glob), built-in expiry (SQL WHERE), concurrent access
handled by WAL. Disadvantages: less inspectable (need `sqlite3` CLI vs `cat`),
binary format (not human-readable at rest), single-writer bottleneck. The
`cat`-debuggability of NDJSON is a real feature--it is why aq chose NDJSON in
the first place. SQLite would be a good option for users who prefer it, but
NDJSON should remain the default.

### 3.6 Unix Domain Sockets / Named Pipes

**Mechanism**: Kernel-mediated IPC. Unix domain sockets support both stream
(TCP-like) and datagram (UDP-like) modes. Named pipes (FIFOs) are
unidirectional. Both require processes on the same machine. Zero network
overhead--data copies directly between process buffers via the kernel.

**How aq broadcasts would work**:
```
# Datagram socket (connectionless, like UDP)
Socket path: /tmp/aq-broadcast.sock
# or in $AQ_HOME
Socket path: ~/.aq/broadcast.sock

# Agent A sends datagram:
echo '{"v":3,"agent":"...","cid":"C-1",...}' | socat - UNIX-SENDTO:~/.aq/broadcast.sock

# Agent B listens:
socat UNIX-RECVFROM:~/.aq/broadcast.sock,fork -
```

Unix datagram sockets are the most natural fit: connectionless, message-
boundary-preserving, no ordering guarantees. This is the closest thing to
"UDP on localhost."

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Excellent. Datagram sockets = fire-and-forget. |
| Operational overhead | Zero. Kernel-native. No service required. |
| Filesystem-first | Preserves (socket lives in the filesystem namespace). |
| Coexistence | Yes. UDS as Tier 0.5 (same machine, faster than file I/O). |
| Failure mode | No listener = message dropped. Perfect for gossip. |
| TTL | Not native. Sender-side only. |

**Drawback**: Requires a listener process. With NDJSON files, any process can
read at any time. With sockets, someone must be `recvfrom()`-ing when the
message arrives, or it is lost. This changes the model from "write and
forget" to "send and hope someone is listening"--which is arguably more
gossip-like, but loses the auditability of filesystem state.

**Verdict**: Good for daemon-to-daemon communication (`aq watch` to `aq
watch`), but not a replacement for filesystem. Best used as an optimization
layer: when the daemon is running, use UDS for sub-millisecond notification,
and write to filesystem as the durable backup.

### 3.7 mDNS / DNS-SD (Bonjour / Avahi)

**Protocol**: RFC 6762 (mDNS) and RFC 6763 (DNS-SD). Multicast UDP on port
5353 to the `224.0.0.251` multicast group. Zero-configuration service
discovery on local networks.

**This is literally what aq analogizes to.** From CLAUDE.md: "It is the mDNS
of multi-agent development: broadcast 'does anyone know this address?', cost
near zero."

**How aq broadcasts would work**:
```
# Register a service via Avahi/Bonjour:
Service type: _aq._tcp
Instance name: agent-origin-feat-auth
TXT records:
  cid=C-1
  phase=p
  files=auth.py,session.py
  ttl=300

# Discovery:
avahi-browse -t _aq._tcp
# or
dns-sd -B _aq._tcp
```

DNS-SD services have built-in TTL semantics: the DNS TTL on the SRV/TXT
records controls how long the service advertisement persists. If the agent
stops re-announcing, the record expires and the service disappears from
discovery. This is *exactly* aq's TTL model.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Perfect. mDNS IS gossip--multicast announce, TTL expiry, no obligation. |
| Operational overhead | Zero on macOS (Bonjour built-in). Near-zero on Linux (Avahi). |
| Filesystem-first | Does not preserve. Network-based (multicast). |
| Coexistence | Yes. mDNS as Tier 1 (local network). |
| Failure mode | No multicast = no discovery. Degrades to filesystem. |
| TTL | Native. DNS record TTL. |

**Verdict**: The most philosophically aligned transport after filesystem. mDNS
*is* the protocol aq analogizes to. The fit is not accidental--aq's design was
inspired by mDNS. The limitation: DNS TXT records have a 255-byte-per-string
limit (with multiple strings allowed up to ~1300 bytes total). aq's broadcast
payload easily fits. The real concern is operational: mDNS works on local
subnets only (multicast does not cross routers without special configuration).
For same-machine agents, mDNS is unnecessary overhead compared to filesystem.
For same-network agents (e.g., a team's office LAN), mDNS is ideal.

### 3.8 D-Bus (Linux IPC)

**Protocol**: Message-oriented middleware for same-machine IPC. Session bus
(per-user, per-login-session) and system bus (system-wide). Signal broadcast
mechanism: one-to-many, fire-and-forget, no response expected.

**How aq broadcasts would work**:
```python
import dbus

bus = dbus.SessionBus()
# Emit signal
bus.emit_signal(
    '/com/aq/Broadcast',
    'com.aq.Channel',
    'Announce',
    dbus.String('{"v":3,"agent":"...","cid":"C-1",...}')
)

# Subscribe to signals
bus.add_signal_receiver(
    handler,
    signal_name='Announce',
    dbus_interface='com.aq.Channel',
    path='/com/aq/Broadcast'
)
```

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Good. Signals are broadcast, no response. |
| Operational overhead | Zero on Linux (dbus-daemon is always running). |
| Filesystem-first | Does not preserve. |
| Coexistence | Yes. D-Bus as Tier 0.5 (Linux only). |
| Failure mode | dbus-daemon crash = IPC failure. Rare. |
| TTL | Not native. Application-level. |
| Platform | Linux and some BSDs only. Not macOS (deprecated), not Windows. |

**Verdict**: Linux-only is a dealbreaker for a cross-platform tool. D-Bus is
also overly complex for aq's needs--the interface/path/member naming convention
is enterprise middleware dressed as IPC. However, for a Linux-only deployment,
D-Bus session bus signals are zero-config, zero-overhead, and fire-and-forget.
If aq ever has a "detect all agents on this machine" feature, D-Bus service
registration (via `org.freedesktop.DBus.ListNames`) would be the right
mechanism on Linux.

### 3.9 Plan 9 / 9P Protocol

**Protocol**: The Plan 9 filesystem protocol. Everything is a file. Network
services are exposed as filesystem trees. 9P is a generic, medium-agnostic,
byte-oriented protocol with 17 message types for navigating, reading, and
writing files.

**How aq broadcasts would work**:
```
# Mount a remote aq channel as a filesystem:
9pfuse remote-host:5640 /mnt/aq

# Announce (same as filesystem!):
echo '{"v":3,"agent":"...","cid":"C-1",...}' > /mnt/aq/broadcast/aq-$(date +%s)-$(ulid).json

# Read active (same as filesystem!):
cat /mnt/aq/broadcast/aq-*.json
```

The insight: if aq already works with filesystem operations, and 9P exposes
remote resources as a filesystem, then aq works over 9P *with zero code
changes*. The transport is transparent. This is Plan 9's whole philosophy.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Good. Filesystem semantics preserved. |
| Operational overhead | Moderate. Requires 9P server (u9fs, diod, plan9port). |
| Filesystem-first | Perfectly preserves. The transport IS the filesystem. |
| Coexistence | Yes. 9P as Tier 1 (transparent upgrade from Tier 0). |
| Failure mode | Mount failure = fall back to local filesystem. |
| TTL | Inherited from aq's filesystem implementation. |

**Verdict**: The most elegant transport for aq. Zero code changes required.
The aq binary does not even know it is talking to a remote machine--it just
reads and writes files. The obstacle is adoption: 9P is obscure. Nobody has
a 9P server running. But for teams willing to set one up (or use FUSE-based
mounts), this is the purest expression of aq's filesystem-first axiom applied
to multi-machine scenarios. Also worth noting: Tailscale has explored 9P-style
filesystem sharing over WireGuard tunnels.

### 3.10 Tailscale / WireGuard Subnet

**Mechanism**: Tailscale creates a mesh VPN over WireGuard, assigning each
device a stable IP in the `100.x.y.z` range. Nodes can access each other's
filesystems via NFS, SMB, or SSHFS mounts over the Tailscale network.

**How aq broadcasts would work**:
```bash
# Mount remote AQ_HOME via SSHFS over Tailscale:
sshfs user@100.x.y.z:~/.aq /mnt/remote-aq

# Or symlink:
ln -s /mnt/remote-aq/channels/broadcast ~/.aq/channels/remote-broadcast

# aq reads both local and remote channels:
AQ_CHANNELS="broadcast,remote-broadcast" aq status
```

Like 9P, this is a transparent transport: aq reads and writes files, and the
VPN + network filesystem makes remote files appear local.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Adequate (filesystem semantics, but high latency). |
| Operational overhead | Moderate. Requires Tailscale + network filesystem. |
| Filesystem-first | Preserves. |
| Coexistence | Yes. VPN mount as Tier 1. |
| Failure mode | VPN down or mount stale = no remote broadcasts. |
| TTL | Inherited from aq. |

**Verdict**: Pragmatic for cross-machine scenarios if you already have
Tailscale. Not worth setting up just for aq. The latency of NFS/SSHFS reads
(tens to hundreds of milliseconds) may violate C-1's p99 < 500ms target at
scale. Better for "casual cross-machine presence" than for high-frequency
gossip.

### 3.11 ZeroMQ

**Protocol**: Brokerless messaging library. Sockets with built-in patterns
(PUB/SUB, REQ/REP, PUSH/PULL, etc.). Peer-to-peer, no central broker.

**How aq broadcasts would work**:
```python
import zmq

# Publisher
ctx = zmq.Context()
pub = ctx.socket(zmq.PUB)
pub.bind("tcp://*:5555")
pub.send_json({"v": 3, "agent": "...", "cid": "C-1", ...})

# Subscriber
sub = ctx.socket(zmq.SUB)
sub.connect("tcp://publisher:5555")
sub.subscribe(b"")  # subscribe to all
msg = sub.recv_json()
```

ZeroMQ PUB/SUB is fire-and-forget: "Pub-sub is like a radio broadcast; you
miss everything before you join, and then how much information you get depends
on the quality of your reception."

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Good. PUB/SUB is fire-and-forget. Brokerless. |
| Operational overhead | Low. Library, not service. But requires binding to a port. |
| Filesystem-first | Does not preserve. |
| Coexistence | Yes. ZeroMQ as Tier 2. |
| Failure mode | Publisher down = no messages. Subscriber reconnects automatically. |
| TTL | Not native. Application-level. |

**Verdict**: ZeroMQ's brokerless design is attractive--no server to run. But
it requires each agent to bind a network port, which introduces discovery
problems (how does agent B find agent A's port?). This is the problem mDNS
solves, leading to a ZeroMQ+mDNS combination that is more complex than just
using NATS. ZeroMQ is best suited for embedded/high-performance scenarios.
For aq's use case, NATS provides the same fire-and-forget semantics with
much simpler setup.

---

## 4. Federated and Decentralized Protocols

### 4.1 Matrix Protocol

**Architecture**: Federated rooms replicated across homeservers. Events are
JSON objects sent to rooms. All participants receive events. Rooms are
replicated across every homeserver with a participant--no single server owns
a room.

**Presence in Matrix**: Native. Homeservers push presence information
(online/offline/unavailable) as part of the federation protocol.

**How aq broadcasts would work**:
```json
{
  "type": "com.aq.broadcast",
  "room_id": "!aqbroadcast:homeserver.local",
  "content": {
    "v": 3,
    "agent": "origin/feat-auth",
    "cid": "C-1",
    "phase": "p",
    "files": ["auth.py"],
    "ttl": 300
  }
}
```

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Moderate. Matrix is designed for persistent, ordered messaging. |
| Operational overhead | High. Requires Synapse/Dendrite homeserver. |
| Filesystem-first | Does not preserve. |
| Coexistence | Yes, but heavy. Matrix as Tier 3. |
| Failure mode | Homeserver down = no federation. |
| TTL | Not native for messages. State events have replacement semantics. |

**Verdict**: Overkill for aq. Matrix is a full communication platform with
persistence, ordering, and state resolution. aq needs none of this. However,
for organizations already running Matrix (e.g., Element), an aq Matrix bot
that posts broadcasts to a dedicated room would be easy to build and would
give human observers a live view of agent activity. This is a monitoring
integration, not a primary transport.

### 4.2 ActivityPub

**Protocol**: W3C standard for federated social networking. JSON-LD objects.
The `Announce` activity is a first-class verb meaning "share/boost."

**How aq broadcasts would work**:
```json
{
  "@context": "https://www.w3.org/ns/activitystreams",
  "type": "Announce",
  "actor": "https://aq.local/agents/origin-feat-auth",
  "object": {
    "type": "Note",
    "content": "Prosecuting C-1 (proof phase) on auth.py"
  },
  "published": "2026-03-14T12:00:00Z"
}
```

The `Announce` activity type is literally what aq does. The semantic
alignment is uncanny.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Moderate. ActivityPub is request/response (HTTP POST to inbox). |
| Operational overhead | High. Requires ActivityPub server, HTTP signatures, JSON-LD. |
| Filesystem-first | Does not preserve. |
| Coexistence | Possible but awkward. |
| Failure mode | Server down = no delivery. |
| TTL | Not native. |

**Verdict**: Fascinating conceptual alignment, impractical implementation. The
ActivityPub `Announce` activity is semantically identical to `aq announce`.
But ActivityPub requires HTTP endpoints, JSON-LD processing, and HTTP
Signatures--far more machinery than aq should carry. The lesson: aq and
ActivityPub solve the same problem (broadcasting activity) in different
contexts (local dev vs. federated social). If aq ever needs to federate with
external systems, ActivityPub is the right vocabulary.

### 4.3 Nostr

**Protocol**: Decentralized event relay protocol. Events are JSON objects with
an id (SHA-256 hash), pubkey, kind (integer type), tags, content, and sig
(Schnorr signature). Clients publish events to relays. Relays store and
forward events. Relays do not gossip with each other--clients push to multiple
relays independently.

**Event structure**:
```json
{
  "id": "<sha256>",
  "pubkey": "<hex>",
  "created_at": 1678884000,
  "kind": 1,
  "tags": [["t", "aq"], ["c", "C-1"]],
  "content": "{\"phase\":\"proof\",\"files\":[\"auth.py\"]}",
  "sig": "<schnorr>"
}
```

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Good-to-moderate. Events are fire-and-forget to relays. But relays persist. |
| Operational overhead | Moderate. Requires a relay (can self-host, e.g., strfry). |
| Filesystem-first | Does not preserve. |
| Coexistence | Yes. Nostr as Tier 3 (public/semi-public broadcast). |
| Failure mode | Relay down = events queued client-side. |
| TTL | Nostr events are persistent by default. Ephemeral events (NIP-16) exist but are less supported. |

**Verdict**: Nostr's event model is close to aq's broadcast model.
Both are signed, timestamped, typed JSON blobs. The key difference: Nostr
events are persistent and content-addressed; aq broadcasts are ephemeral and
TTL-expiring. Nostr's lack of a gossip layer between relays (clients must
push to multiple relays) mirrors aq's design--no relay-to-relay coordination.
The cryptographic identity model (Schnorr signatures over secp256k1) is more
robust than aq's `{remote}/{branch}` agent identity but far heavier.

### 4.4 Secure Scuttlebutt (SSB)

**Protocol**: Gossip-native. Each identity has exactly one append-only log
(feed). Messages are signed with Ed25519. Replication follows the social
graph: you replicate feeds of people you follow and their friends (2-3 hops).

**Gossip replication**: Peers exchange messages via the `createHistoryStream`
RPC or Epidemic Broadcast Trees (EBT). EBT uses vector clocks to track which
messages each peer holds, enabling selective sync.

**Secret Handshake**: Four-step mutual authentication providing identity
verification, shared secret, and forward secrecy.

**Evaluation**:
| Criterion | Assessment |
|-----------|------------|
| Gossip fit | Excellent in theory. SSB IS a gossip protocol. |
| Operational overhead | High. Requires ssb-server, social graph setup, key management. |
| Filesystem-first | Partially. SSB stores feeds as files (append-only logs). |
| Coexistence | Possible but very heavy for aq's use case. |
| Failure mode | Works offline. Syncs when peers reconnect. Graceful. |
| TTL | No TTL. Append-only means messages persist forever. |

**Verdict**: SSB is the most gossip-native protocol in this survey. Its
append-only log model, social-graph replication, and offline-first design are
philosophically aligned with aq. But it is massively overengineered for aq's
use case. The append-only model directly contradicts aq's TTL-based expiry:
SSB messages never expire. The key management overhead (Ed25519 keys, secret
handshake) is inappropriate for local dev agents. SSB is the right model for
a decentralized social network; it is the wrong model for a local presence
layer.

**What aq can learn from SSB**: The EBT replication strategy (vector clocks
for selective sync) is relevant if aq ever needs to sync broadcast state
between machines. Instead of replicating all broadcasts, peers would exchange
"I have broadcasts up to sequence N" and only transfer the delta. This is
more efficient than the current model of "scan all files."

---

## 5. Enterprise and Historical Precedents

### 5.1 WS-Notification / WS-Eventing (SOAP Era)

**WS-Eventing** (W3C, 2004): SOAP-based protocol for subscribing to
notifications. A "subscriber" registers interest with an "event source" and
receives "notifications" as unsolicited SOAP messages. Push model. Expiration
timestamps on subscriptions (a form of TTL).

**WS-BaseNotification** (OASIS): Defines topic-based publish/subscribe using
SOAP. Topics, filters, notification broker.

The PRESENTATION.org deck already identifies this parallel:

> "Every failure has a 2005 spec. UDDI expiry = TTL cliff. WS-Notification =
> broadcast semantics."

**What aq can learn**:
- WS-Eventing's subscription expiration is aq's TTL by another name. The
  failure mode is identical: subscribers that do not renew their subscription
  are silently dropped. WS-Eventing addressed this with `Renew` messages--
  exactly what C-7 (heartbeat) proposes.
- WS-BaseNotification's topic filtering maps to aq's conjecture-based
  filtering. The concept is sound; the implementation (XML Schema, SOAP
  envelopes, WS-Addressing headers) was the problem.
- The death of WS-Notification teaches a design lesson: *the protocol was
  correct but the serialization format killed adoption.* JSON + TTL gets the
  same semantics with 1/10 the complexity. aq is WS-Notification with the
  XML tax removed.

### 5.2 UDDI (Universal Description, Discovery, and Integration)

**What it was**: An XML-based registry for web services. Businesses published
service descriptions (WSDL). Consumers discovered services by querying the
registry. Public UDDI registries operated by IBM, Microsoft, and SAP.

**What killed it**: The public registries shut down in 2006. Nobody
re-registered their services. The registry went stale. Microsoft deprecated
UDDI services entirely by 2013. The OASIS technical committee closed in 2007.

**The TTL cliff parallel**: UDDI registries had no automatic expiry. Services
registered once and were never updated. When services moved or shut down, the
registry contained stale entries. The solution was manual curation, which
nobody did. This is exactly aq's TTL cliff problem (DOGFOODING.md failure #3)
in a different era: without automatic re-announcement, the state goes stale.

**What aq can learn**:
- *Automatic expiry is non-negotiable.* UDDI's failure was the absence of TTL.
  aq has TTL but lacks automatic re-announcement. The solution is the same in
  both cases: enforce periodic re-registration (aq calls this "heartbeat").
- *Centralized registries die from neglect.* UDDI required a central registry
  that somebody had to maintain. aq's filesystem-first, no-server approach
  avoids this failure mode entirely. Every agent writes to a shared directory;
  no central registry to maintain.

### 5.3 XMPP PEP (Personal Eventing Protocol)

**XEP-0163**: Every XMPP account is a virtual pubsub service. Presence
subscriptions automatically create PEP subscriptions. Notifications are
filtered based on the subscriber's expressed capabilities (via Entity
Capabilities). Smart defaults: you receive events from your contacts'
personal pubsub nodes without explicit subscription setup.

**Key principles**:
1. Every account is a pubsub service
2. One publisher per node
3. Use presence to drive subscriptions
4. Filter notifications based on expressed interest
5. Smart defaults

**What aq can learn**:
- PEP's insight that *presence drives subscriptions* is powerful. In aq terms:
  you should automatically receive broadcasts from agents in your worktree
  group without explicit subscription. The filesystem model already does this
  (everyone reads the same directory), but a network transport would need this
  principle.
- PEP's filtering by expressed interest maps to aq's potential conjecture-
  based filtering: "only show me conflicts for conjectures I care about."
- PEP's "one publisher per node" maps to aq's "one agent per worktree."

### 5.4 Zeroconf / Bonjour Service Discovery

See section 3.7 (mDNS / DNS-SD). The Zeroconf suite (link-local addressing +
mDNS + DNS-SD) is the closest existing standard to what aq does.

### 5.5 Consul Gossip (HashiCorp)

See section 2.2. Consul's LAN/WAN gossip pool split is the model for aq's
tier system. Additional Consul-specific notes:

- **Consul's scale test** (published by HashiCorp) demonstrated gossip
  stability at 10,000 nodes with sub-second convergence. aq's target is
  orders of magnitude smaller (10 agents), but the test methodology
  (synthetic load, p99 measurement) is directly applicable to C-1 validation.
- **Consul's key rotation** (`consul keyring`) shows how to handle transport
  encryption without pre-shared static keys. If aq ever needs encrypted
  broadcasts, Consul's approach is the reference implementation.

---

## 6. Email as Transport

### 6.1 SMTP for Fire-and-Forget Broadcasts

**Concept**: An agent announces by sending an email to a mailing list. The
broadcast payload is the email body (JSON or plain text). SMTP relay is the
dissemination mechanism. IMAP folders are the channel directories.

```
From: agent-alpha@worktree.local
To: aq-broadcast@team.example.com
Subject: [aq] C-1 proof auth.py
Content-Type: application/json

{"v":3,"agent":"origin/feat-auth","cid":"C-1","phase":"p","files":["auth.py"],"ttl":300}
```

### 6.2 How Absurd Is This?

**Arguments for**:
- SMTP is the original fire-and-forget broadcast protocol. Send and move on.
- Email infrastructure is everywhere. Every organization has an SMTP server.
- Mailing lists are a natural broadcast channel with built-in subscription
  management.
- IMAP folders provide searchable, persistent storage with date-based expiry
  (auto-archive rules = TTL).
- Human-readable. Anyone can see what agents are doing by reading their email.
- Store-and-forward architecture: tolerates intermittent connectivity.
- Built-in authentication (SMTP AUTH, DKIM, SPF).

**Arguments against**:
- Latency. SMTP is measured in seconds to minutes, not milliseconds. Too slow
  for real-time conflict detection.
- Overhead per message. SMTP envelope, headers, MIME encoding--hundreds of
  bytes of overhead for a ~200-byte JSON payload.
- Rate limiting. Most SMTP servers throttle high-frequency senders. An agent
  announcing every 30 seconds would be rate-limited or flagged as spam.
- Cultural mismatch. "My CI agent is sending me 50 emails an hour" is a
  support ticket, not a feature.
- Polling required. IMAP IDLE helps, but real-time notification requires
  additional infrastructure (push notifications, webhooks).

### 6.3 Verdict

**Absurd as a primary transport. Surprisingly practical as a notification
channel.** Email is the wrong choice for high-frequency gossip (millisecond
latency, hundreds of messages per minute). But it is a reasonable choice for
low-frequency summary notifications: "Daily digest: 47 broadcasts, 3 HIGH
conflicts detected." An `aq notify --email` command that sends a conflict
summary to a mailing list would be useful. This is not a transport--it is a
reporting integration.

The deeper insight: email's store-and-forward model is architecturally similar
to aq's filesystem model. Both persist messages to durable storage. Both allow
asynchronous reads. Both tolerate sender and receiver being active at different
times. The difference is latency and overhead. If aq's filesystem transport is
"email to yourself on localhost," then actual email is the natural extension
for cross-organization broadcasts where latency tolerance is high.

---

## 7. Wave Protocol Lessons

The full Wave analysis is in `docs/WAVE-PROTOCOL.md`. Here is what matters for
transport design.

### 7.1 Presence as a Side Effect of the Data Stream

Wave had no dedicated "presence service." Presence was embedded in the
operation stream: if you received operations from a user, that user was active.
Cursor annotations were just another document mutation.

**aq's transport design should preserve this property.** A broadcast IS the
presence signal. There is no separate "agent is alive" heartbeat and "agent is
working on X" announcement. They are the same message. Any transport that
separates presence from content (e.g., a health check endpoint + a separate
message queue) violates this principle.

### 7.2 The Coupling Trap

Wave coupled presence to OT, XMPP federation, protobuf serialization, and a
five-layer data model. You could not get the presence semantics without buying
the entire stack.

**aq's transport design must avoid this.** Each transport should be
independently adoptable. Adding MQTT should not require changing the broadcast
payload format. Adding mDNS should not require a running daemon. The transport
is a pipe; the payload is unchanged.

### 7.3 What SwellRT Learned

SwellRT (the Wave continuation) replaced XMPP with Matrix in 2016. This
validated that Wave's transport choice was the weak point, while the core
semantics had lasting value. The lesson for aq: the transport will change.
Design the abstraction layer so that transport replacement is a configuration
change, not a code change.

---

## 8. Config-Driven Channel Abstraction

### 8.1 The Minimal Interface

Based on the research above, the channel abstraction needs exactly three
operations:

```python
# Python
from abc import ABC, abstractmethod
from typing import Iterator
from aq.protocol import Broadcast

class Channel(ABC):
    @abstractmethod
    def publish(self, broadcast: Broadcast) -> None:
        """Write a broadcast. Fire-and-forget."""
        ...

    @abstractmethod
    def subscribe(self) -> Iterator[Broadcast]:
        """Yield broadcasts as they arrive. Blocking iterator."""
        ...

    @abstractmethod
    def active(self) -> list[Broadcast]:
        """Return all non-expired broadcasts. Point-in-time snapshot."""
        ...
```

```go
// Go
type Channel interface {
    Publish(broadcast Broadcast) error
    Subscribe(ctx context.Context) <-chan Broadcast
    Active() ([]Broadcast, error)
}
```

**Why three methods**:
- `Publish` = `aq announce`. Write a broadcast. Caller does not wait for
  delivery confirmation (fire-and-forget). Error return is for local failures
  only (filesystem full, network unreachable), not delivery failures.
- `Subscribe` = `aq watch`. Stream of broadcasts as they arrive. Blocking.
  Used by the daemon.
- `Active` = `aq status`. Snapshot of all non-expired broadcasts. Used by the
  CLI for one-shot queries.

### 8.2 Filesystem Implementation (Tier 0)

The current implementation maps directly:

```python
class FilesystemChannel(Channel):
    def __init__(self, base: Path):
        self.requests = base / "requests"
        self.archive = base / "archive"

    def publish(self, broadcast: Broadcast) -> None:
        self.requests.mkdir(parents=True, exist_ok=True)
        path = self.requests / f"aq-{int(broadcast.ts):014d}-{broadcast.id}.json"
        path.write_text(broadcast.to_json() + "\n")

    def subscribe(self) -> Iterator[Broadcast]:
        # Use FSEvents (macOS) / inotify (Linux) to watch self.requests
        raise NotImplementedError("Requires daemon")

    def active(self) -> list[Broadcast]:
        if not self.requests.exists():
            return []
        out = []
        for f in sorted(self.requests.glob("aq-*.json")):
            try:
                b = Broadcast.from_json(f.read_text().strip())
                if b.is_expired():
                    self.archive.mkdir(exist_ok=True)
                    f.rename(self.archive / f.name)
                else:
                    out.append(b)
            except Exception:
                pass
        return out
```

### 8.3 NATS Implementation Sketch (Tier 2)

```python
import nats

class NATSChannel(Channel):
    def __init__(self, url: str = "nats://localhost:4222", subject: str = "aq.broadcast"):
        self.url = url
        self.subject = subject
        self._recent: list[Broadcast] = []

    async def publish(self, broadcast: Broadcast) -> None:
        nc = await nats.connect(self.url)
        await nc.publish(self.subject, broadcast.to_json().encode())
        await nc.close()

    async def subscribe(self) -> AsyncIterator[Broadcast]:
        nc = await nats.connect(self.url)
        sub = await nc.subscribe(self.subject)
        async for msg in sub.messages:
            b = Broadcast.from_json(msg.data.decode())
            if not b.is_expired():
                self._recent.append(b)
                yield b

    def active(self) -> list[Broadcast]:
        # Prune expired from in-memory cache
        self._recent = [b for b in self._recent if not b.is_expired()]
        return self._recent
```

Note the `active()` limitation: NATS has no persistence, so `active()` can
only return broadcasts received since this process started. This is why
filesystem remains Tier 0--it provides durable state that survives process
restarts.

### 8.4 Multi-Channel Composition

The most powerful pattern: compose multiple channels. Publish to all, read
from all, deduplicate by broadcast ID.

```python
class MultiChannel(Channel):
    def __init__(self, channels: list[Channel]):
        self.channels = channels

    def publish(self, broadcast: Broadcast) -> None:
        for ch in self.channels:
            try:
                ch.publish(broadcast)
            except Exception:
                pass  # Gossip tolerates publication failure

    def active(self) -> list[Broadcast]:
        seen = set()
        out = []
        for ch in self.channels:
            try:
                for b in ch.active():
                    if b.id not in seen:
                        seen.add(b.id)
                        out.append(b)
            except Exception:
                pass
        return sorted(out, key=lambda b: b.ts)
```

### 8.5 Configuration

```toml
# ~/.aq/config.toml

[channels]
# Tier 0: always present
filesystem = { enabled = true, path = "~/.aq/channels/broadcast" }

# Tier 1: local network (optional)
# mdns = { enabled = false, service_type = "_aq._tcp" }

# Tier 2: requires a service (optional)
# nats = { enabled = false, url = "nats://localhost:4222", subject = "aq.broadcast" }
# mqtt = { enabled = false, url = "mqtt://localhost:1883", topic = "aq/broadcast", qos = 0 }

# Tier 3: external (optional)
# matrix = { enabled = false, homeserver = "https://matrix.example.com", room = "#aq:example.com" }
```

### 8.6 What Other Tools Do

**HashiCorp memberlist**: Delegate interface (section 2.2). Custom metadata
piggybacks on protocol messages. Transport is a separate interface
(`Transport` with `FinalAdvertiseAddr`, `WriteTo`, `PacketCh`, `StreamCh`).

**Terraform backends**: Pluggable state storage. Interface defines
`StateMgr()` returning a `state.State`. Implementations for local files, S3,
GCS, Consul, etc. Configuration via `backend "s3" { ... }` blocks in HCL.

**Grafana Loki / Prometheus**: Pluggable storage via `-storage.type` flag.
Implementations for filesystem, S3, GCS, DynamoDB, etc. Configuration via YAML.

**Docker logging drivers**: `--log-driver=json-file|syslog|journald|fluentd|...`
Each driver implements the same `Logger` interface. Selected at container
creation time.

The pattern is consistent: **define a minimal interface, implement filesystem
first, add network transports as optional plugins, configure via a single
config file.**

---

## 9. Transport Comparison Matrix

| Transport | Gossip Fit | Ops Overhead | FS-First | TTL | Tier | Best For |
|-----------|-----------|-------------|----------|-----|------|----------|
| **NDJSON files** | Good | Zero | Yes | App-level | 0 | Default. Local agents. |
| **SQLite WAL** | Adequate | Zero | Yes | SQL WHERE | 0-alt | Power users. Atomic queries. |
| **Unix datagram socket** | Excellent | Zero | Partial | No | 0.5 | Daemon-to-daemon IPC. |
| **D-Bus signals** | Good | Zero (Linux) | No | No | 0.5 | Linux-only deployments. |
| **mDNS / DNS-SD** | Perfect | Zero-Low | No | Native DNS TTL | 1 | Same-subnet discovery. |
| **9P / FUSE mount** | Good | Moderate | Yes | Inherited | 1 | Cross-machine via filesystem. |
| **Tailscale + NFS** | Adequate | Moderate | Yes | Inherited | 1 | VPN-connected machines. |
| **NATS** | Excellent | Low | No | JetStream only | 2 | Cross-machine gossip. |
| **MQTT (QoS 0)** | Excellent | Low-Mod | No | MQTT 5.0 | 2 | IoT-adjacent / existing infra. |
| **Redis pub/sub** | Good | Moderate | No | On keys, not msgs | 2 | Existing Redis deployments. |
| **ZeroMQ PUB/SUB** | Good | Low | No | No | 2 | Embedded / high-perf. |
| **IRC** | Good | Moderate | No | No | 2 | Teams with IRC infra. |
| **Matrix** | Moderate | High | No | No | 3 | Monitoring / human visibility. |
| **Nostr** | Good-Mod | Moderate | No | NIP-16 (limited) | 3 | Public / semi-public broadcast. |
| **ActivityPub** | Moderate | High | No | No | 3 | Federated / cross-org. |
| **SSB** | Excellent* | High | Partial | No (append-only) | 3 | Academic interest only. |
| **XMPP PEP** | Good | High | No | Presence-driven | 3 | Existing XMPP infra. |
| **Email (SMTP)** | Poor | Low | No | IMAP rules | Notify | Summary / digest reports. |
| **Kafka** | Poor | Very High | No | Topic retention | N/A | Instructive only. |

\* SSB's gossip fit is excellent in theory but the append-only model
contradicts aq's TTL-based expiry.

---

## 10. Recommendations

### 10.1 Tier 0: Keep NDJSON, Offer SQLite

NDJSON files in `~/.aq/channels/broadcast/requests/` remain the default. The
`cat`-debuggability is a real feature. Add SQLite WAL as an opt-in alternative
(`aq --storage=sqlite`) for users who want atomic queries and cleaner expiry.

### 10.2 Tier 0.5: Unix Datagram Sockets for the Daemon

When `aq watch` is running, it listens on `~/.aq/broadcast.sock` (Unix
datagram socket). Agents can send broadcasts to the socket for sub-millisecond
local notification AND write to filesystem for durability. The daemon writes
to filesystem on receipt, ensuring the two are always in sync.

### 10.3 Tier 1: mDNS for Same-Network Discovery

mDNS is the philosophical home of aq. Implement DNS-SD service registration
(`_aq._tcp`) with TXT records carrying the broadcast payload summary. Use DNS
TTL for automatic expiry. This enables same-subnet agents to discover each
other without shared filesystem access.

### 10.4 Tier 2: NATS for Cross-Machine Gossip

NATS is the recommended network transport. Reasons:
- Ephemeral by default (no persistence = gossip semantics)
- Subject wildcards map to channel structure
- Single binary, ~10MB, zero config
- Go client library is idiomatic (important for aq's Go port)
- JetStream available if users need persistence

### 10.5 The Implementation Order

1. **Define the `Channel` interface** (section 8.1). Refactor existing
   `protocol.py` / `main.go` to use it.
2. **Implement `FilesystemChannel`** as the reference implementation.
3. **Implement `MultiChannel`** for composition.
4. **Add config file parsing** (`~/.aq/config.toml`).
5. **Implement `NATSChannel`** as the first network transport.
6. **Implement `mDNSChannel`** for local network discovery.
7. **Everything else** based on user demand.

### 10.6 What Not to Build

- **Kafka transport**: aq is not a log processing system. Kafka's ordered,
  persistent, partitioned log model is the opposite of gossip.
- **SSB transport**: The append-only model contradicts TTL expiry.
- **Full Matrix/ActivityPub integration**: Too heavy. Build a bot/webhook
  instead.
- **Custom gossip protocol**: Do not reinvent SWIM/Serf. Use memberlist
  (Go) or equivalent if aq needs cluster membership. The broadcast channel
  and the membership protocol are separate concerns.

### 10.7 The Phi Accrual Insight

The single most valuable idea from this research for aq's immediate roadmap:
**replace hard TTL with adaptive expiry based on the phi accrual failure
detector model.** Instead of "this broadcast expires in 300 seconds," track
the inter-arrival time distribution of broadcasts from each agent. If the
gap since the last broadcast exceeds the expected interval by a statistically
significant amount, the agent is probably gone. This directly addresses the
TTL cliff problem and adapts automatically to different agent work patterns
(some announce every 30 seconds, some every 5 minutes).

Implementation sketch:
```python
import math
from collections import defaultdict

class PhiAccrual:
    def __init__(self, threshold: float = 8.0, window_size: int = 100):
        self.threshold = threshold
        self.windows: dict[str, list[float]] = defaultdict(list)
        self.last_seen: dict[str, float] = {}

    def record(self, agent: str, ts: float) -> None:
        if agent in self.last_seen:
            interval = ts - self.last_seen[agent]
            w = self.windows[agent]
            w.append(interval)
            if len(w) > 100:
                w.pop(0)
        self.last_seen[agent] = ts

    def phi(self, agent: str, now: float) -> float:
        if agent not in self.last_seen or not self.windows[agent]:
            return 0.0
        elapsed = now - self.last_seen[agent]
        intervals = self.windows[agent]
        mean = sum(intervals) / len(intervals)
        # Simplified: exponential distribution assumption
        return elapsed / mean if mean > 0 else float('inf')

    def is_expired(self, agent: str, now: float) -> bool:
        return self.phi(agent, now) > self.threshold
```

---

## 11. References

### Foundational Papers

- Demers, A., et al. "Epidemic Algorithms for Replicated Database
  Maintenance." PODC 1987, ACM.
  [PDF](https://www.cis.upenn.edu/~bcpierce/courses/dd/papers/demers-epidemic.pdf)

- Das, A., Gupta, I., Motivala, A. "SWIM: Scalable Weakly-consistent
  Infection-style Process Group Membership Protocol." DSN 2002, IEEE.
  [PDF](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf)

- Leitao, J., Pereira, J., Rodrigues, L. "Epidemic Broadcast Trees." SRDS
  2007, IEEE.
  [PDF](https://asc.di.fct.unl.pt/~jleitao/pdf/srds07-leitao.pdf)

- Hayashibara, N., et al. "The Phi Accrual Failure Detector." SRDS 2004, IEEE.

- Montresor, A. "Gossip and Epidemic Protocols." 2017.
  [PDF](http://disi.unitn.it/~montreso/ds/papers/montresor17.pdf)

### Protocol Specifications

- RFC 6762: Multicast DNS (mDNS)
- RFC 6763: DNS-Based Service Discovery (DNS-SD)
- RFC 1459 / RFC 2812: Internet Relay Chat (IRC)
- OASIS MQTT Version 5.0
- NATS Protocol: https://docs.nats.io/nats-protocol/nats-protocol
- W3C ActivityPub: https://www.w3.org/TR/activitypub/
- W3C WS-Eventing: https://www.w3.org/submissions/WS-Eventing/
- OASIS WS-BaseNotification: https://docs.oasis-open.org/wsn/wsn-ws_base_notification-1.3-spec-os.htm
- XEP-0163: Personal Eventing Protocol: https://xmpp.org/extensions/xep-0163.html
- Matrix Specification: https://spec.matrix.org/latest/
- Nostr NIP-01: https://github.com/nostr-protocol/nips/blob/master/01.md
- 9P Protocol: https://en.wikipedia.org/wiki/9P_(protocol)
- Scuttlebutt Protocol Guide: https://ssbc.github.io/scuttlebutt-protocol-guide/

### Implementations

- HashiCorp memberlist (Go): https://github.com/hashicorp/memberlist
- HashiCorp Serf: https://github.com/hashicorp/serf
- Consul Gossip Documentation: https://developer.hashicorp.com/consul/docs/architecture/gossip
- Cassandra Gossip: https://deepwiki.com/apache/cassandra/5.1-gossip-protocol
- Helium PlumTree (Erlang): https://github.com/helium/plumtree
- SSB Epidemic Broadcast Trees (JS): https://github.com/ssbc/epidemic-broadcast-trees
- Avahi (mDNS/DNS-SD): https://github.com/avahi/avahi

### aq Internal References

- `docs/WAVE-PROTOCOL.md` -- Wave protocol reconstruction and aq lineage
- `docs/DOGFOODING.md` -- Eight failures in 58 minutes
- `docs/PRESENTATION.org` -- Conference-style summary
- `src/aq/protocol.py` -- Current filesystem implementation
- `src/aq/conflict.py` -- Current conflict detection

### Secondary Sources

- Brian Storti, "SWIM: The scalable membership protocol":
  https://www.brianstorti.com/swim/
- Bartosz Sypytkowski, "Plumtree - epidemic broadcast trees":
  https://www.bartoszsypytkowski.com/plumtree/
- HashiCorp, "Everybody Talks: Gossip, Serf, memberlist, Raft, and SWIM":
  https://www.hashicorp.com/en/resources/everybody-talks-gossip-serf-memberlist-raft-swim-hashicorp-consul
- Digitalis, "Understanding phi_convict_threshold in Apache Cassandra":
  https://digitalis.io/post/understanding-phi-convict-threshold-in-apache-cassandra-a-deep-dive-into-failure-detection
- AxonOps, "Cassandra Gossip Protocol and Internode Messaging":
  https://axonops.com/docs/data-platforms/cassandra/architecture/cluster-management/gossip/
- ZeroMQ Guide, "Advanced Pub-Sub Patterns": https://zguide.zeromq.org/docs/chapter5/
- HiveMQ, "MQTT QoS Levels": https://www.hivemq.com/blog/mqtt-essentials-part-6-mqtt-quality-of-service-levels/
- Tailscale, "How Tailscale Works": https://tailscale.com/blog/how-tailscale-works
