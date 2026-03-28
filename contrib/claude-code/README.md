# Claude Code Integration for aq

Session lifecycle hooks for publishing aq presence to MQTT when using
Claude Code.

## Installation

```bash
# 1. Copy hook script
mkdir -p ~/.claude/hooks
cp aq-session-start.sh ~/.claude/hooks/
chmod +x ~/.claude/hooks/aq-session-start.sh

# 2. Configure Claude Code hooks
# Add to ~/.claude/settings.json:
```

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

```bash
# 3. Configure MQTT broker in ~/.aq/config.json:
```

```json
{
  "mqtt": {
    "enabled": true,
    "host": "localhost",
    "port": 1883,
    "topic": "aq"
  }
}
```

## What It Does

When a Claude Code session starts (or resumes), the hook publishes to MQTT:

```
Topic: aq/session/startup
Payload: {
  "session_id": "abc123",
  "project": "my-project",
  "cwd": "/home/user/projects/my-project",
  "source": "startup",
  "model": "claude-opus-4-5",
  "agent": "username",
  "hostname": "workstation",
  "ts": 1711636800
}
```

## Session Sources

The `source` field indicates the session lifecycle event:

| Source    | Meaning                    |
|-----------|----------------------------|
| `startup` | New session                |
| `resume`  | Continued existing session |
| `clear`   | `/clear` command           |
| `compact` | Auto/manual compaction     |

## Monitoring Sessions

Subscribe to all session events:

```bash
mosquitto_sub -h localhost -t 'aq/session/#' -v
```

Or specific events:

```bash
mosquitto_sub -h localhost -t 'aq/session/startup'
```

## Requirements

- `mosquitto_pub` (mosquitto-clients package)
- `jq` (optional, for JSON parsing — falls back to grep)

## Configuration

The hook reads from `~/.aq/config.json` and supports environment overrides:

| Variable        | Purpose            |
|-----------------|--------------------|
| `AQ_MQTT`       | Enable (1) / disable (0) |
| `AQ_MQTT_HOST`  | Broker hostname    |
| `AQ_MQTT_PORT`  | Broker port        |
| `AQ_MQTT_TOPIC` | Topic prefix       |

Environment variables take precedence over config file settings.
