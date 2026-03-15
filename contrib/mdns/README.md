# mDNS Transport for aq

> mDNS is the philosophical home of aq.
>
> -- [TRANSPORT-RESEARCH.md, Section 10.3](../../docs/TRANSPORT-RESEARCH.md)

aq's entire design was inspired by mDNS/DNS-SD. From CLAUDE.md:

> aq is the mDNS of multi-agent development: broadcast "does anyone know
> this address?", cost near zero, benefit when someone does know is
> conflict avoidance before the merge wall. No orchestrator, no broker,
> no authority. Just gossip.

This directory contains a working demonstration of aq broadcasts over
mDNS/DNS-SD. It is a Tier 1 transport -- the filesystem (Tier 0) remains
the only *required* transport, but mDNS extends aq's reach from a single
machine to every machine on the local network, with zero configuration
and zero infrastructure.

---

## 1. How mDNS Maps to aq

The mapping between DNS-SD concepts and aq concepts is not an analogy.
It is a direct structural correspondence:

| DNS-SD Concept       | aq Concept           | Example                                        |
|----------------------|----------------------|------------------------------------------------|
| Service type         | Channel              | `_aq._tcp`                                     |
| Instance name        | Agent address        | `github.com/jwalsh/aq/feat-auth`               |
| TXT records          | Broadcast payload    | `conjecture=C-1 phase=proof files=auth.py`     |
| DNS TTL              | Broadcast TTL        | 300s (default), 60s (whisper)                   |
| Service registration | `aq announce`        | Agent starts working                           |
| Service browsing     | `aq status`          | Peer checks who else is active                 |
| Service deregistration | `status=done`      | Agent finishes, record expires                 |
| Multicast group      | Broadcast channel    | `224.0.0.251:5353` (all peers on subnet)       |

### Service Type

All aq broadcasts use the DNS-SD service type:

```
_aq._tcp
```

This follows RFC 6763 naming conventions. The `_tcp` suffix is required
by DNS-SD even though aq does not use TCP connections -- the service type
identifies the *application protocol*, and DNS-SD mandates either `_tcp`
or `_udp` as the transport suffix.

### Instance Name

The instance name is the agent's address -- the same `{remote}/{branch}`
or worktree identifier used in filesystem broadcasts:

```
github.com/jwalsh/aq/feat-auth
```

Each agent registers exactly one service instance. Multiple conjectures
from the same agent update the TXT records on the same instance.

### TXT Records

The broadcast payload is carried in DNS TXT records. Each key-value pair
is a separate TXT string (per RFC 6763 Section 6):

| TXT Key        | aq Field           | Example Value                   |
|----------------|--------------------|---------------------------------|
| `conjecture`   | `conjecture_id`    | `C-1`                           |
| `claim`        | `conjecture_claim` | `filesystem transport is sufficient` |
| `phase`        | `phase`            | `proof`                         |
| `status`       | `status`           | `prosecuting`                   |
| `files`        | `files`            | `auth.py,session.py`            |
| `worktree`     | `worktree`         | `feat-auth`                     |
| `id`           | `id`               | `018f3a...`                     |

DNS TXT records have a 255-byte-per-string limit, with multiple strings
allowed up to roughly 1300 bytes total. The aq broadcast payload fits
comfortably within this limit. The `files` field uses comma separation
rather than a JSON array to stay compact.

### TTL Mapping

DNS record TTL maps directly to aq broadcast TTL:

- **Default TTL**: 300 seconds (5 minutes). Standard working session.
- **Whisper TTL**: 60 seconds. Short-lived broadcasts for transient state.

When the TTL expires and the agent does not re-announce, the mDNS record
disappears from the network. This is exactly aq's expiry semantics --
silence means the agent stopped. No explicit deregistration required,
though agents SHOULD deregister on `status=done` for faster cleanup.

---

## 2. What Happens on the Network

### Step-by-step flow

```
                    LAN (224.0.0.251:5353)
                    ~~~~~~~~~~~~~~~~~~~~~~

Machine A                                    Machine B
(Agent: origin/feat-auth)                    (Agent: origin/feat-session)

1. Agent A starts working on auth.py
   |
   +---> mDNS ANNOUNCE (multicast)
         Service: _aq._tcp
         Instance: github.com/jwalsh/aq/feat-auth
         TXT: conjecture=C-1 phase=proof
              files=auth.py,session.py
              status=prosecuting
         TTL: 300
                         |
                         +---> 2. Agent B is browsing _aq._tcp
                                  (continuous discovery)
                                  |
                                  +---> Sees Agent A's broadcast
                                  |
                                  +---> 3. Conflict check:
                                        Agent B plans to touch auth.py
                                        Agent A is in phase=proof on auth.py
                                        CONFLICT: HIGH (both proof, shared file)
                                        |
                                        +---> Agent B adjusts plan
                                              or waits for Agent A

4. Agent A finishes
   |
   +---> mDNS UPDATE (multicast)
         TXT: status=done
         TTL: 0 (or deregister)
                         |
                         +---> 5. Agent B sees status=done
                                  Conflict cleared.
                                  Agent B proceeds with auth.py
```

