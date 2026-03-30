# aq-mesh: Meshtastic/LoRa Transport Plugin

Standalone Go binary that bridges aq broadcasts to Meshtastic mesh radio.
Physical proximity becomes your subscription filter --- if your radio hears
it, it's probably your concern.

This is a Tier 3 transport. It does not replace the filesystem transport
(Tier 0). It adds a fanout layer for the specific case where "nearby" is a
useful approximation of "interested."

## Prerequisites

One of:

- **Serial mode**: `meshtastic` CLI installed, device connected via USB
- **MQTT mode**: `mosquitto_pub`/`mosquitto_sub` installed (from the Mosquitto package)

```bash
# macOS
brew install meshtastic mosquitto

# Debian/Ubuntu
apt install meshtastic mosquitto-clients
```

## Compact Wire Format

Meshtastic text payloads are limited to ~200 bytes. Full aq broadcasts are
300-500 bytes JSON. The compact format compresses to <=80 bytes:

```
1|<phase>|<agent_short>|<timestamp>|<status>|<conjecture>[|<files>]
```

| Field       | Encoding                                | Example          |
|-------------|----------------------------------------|------------------|
| version     | always `1`                             | `1`              |
| phase       | c=conjecture, p=proof, r=refutation, n=refinement | `p` |
| agent       | last 20 chars of agent address         | `jwalsh/feat-auth` |
| timestamp   | seconds since epoch                    | `1743321600`     |
| status      | a=prosecuting, d=done, b=blocked       | `a`              |
| conjecture  | conjecture ID                          | `C-42`           |
| files       | comma-separated basenames (optional)   | `api.py,auth.py` |

Example encoded payload (43 bytes):

```
1|p|jwalsh/feat-auth|1743321600|a|C-42|api.py,auth.py
```

## Usage

### Publish (TX)

Send a broadcast over Meshtastic:

```bash
# Via MQTT bridge (default, no hardware required)
go run mesh.go -publish \
    -agent jwalsh/feat-auth \
    -conjecture C-42 \
    -phase proof \
    -files "api.py,auth.py" \
    -mqtt-host mqtt.meshtastic.org

# Via serial (requires Meshtastic device on USB)
go run mesh.go -publish \
    -via serial \
    -port /dev/cu.usbmodem1101 \
    -channel 1 \
    -agent jwalsh/feat-auth \
    -conjecture C-42 \
    -phase proof \
    -files "api.py,auth.py"
```

### Subscribe (RX)

Listen for mesh broadcasts and write them as aq JSON to the local filesystem:

```bash
# Listen via MQTT bridge
go run mesh.go -subscribe \
    -mqtt-host mqtt.meshtastic.org
```

Received broadcasts are decoded from compact format and written as full
Broadcast JSON to `~/.aq/channels/broadcast/requests/aq-<ulid>.json`.
These are then visible to `aq status` and `aq check` on the local machine.

## Flags

| Flag          | Default                | Description                                |
|---------------|------------------------|--------------------------------------------|
| `-publish`    | false                  | TX mode: send a compact payload            |
| `-subscribe`  | false                  | RX mode: listen and write aq-*.json        |
| `-via`        | `mqtt`                 | Transport: `serial` or `mqtt`              |
| `-port`       | `/dev/ttyUSB0`         | Serial port for Meshtastic device          |
| `-mqtt-host`  | `mqtt.meshtastic.org`  | MQTT broker host                           |
| `-channel`    | `0`                    | Meshtastic channel index                   |
| `-agent`      | (required for publish) | Agent address (e.g., `jwalsh/feat-auth`)   |
| `-conjecture` | `C-0`                  | Conjecture ID                              |
| `-claim`      | (empty)                | Conjecture claim / intent                  |
| `-phase`      | `conjecture`           | CPRR phase                                 |
| `-status`     | `prosecuting`          | Broadcast status                           |
| `-files`      | (empty)                | Comma-separated file list                  |

## Architecture

```
aq announce --mesh
      |
      v
  mesh.go -publish
      |
      +-- -via serial --> meshtastic --sendtext '<compact>' --ch-index N
      |
      +-- -via mqtt ----> mosquitto_pub -h <host> -t 'msh/...' -m '<compact>'


  mesh.go -subscribe
      |
      v
  mosquitto_sub -h <host> -t 'msh/US/2/+/+'
      |
      v
  compact decode --> full Broadcast JSON
      |
      v
  ~/.aq/channels/broadcast/requests/aq-<ulid>.json
      |
      v
  aq status / aq check (reads local filesystem)
```

## Design Constraints

- **stdlib only**: no external Go dependencies. Shells out to CLI tools.
- **`//go:build ignore`**: not compiled into the main aq binary.
- **best-effort**: a failed mesh broadcast never blocks `aq announce`.
- **compact wire format**: <=80 bytes target, <=200 bytes hard limit.
- **basenames only**: file paths are stripped to basenames before TX. No
  full paths, file contents, diffs, or secrets cross the radio.

## Relation to spec.org

See `spec.org` in this directory for the full Meshtastic bridge protocol
specification, including security considerations, failure taxonomy,
deployment topology, and collaborator onboarding.
