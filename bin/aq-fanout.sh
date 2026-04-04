#!/usr/bin/env bash
# aq-fanout.sh — best-effort fanout to enabled transports after aq announce
#
# Called by git hooks and Claude Code hooks after aq writes to disk.
# Each transport is fire-and-forget with a timeout. Failures are logged
# to stderr but never block the caller.
#
# Transports: mqtt, mesh (ch-index 1 ONLY), ggwave, udp-multicast, mdns
# Config: ~/.aq/config.json (jq required for config parsing)
#
# Usage: aq-fanout.sh <conjecture> <phase> <files> <claim>
# Example: aq-fanout.sh C-1 proof "main.go" "committed auth refactor"

set -euo pipefail

CONJECTURE="${1:-C-0}"
PHASE="${2:-proof}"
FILES="${3:-}"
CLAIM="${4:-checkpoint}"

# Compact AMTP payload for constrained transports
AGENT_SHORT="$(git config user.name 2>/dev/null | head -c2 || echo 'aq')"
BRANCH_SHORT="$(git rev-parse --abbrev-ref HEAD 2>/dev/null | head -c1 || echo 'm')"
PHASE_SHORT="${PHASE:0:1}"
COMPACT="aq:${AGENT_SHORT}/${BRANCH_SHORT} ${CONJECTURE} [${PHASE_SHORT}] ${FILES}"

CONFIG="${AQ_HOME:-$HOME/.aq}/config.json"
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# --- Config helpers (jq, no python) ---
cfg() {
    # cfg <path> [default]
    if [ -f "$CONFIG" ] && command -v jq >/dev/null 2>&1; then
        jq -r "$1 // \"${2:-}\"" "$CONFIG" 2>/dev/null || echo "${2:-}"
    else
        echo "${2:-}"
    fi
}

is_enabled() {
    [ "$(cfg ".$1.enabled" "false")" = "true" ]
}

# --- Full JSON payload for transports that support it ---
AGENT="$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]||;s|\.git$||' || echo 'local')/$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo 'main')"
TS="$(date +%s)"
PAYLOAD="{\"agent\":\"${AGENT}\",\"conjecture_id\":\"${CONJECTURE}\",\"phase\":\"${PHASE}\",\"claim\":\"${CLAIM}\",\"files\":\"${FILES}\",\"ts\":${TS},\"ttl\":3600}"

# --- Transport 1: MQTT (via mosquitto_pub) ---
if is_enabled mqtt && command -v mosquitto_pub >/dev/null 2>&1; then
    MQTT_HOST="$(cfg '.mqtt.host' 'localhost')"
    MQTT_PORT="$(cfg '.mqtt.port' '1883')"
    MQTT_TOPIC="$(cfg '.mqtt.topic' 'aq')"
    timeout 5 mosquitto_pub -h "$MQTT_HOST" -p "$MQTT_PORT" \
        -t "${MQTT_TOPIC}/announce" -m "$PAYLOAD" 2>/dev/null &
    echo "[fanout] mqtt: ${MQTT_HOST}:${MQTT_PORT}" >&2
fi

# --- Transport 2: Meshtastic (channel 1 = amigosmalla, NEVER channel 0 = all of Boston) ---
if is_enabled mesh; then
    MESH_PORT="$(cfg '.mesh.port' '/dev/cu.usbmodem3101')"
    if [ -c "$MESH_PORT" ] 2>/dev/null; then
        timeout 15 uv run --with meshtastic meshtastic \
            --port "$MESH_PORT" --ch-index 1 --sendtext "$COMPACT" 2>/dev/null &
        echo "[fanout] mesh: ch1 via ${MESH_PORT}" >&2
    else
        echo "[fanout] mesh: ${MESH_PORT} not available" >&2
    fi
fi

# --- Transport 3: ggwave (audio chirp) ---
if is_enabled ggwave; then
    GGWAVE_PROTOCOL="$(cfg '.ggwave.protocol' 'audible-fast')"
    GGWAVE_VOLUME="$(cfg '.ggwave.volume' '50')"
    timeout 15 uv run "${SCRIPT_DIR}/contrib/ggwave/aq_ggwave_tx.py" \
        "$COMPACT" --protocol "$GGWAVE_PROTOCOL" --volume "$GGWAVE_VOLUME" 2>/dev/null &
    echo "[fanout] ggwave: ${GGWAVE_PROTOCOL} vol=${GGWAVE_VOLUME}" >&2
fi

# --- Transport 4: UDP multicast ---
if is_enabled udp; then
    UDP_GROUP="$(cfg '.udp.group' '239.69.83.65')"
    UDP_PORT="$(cfg '.udp.port' '4181')"
    timeout 5 go run "${SCRIPT_DIR}/contrib/udp-multicast/udp.go" \
        -publish -group "$UDP_GROUP" -port "$UDP_PORT" \
        -agent "$AGENT" -conjecture "$CONJECTURE" -phase "$PHASE" \
        -files "$FILES" 2>/dev/null &
    echo "[fanout] udp: ${UDP_GROUP}:${UDP_PORT}" >&2
fi

# --- Transport 5: mDNS (LAN service advertisement) ---
if is_enabled mdns && command -v dns-sd >/dev/null 2>&1; then
    timeout 3 dns-sd -R "aq-checkpoint" _aq._tcp local 4181 \
        "conjecture=$CONJECTURE" "phase=$PHASE" "files=$FILES" 2>/dev/null &
    echo "[fanout] mdns: registered" >&2
fi

# Wait for all background jobs (best effort, don't block long)
wait 2>/dev/null || true