### Key properties

- **Zero configuration**: No server to set up. No ports to open. No
  discovery service to run. mDNS uses well-known multicast address
  `224.0.0.251` on port 5353.

- **Zero infrastructure**: No broker, no registry, no coordinator. Every
  machine with mDNS support (all macOS machines, all Linux machines with
  Avahi) participates automatically.

- **Graceful degradation**: If multicast is blocked (corporate firewalls,
  VPNs, containers), aq falls back to filesystem-only. The filesystem
  transport is always present. mDNS is an amplifier, not a dependency.

- **No obligation**: Just like filesystem broadcasts, mDNS broadcasts
  carry no obligation. An agent that does not browse `_aq._tcp` simply
  does not see the broadcasts. Silence is normal.

- **Subnet-scoped**: mDNS multicast does not cross routers. This is a
  feature, not a limitation -- it matches the "team office LAN" use case
  where developers are on the same network.

---

## 3. macOS: Using Bonjour (built-in)

macOS has Bonjour built in. The `dns-sd` command-line tool is available
on every Mac without installing anything.

### Register a broadcast

```bash
# Agent announces: working on C-1, proof phase, touching auth.py and session.py
dns-sd -R \
  "github.com/jwalsh/aq/feat-auth" \
  "_aq._tcp" \
  "local" \
  0 \
  conjecture=C-1 \
  claim="filesystem transport is sufficient" \
  phase=proof \
  status=prosecuting \
  files=auth.py,session.py \
  worktree=feat-auth
```

Arguments:
- `-R` -- Register a service
- `"github.com/jwalsh/aq/feat-auth"` -- Instance name (agent address)
- `"_aq._tcp"` -- Service type
- `"local"` -- Domain (local network)
- `0` -- Port (not used by aq, but required by DNS-SD; 0 means no port)
- Everything after the port is key=value TXT records

The command runs in the foreground and keeps the registration alive.
When the process exits, the service is deregistered. This maps perfectly
to aq's lifecycle: the broadcast lives as long as the agent is working.

### Browse for broadcasts

In another terminal:

```bash
# Discover all aq agents on the network
dns-sd -B _aq._tcp local
```

Output looks like:

```
Browsing for _aq._tcp.local
DATE: ---Mon 14 Mar 2026---
 3:42:15.123  ...DIFFERING_FLAGS...  Add  2  4  local.  _aq._tcp.  github.com/jwalsh/aq/feat-auth
 3:42:16.456  ...DIFFERING_FLAGS...  Add  2  4  local.  _aq._tcp.  github.com/jwalsh/aq/feat-session
```

### Look up broadcast details

```bash
# Get the TXT records (broadcast payload) for a specific agent
dns-sd -L "github.com/jwalsh/aq/feat-auth" "_aq._tcp" "local"
```

Output:

```
Lookup github.com/jwalsh/aq/feat-auth._aq._tcp.local
DATE: ---Mon 14 Mar 2026---
 3:42:20.789  github.com/jwalsh/aq/feat-auth._aq._tcp.local. can be reached at myhost.local.:0
 conjecture=C-1 claim=filesystem\ transport\ is\ sufficient phase=proof status=prosecuting files=auth.py,session.py worktree=feat-auth
```

### Resolve the host

```bash
# Optional: resolve the hostname to an IP address
dns-sd -G v4 myhost.local
```

### Full demo sequence

```bash
# Terminal 1: Start browsing (leave running)
dns-sd -B _aq._tcp local

# Terminal 2: Register Agent A
dns-sd -R "origin/feat-auth" "_aq._tcp" "local" 0 \
  conjecture=C-1 phase=proof status=prosecuting files=auth.py

# Terminal 3: Register Agent B (on same or different machine)
dns-sd -R "origin/feat-session" "_aq._tcp" "local" 0 \
  conjecture=C-7 phase=proof status=prosecuting files=auth.py

# Terminal 1 now shows both agents.
# A human or script reading this sees: both agents in proof phase
# on auth.py = HIGH conflict severity.

# Terminal 4: Look up details
dns-sd -L "origin/feat-auth" "_aq._tcp" "local"
dns-sd -L "origin/feat-session" "_aq._tcp" "local"

# When Agent A finishes, Ctrl-C Terminal 2.
# Terminal 1 shows the service removed. Conflict cleared.
```

