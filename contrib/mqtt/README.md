# MQTT Transport for aq

MQTT is gossip over a wire. QoS 0 (fire-and-forget) is literally the
network-protocol encoding of aq's foundational axiom: broadcast presence,
no guaranteed delivery, no obligation to listen.

## Why MQTT Fits aq

| MQTT Concept     | aq Concept           | Why it maps                                    |
|------------------|----------------------|------------------------------------------------|
| QoS 0            | Gossip semantics     | Fire-and-forget. Lossy is fine. No ACKs.       |
| RETAIN flag      | `aq status`          | New subscribers see last known state instantly. |
| Topic hierarchy  | Channel / repo / branch | `aq/{repo}/{branch}/presence`               |
| Last Will (LWT)  | Auto `status=done`   | Broker publishes will on unclean disconnect.   |
| Topic wildcards   | `aq status --all`    | `aq/+/+/presence` sees every repo and branch.  |

### QoS 0 = Gossip

MQTT defines three quality-of-service levels. Only QoS 0 matters for aq:

- **QoS 0**: At most once. No ACK. Message may be lost. This is gossip.
- **QoS 1**: At least once. Requires ACK. This is coordination.
- **QoS 2**: Exactly once. Two-phase commit. This is a transaction.

aq is gossip, not coordination. QoS 0 is the only correct choice.

### RETAIN = Presence for Latecomers

When an agent publishes with `RETAIN=true`, the broker stores the message.
Any new subscriber immediately receives the last retained message on that
topic. This solves the "I just started, who else is working?" problem --
equivalent to `aq status` reading the filesystem, but over the network.

### Last Will = Solving the Heartbeat Problem

MQTT Last Will and Testament (LWT) is set at connection time. If the
client disconnects unexpectedly (crash, network loss, kill -9), the
broker publishes the will message on behalf of the dead client. For aq,
this means: set a will message of `{"status": "done"}` and agent
disconnects are automatically announced. No heartbeat daemon needed.
This is evidence relevant to C-7 (auto-renewal / heartbeat).

### Topic Hierarchy

```
aq/{repo}/{branch}/presence    -- per-branch presence
aq/{repo}/+/presence           -- all branches in a repo
aq/+/+/presence                -- all repos (global view)
```

### The Meshtastic Connection

The user already runs MQTT for Meshtastic mesh networking
(`mqtt.meshtastic.org`, seabord channel). The same broker infrastructure
can carry aq presence alongside mesh device telemetry. Agents on mesh
network devices (Raspberry Pi, ESP32) could gossip their presence via
the MQTT bridge -- aq broadcasts reaching devices with no filesystem
access, no IP stack, just LoRa radio + MQTT uplink.

Mosquitto is tiny (<1MB), runs on FreeBSD, macOS, Linux, Raspberry Pi,
and OpenWrt routers. It is the smallest viable bridge between filesystem
gossip and network gossip.

## References

- MQTT 3.1.1 Specification: https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html
- Eclipse Mosquitto: https://mosquitto.org/
- Eclipse Paho (Go client): https://github.com/eclipse/paho.mqtt.golang
- Meshtastic MQTT: https://meshtastic.org/docs/configuration/module/mqtt/
