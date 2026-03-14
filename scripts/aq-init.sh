#!/usr/bin/env bash
set -euo pipefail
AQ_HOME="${AQ_HOME:-$HOME/.aq}"
for dir in \
  "$AQ_HOME/channels/broadcast/requests" \
  "$AQ_HOME/channels/broadcast/archive" \
  "$AQ_HOME/agents" \
  "$AQ_HOME/logs"; do
  mkdir -p "$dir"
done
cat > "$AQ_HOME/config.json" <<EOF
{"version":"0.1.0","default_channel":"broadcast","default_ttl":300}
EOF
echo "aq initialized at $AQ_HOME"
