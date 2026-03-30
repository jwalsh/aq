# UDP Multicast Transport for aq

Tier 0.5 transport -- true LAN gossip via UDP multicast. No broker, no
discovery, no pairing. Sub-millisecond latency. The network primitive
that mDNS, SWIM, Serf, and Consul LAN gossip are all built on top of.

## How it works

A sender writes a framed JSON datagram to a multicast group address.
Every process on the LAN that has joined that group receives it. The
kernel does the fanout. That's the entire protocol.

Wire format: 4-byte header (`AQ` magic + version + format byte) followed
by the JSON-encoded Broadcast payload. Always fits in a single
unfragmented UDP datagram (< 1500 bytes).

## Requirements

- Go 1.21+ (stdlib only, no external dependencies)
- Network with multicast support (any LAN, Docker `--network=host`)

## Quick start

```bash
# Terminal 1: start a subscriber
go run udp.go -subscribe

# Terminal 2: send a broadcast
go run udp.go -publish -agent jwalsh/main -conjecture C-1 \
    -phase proof -claim "testing UDP multicast transport" \
    -files "protocol.py,cli.py"
```

The subscriber writes received broadcasts to
`~/.aq/channels/broadcast/requests/aq-{ts}-{id}.json`.

## Flags

### Mode (required, pick one)

| Flag         | Description                                       |
|--------------|---------------------------------------------------|
| `-publish`   | TX: send a single broadcast to the multicast group |
| `-subscribe` | RX: listen and write received broadcasts to disk  |

### Multicast configuration

| Flag    | Default          | Description                          |
|---------|------------------|--------------------------------------|
| `-group`| `239.192.65.81`  | IPv4 multicast group address         |
| `-port` | `4181`           | UDP port                             |
| `-iface`| (all interfaces) | Network interface to bind             |
| `-ttl`  | `1`              | Multicast TTL hops (1 = LAN only)   |

### Broadcast content (publish mode)

| Flag              | Default        | Description                    |
|-------------------|----------------|--------------------------------|
| `-agent`          | (required)     | Agent address, e.g. `jwalsh/main` |
| `-conjecture`     | `C-0`          | Conjecture ID                  |
| `-claim`          | (empty)        | Human-readable intent          |
| `-phase`          | `conjecture`   | CPRR phase                     |
| `-status`         | `prosecuting`  | Broadcast status               |
| `-files`          | (empty)        | Comma-separated file list      |
| `-broadcast-ttl`  | `3600`         | Broadcast TTL in seconds       |

### Self-exclusion (subscribe mode)

Pass `-agent` in subscribe mode to filter out your own broadcasts:

```bash
go run udp.go -subscribe -agent jwalsh/main
```

## Examples

### Two agents on the same LAN

```bash
# Machine A (nexus): subscribe, filtering own broadcasts
go run udp.go -subscribe -agent agent-a/feat-auth

# Machine B (mini): subscribe
go run udp.go -subscribe -agent agent-b/fix-api

# Machine A: announce
go run udp.go -publish -agent agent-a/feat-auth \
    -conjecture C-2 -phase proof -files "auth.py"

# Machine B sees the broadcast, writes aq-*.json locally.
# Machine A's subscriber filters it (self-exclusion).
```

### Specific interface

```bash
# Bind to en0 only (useful with multiple NICs or VPNs)
go run udp.go -subscribe -iface en0
go run udp.go -publish -iface en0 -agent jwalsh/main -conjecture C-1
```

### Cross-subnet (TTL > 1)

```bash
# Allow datagrams to cross one router hop
go run udp.go -subscribe -ttl 2
go run udp.go -publish -ttl 2 -agent jwalsh/main -conjecture C-1
```

## Design decisions

- **Framed JSON** over raw compact: 4 bytes of overhead buys magic-byte
  filtering, version evolution, and format negotiation. Per the spec
  (section 3.3), framed is canonical.

- **In-memory dedup** over filesystem scan: the IRC transport scans the
  requests directory for duplicates. At multicast rates (potentially
  hundreds of datagrams/second), filesystem scan is too slow. The
  in-memory cache is bounded to 1024 entries with 5-minute expiry.

- **Self-exclusion by agent address** over `IP_MULTICAST_LOOP=0`:
  loopback is left enabled (the default) so the sender can verify its
  own datagrams arrive. Self-exclusion happens at the application layer
  using the agent address, which also handles the case where multiple
  agents run on the same machine.

- **`239.192.65.81`** as default group: "AQ" is 0x41 0x51 in ASCII.
  The `239.192.0.0/14` range is Organization-Local Scope per RFC 2365.
  This address will not be routed beyond the site. Override with `-group`
  for your network.

## Transport hierarchy

```
Tier 0:   Filesystem (~/.aq/)         -- single machine, zero deps
Tier 0.5: UDP Multicast (239.192.65.81) -- LAN, zero deps, zero broker
Tier 1:   mDNS (_aq._tcp)            -- LAN, zero-config discovery
Tier 1.5: IRC / NATS                 -- text protocol / light broker
Tier 2:   MQTT / Keybase             -- broker / E2E encrypted
Tier 3:   Meshtastic (LoRa)          -- radio, proximity-scoped
```
