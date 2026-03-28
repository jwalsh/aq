# aq Configuration

aq reads configuration from `~/.aq/config.json`. All transports are disabled
by default and must be explicitly enabled.

## Configuration File

```json
{
  "mqtt": {
    "enabled": true,
    "host": "localhost",
    "port": 1883,
    "topic": "aq"
  },
  "mdns": {
    "enabled": false,
    "service_type": "_aq._tcp",
    "domain": "local"
  },
  "mesh": {
    "enabled": false,
    "via": "serial"
  }
}
```

### MQTT Section

| Key       | Type    | Default     | Description                              |
|-----------|---------|-------------|------------------------------------------|
| `enabled` | boolean | `false`     | Enable MQTT publishing on every announce |
| `host`    | string  | `localhost` | MQTT broker hostname                     |
| `port`    | integer | `1883`      | MQTT broker port                         |
| `topic`   | string  | `aq`        | Topic prefix for all aq messages         |

Topics published:
- `{topic}/announce` — agent broadcasts
- `{topic}/session/{startup|resume|clear}` — session lifecycle (Claude Code hook)
- `{topic}/conflict` — conflict alerts (future)

### mDNS Section

| Key            | Type    | Default      | Description                    |
|----------------|---------|--------------|--------------------------------|
| `enabled`      | boolean | `false`      | Enable mDNS service discovery  |
| `service_type` | string  | `_aq._tcp`   | Service type to advertise      |
| `domain`       | string  | `local`      | mDNS domain                    |

### Mesh Section

| Key       | Type    | Default  | Description                          |
|-----------|---------|----------|--------------------------------------|
| `enabled` | boolean | `false`  | Enable Meshtastic mesh broadcast     |
| `via`     | string  | `serial` | Transport: `serial` or `mqtt`        |

## Environment Overrides

Environment variables take precedence over config file settings:

| Variable        | Overrides                |
|-----------------|--------------------------|
| `AQ_MQTT`       | `mqtt.enabled` (1 = on)  |
| `AQ_MQTT_HOST`  | `mqtt.host`              |
| `AQ_MQTT_PORT`  | `mqtt.port`              |
| `AQ_MQTT_TOPIC` | `mqtt.topic`             |
| `AQ_MESH`       | `mesh.enabled` (1 = on)  |

## Claude Code Integration

aq can announce session starts via MQTT when used with Claude Code. This
enables monitoring of agent activity across multiple machines.

### Setup

1. Install the session hook:

```bash
mkdir -p ~/.claude/hooks
# Copy the hook script (see contrib/claude-code/aq-session-start.sh)
chmod +x ~/.claude/hooks/aq-session-start.sh
```

2. Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "~/.claude/hooks/aq-session-start.sh"
          }
        ]
      }
    ]
  }
}
```

3. Configure MQTT in `~/.aq/config.json` (see above).

### Session Payload

The hook publishes JSON to `{topic}/session/{source}`:

```json
{
  "session_id": "abc123",
  "project": "aq",
  "cwd": "/home/user/projects/aq",
  "source": "startup",
  "model": "claude-opus-4-5",
  "agent": "username",
  "hostname": "workstation",
  "ts": 1711636800
}
```

Where `source` is one of:
- `startup` — new session
- `resume` — continued session
- `clear` — session cleared
- `compact` — session compacted

## Sample Configuration

See `contrib/config.sample.json` for a template with all options disabled.
Copy to `~/.aq/config.json` and enable the transports you need.
