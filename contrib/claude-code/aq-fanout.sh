#!/bin/sh
# aq-fanout.sh — fan out aq broadcast to all enabled transports
#
# Called as a CC hook on Stop/PostToolUse events.
# Writes to: filesystem (always), MQTT (if broker reachable), mesh (if device connected),
#            ggwave (if sounddevice available — audible chirp)
#
# Install:
#   cp contrib/claude-code/aq-fanout.sh ~/.claude/hooks/
#   chmod +x ~/.claude/hooks/aq-fanout.sh

set -e

# --- Config ---
AQ_CONFIG="$HOME/.aq/config.json"
AQ_DIR="$HOME/.aq/channels/broadcast/requests"
MESH_PORT="${AQ_MESH_PORT:-/dev/cu.usbmodem3101}"

# Read CC hook stdin
INPUT=$(cat)

# Extract fields
if command -v jq >/dev/null 2>&1; then
    CWD=$(echo "$INPUT" | jq -r '.cwd // "unknown"')
    SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"')
else
    CWD=$(echo "$INPUT" | grep -o '"cwd":"[^"]*"' | cut -d'"' -f4 || echo "unknown")
    SESSION_ID="unknown"
fi

# Detect agent/branch from git
BRANCH=$(git -C "$CWD" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "main")
PROJECT=$(basename "$CWD")
USER=${USER:-$(whoami)}
AGENT="${USER}/${BRANCH}"
TS=$(date +%s)

# Build AMTP compact payload (fits in mesh 200-byte limit)
AMTP="aq:${USER:0:2}/${BRANCH:0:1} C-1 [p] ${PROJECT}"

# Build full JSON broadcast
PAYLOAD=$(cat <<EOF
{"agent":"${AGENT}","worktree":"${BRANCH}","conjecture_id":"C-1","phase":"proof","status":"prosecuting","project":"${PROJECT}","session":"${SESSION_ID}","ts":${TS},"ttl":3600}
EOF
)

# --- Transport 1: Filesystem (always) ---
mkdir -p "$AQ_DIR"
FILENAME="aq-$(date +%Y%m%d%H%M%S)-$(echo "$TS" | tail -c 5).json"
echo "$PAYLOAD" > "$AQ_DIR/$FILENAME" 2>/dev/null || true

# --- Transport 2: MQTT (if enabled + reachable) ---
if [ -f "$AQ_CONFIG" ] && command -v jq >/dev/null 2>&1; then
    MQTT_ENABLED=$(jq -r '.mqtt.enabled // false' "$AQ_CONFIG")
    MQTT_HOST=$(jq -r '.mqtt.host // "localhost"' "$AQ_CONFIG")
    MQTT_PORT=$(jq -r '.mqtt.port // 1883' "$AQ_CONFIG")
    MQTT_TOPIC=$(jq -r '.mqtt.topic // "aq"' "$AQ_CONFIG")
fi

if [ "$MQTT_ENABLED" = "true" ] && command -v mosquitto_pub >/dev/null 2>&1; then
    mosquitto_pub \
        -h "$MQTT_HOST" \
        -p "${MQTT_PORT:-1883}" \
        -t "${MQTT_TOPIC:-aq}/announce/${PROJECT}" \
        -m "$PAYLOAD" \
        2>/dev/null || true
fi

# --- Transport 3: Mesh via e196 (if connected) ---
if [ -c "$MESH_PORT" ]; then
    # Use meshtastic CLI to send AMTP compact payload
    # uv run avoids needing global install
    # Use ch-index 1 = amigosmalla channel (not default)
    uv run --with meshtastic meshtastic \
        --port "$MESH_PORT" \
        --ch-index 1 \
        --sendtext "$AMTP" \
        2>/dev/null || true
fi

# --- Transport 4: ggwave audio chirp (if available) ---
AQ_GGWAVE_TX="$(dirname "$0")/../../contrib/ggwave/aq_ggwave_tx.py"
if [ ! -f "$AQ_GGWAVE_TX" ]; then
    # Fallback: try relative to the repo checkout
    AQ_GGWAVE_TX="$CWD/contrib/ggwave/aq_ggwave_tx.py"
fi
if [ -f "$AQ_GGWAVE_TX" ]; then
    uv run "$AQ_GGWAVE_TX" \
        --protocol audible-fast \
        --volume 50 \
        "$AMTP" \
        2>/dev/null || true
fi

exit 0