---

## 4. Linux: Using Avahi

Linux systems use Avahi, the open-source mDNS/DNS-SD implementation.
Most distributions include it by default.

### Install (if needed)

```bash
# Debian/Ubuntu
sudo apt install avahi-daemon avahi-utils

# Fedora/RHEL
sudo dnf install avahi avahi-tools

# Arch
sudo pacman -S avahi
```

### Register a broadcast

```bash
# Register an aq broadcast via Avahi
avahi-publish-service \
  "github.com/jwalsh/aq/feat-auth" \
  "_aq._tcp" \
  0 \
  "conjecture=C-1" \
  "claim=filesystem transport is sufficient" \
  "phase=proof" \
  "status=prosecuting" \
  "files=auth.py,session.py" \
  "worktree=feat-auth"
```

Note: Avahi requires each TXT record string to be quoted separately,
unlike `dns-sd` where they are bare arguments.

### Browse for broadcasts

```bash
# One-shot browse
avahi-browse -t _aq._tcp

# Continuous browse (like dns-sd -B)
avahi-browse _aq._tcp

# Resolve immediately (show TXT records and addresses)
avahi-browse -r _aq._tcp
```

### Look up details

```bash
# Resolve a specific service (shows TXT records, hostname, port)
avahi-resolve-host-name myhost.local
```

### Full demo sequence

```bash
# Terminal 1: Browse continuously with resolution
avahi-browse -r _aq._tcp

# Terminal 2: Register Agent A
avahi-publish-service "origin/feat-auth" "_aq._tcp" 0 \
  "conjecture=C-1" "phase=proof" "status=prosecuting" "files=auth.py"

# Terminal 3: Register Agent B
avahi-publish-service "origin/feat-session" "_aq._tcp" 0 \
  "conjecture=C-7" "phase=proof" "status=prosecuting" "files=auth.py"

# Terminal 1 shows both services with full TXT records.
# Conflict detection: both proof phase on auth.py = HIGH.

# Ctrl-C Terminal 2 to deregister Agent A. Conflict cleared.
```

---

## 5. Cross-Platform Interoperability

Bonjour and Avahi implement the same RFCs (6762, 6763). A macOS machine
running `dns-sd -R` and a Linux machine running `avahi-browse` on the
same LAN will see each other's aq broadcasts without any configuration.

This is the zero-configuration property that makes mDNS the philosophical
home of aq.

---

## 6. Conflict Detection Over mDNS

Conflict detection over mDNS follows the same logic as filesystem-based
conflict detection, using the same CPRR phase severity matrix:

| Agent A Phase | Agent B Phase | Shared Files? | Severity |
|---------------|---------------|---------------|----------|
| conjecture    | conjecture    | yes           | LOW      |
| conjecture    | proof         | yes           | MEDIUM   |
| proof         | proof         | yes           | HIGH     |
| proof         | refutation    | yes           | MEDIUM   |
| refutation    | refutation    | yes           | LOW      |

The only difference is the transport: instead of reading NDJSON files
from `~/.aq/channels/broadcast/`, the conflict checker reads TXT records
from discovered mDNS services.

---

## 7. Limitations

- **Subnet-scoped**: mDNS multicast does not cross routers. For
  cross-network discovery, use Tier 2 transports (NATS, etc.).
- **Same-machine overhead**: For agents on the same machine, filesystem
  transport is simpler and faster. mDNS adds value only when agents
  are on different machines.
- **TXT record size**: 255 bytes per string, ~1300 bytes total. The aq
  payload fits, but very long file lists may need truncation.
- **Firewall**: Some corporate networks block mDNS multicast. aq
  degrades gracefully to filesystem-only.

---

## 8. Files in This Directory

| File        | Description                                                |
|-------------|------------------------------------------------------------|
| `README.md` | This file. Comprehensive explanation of mDNS for aq.      |
| `mdns.go`   | Standalone Go example using `hashicorp/mdns`. `//go:build ignore`. |
| `demo.sh`   | Shell script demo using macOS `dns-sd`. Runnable immediately. |

---

## 9. References

- RFC 6762: Multicast DNS -- https://www.rfc-editor.org/rfc/rfc6762
- RFC 6763: DNS-Based Service Discovery -- https://www.rfc-editor.org/rfc/rfc6763
- Apple Bonjour -- https://developer.apple.com/bonjour/
- Avahi -- https://avahi.org/
- hashicorp/mdns (Go library) -- https://github.com/hashicorp/mdns
- aq Transport Research -- [../../docs/TRANSPORT-RESEARCH.md](../../docs/TRANSPORT-RESEARCH.md) Section 3.7 and 10.3
