#!/usr/bin/env bash
# aq-fanout.sh — best-effort fanout to enabled transports after aq announce
#
# Called by git hooks and Claude Code hooks after aq writes to disk.
# Each transport is fire-and-forget with a timeout. Failures are logged
# to stderr but never block the caller.
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

is_enabled() {
    local transport="$1"
    if [ -f "$CONFIG" ]; then
        python3 -c "
import json,sys
c=json.load(open('$CONFIG'))
t=c.get('$transport',{})
sys.exit(0 if t.get('enabled') else 1)
" 2>/dev/null
    else
        return 1
    fi
}

# Meshtastic (channel 1, never 0)
if is_enabled mesh; then
    MESH_PORT=$(python3 -c "import json;c=json.load(open('$CONFIG'));print(c.get('mesh',{}).get('port','/dev/ttyUSB0'))" 2>/dev/null || echo "/dev/ttyUSB0")
    timeout 10 meshtastic --port "$MESH_PORT" --sendtext "$COMPACT" --ch-index 1 2>/dev/null &
    echo "[fanout] mesh: sent on ch1" >&2
fi

# ggwave (audio)
if is_enabled ggwave; then
    SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
    VOLUME=$(python3 -c "import json;c=json.load(open('$CONFIG'));print(c.get('ggwave',{}).get('volume',30))" 2>/dev/null || echo "30")
    timeout 15 uv run "${SCRIPT_DIR}/contrib/ggwave/aq_ggwave_tx.py" "$COMPACT" --protocol audible --volume "$VOLUME" 2>/dev/null &
    echo "[fanout] ggwave: chirping" >&2
fi

# KBFS (Keybase shared dir)
if is_enabled kbfs; then
    KBFS_DIR=$(python3 -c "import json;c=json.load(open('$CONFIG'));print(c.get('kbfs',{}).get('dir','/keybase/team/default/aq'))" 2>/dev/null || echo "/keybase/team/default/aq")
    SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
    timeout 10 go run "${SCRIPT_DIR}/contrib/keybase/kbfs.go" \
        -publish -path "$KBFS_DIR" \
        -agent "$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]||;s|\.git$||' || echo 'local')/$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo 'main')" \
        -conjecture "$CONJECTURE" -phase "$PHASE" -files "$FILES" 2>/dev/null &
    echo "[fanout] kbfs: writing to $KBFS_DIR" >&2
fi

# mDNS (LAN discovery)
if is_enabled mdns && command -v dns-sd >/dev/null 2>&1; then
    timeout 3 dns-sd -R "aq-checkpoint" _aq._tcp local 4181 \
        "conjecture=$CONJECTURE" "phase=$PHASE" "files=$FILES" 2>/dev/null &
    echo "[fanout] mdns: registered" >&2
fi

# Wait for all background jobs (best effort, don't block long)
wait 2>/dev/null || true
