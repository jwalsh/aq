#!/bin/sh
# Claude Code session start hook — announce session via MQTT
#
# Install:
#   mkdir -p ~/.claude/hooks
#   cp aq-session-start.sh ~/.claude/hooks/
#   chmod +x ~/.claude/hooks/aq-session-start.sh
#
# Then add to ~/.claude/settings.json:
#   {
#     "hooks": {
#       "SessionStart": [{
#         "matcher": "",
#         "hooks": [{"type": "command", "command": "~/.claude/hooks/aq-session-start.sh"}]
#       }]
#     }
#   }
#
# Configure MQTT in ~/.aq/config.json:
#   { "mqtt": { "enabled": true, "host": "localhost", "port": 1883, "topic": "aq" } }
#
# Expected stdin (JSON):
# {
#   "session_id": "abc123",
#   "transcript_path": "/path/to/transcript.jsonl",
#   "cwd": "/current/working/directory",
#   "hook_event_name": "SessionStart",
#   "source": "startup",
#   "model": "claude-sonnet-4-6"
# }

set -e

# Read stdin
INPUT=$(cat)

# Extract fields with jq (if available) or grep fallback
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"')
    CWD=$(echo "$INPUT" | jq -r '.cwd // "unknown"')
    SOURCE=$(echo "$INPUT" | jq -r '.source // "startup"')
    MODEL=$(echo "$INPUT" | jq -r '.model // "unknown"')
else
    # Basic grep fallback
    SESSION_ID=$(echo "$INPUT" | grep -o '"session_id":"[^"]*"' | cut -d'"' -f4 || echo "unknown")
    CWD=$(echo "$INPUT" | grep -o '"cwd":"[^"]*"' | cut -d'"' -f4 || echo "unknown")
    SOURCE=$(echo "$INPUT" | grep -o '"source":"[^"]*"' | cut -d'"' -f4 || echo "startup")
    MODEL=$(echo "$INPUT" | grep -o '"model":"[^"]*"' | cut -d'"' -f4 || echo "unknown")
fi

# Load config from ~/.aq/config.json
AQ_CONFIG="$HOME/.aq/config.json"
if [ -f "$AQ_CONFIG" ] && command -v jq >/dev/null 2>&1; then
    MQTT_ENABLED=$(jq -r '.mqtt.enabled // false' "$AQ_CONFIG")
    MQTT_HOST=$(jq -r '.mqtt.host // "localhost"' "$AQ_CONFIG")
    MQTT_PORT=$(jq -r '.mqtt.port // 1883' "$AQ_CONFIG")
    MQTT_TOPIC=$(jq -r '.mqtt.topic // "aq"' "$AQ_CONFIG")
else
    # Defaults
    MQTT_ENABLED="false"
    MQTT_HOST="localhost"
    MQTT_PORT="1883"
    MQTT_TOPIC="aq"
fi

# Environment overrides
[ -n "$AQ_MQTT" ] && MQTT_ENABLED="$AQ_MQTT"
[ -n "$AQ_MQTT_HOST" ] && MQTT_HOST="$AQ_MQTT_HOST"
[ -n "$AQ_MQTT_PORT" ] && MQTT_PORT="$AQ_MQTT_PORT"
[ -n "$AQ_MQTT_TOPIC" ] && MQTT_TOPIC="$AQ_MQTT_TOPIC"

# Skip if MQTT not enabled
if [ "$MQTT_ENABLED" != "true" ] && [ "$MQTT_ENABLED" != "1" ]; then
    exit 0
fi

# Check mosquitto_pub is available
if ! command -v mosquitto_pub >/dev/null 2>&1; then
    exit 0
fi

# Extract project name from cwd
PROJECT=$(basename "$CWD")
HOSTNAME=$(hostname)
USER=${USER:-$(whoami)}
TIMESTAMP=$(date +%s)

# Build payload
PAYLOAD=$(cat <<EOF
{
  "session_id": "$SESSION_ID",
  "project": "$PROJECT",
  "cwd": "$CWD",
  "source": "$SOURCE",
  "model": "$MODEL",
  "agent": "$USER",
  "hostname": "$HOSTNAME",
  "ts": $TIMESTAMP
}
EOF
)

# Publish to MQTT (best-effort, don't fail the session)
mosquitto_pub \
    -h "$MQTT_HOST" \
    -p "$MQTT_PORT" \
    -t "${MQTT_TOPIC}/session/$SOURCE" \
    -m "$PAYLOAD" \
    2>/dev/null || true

exit 0
