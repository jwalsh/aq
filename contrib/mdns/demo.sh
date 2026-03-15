#!/usr/bin/env bash
# demo.sh — mDNS transport demo for aq using macOS dns-sd
#
# This script demonstrates aq broadcasts over mDNS/DNS-SD using only
# the macOS built-in dns-sd command. No installation required.
#
# What it does:
#   1. Registers two agent broadcasts via mDNS
#   2. Browses for all aq services on the network
#   3. Looks up the TXT records (broadcast payload) for each agent
#   4. Shows conflict detection (both agents touch auth.py in proof phase)
#   5. Deregisters Agent A (simulating status=done)
#   6. Shows the conflict is cleared
#
# Usage:
#   chmod +x demo.sh
#   ./demo.sh
#
# Requirements:
#   - macOS (dns-sd is built in via Bonjour)
#   - No other software needed
#
# To run individual commands manually, see the README.md in this directory.

set -euo pipefail

# Colors for readability (disabled if not a terminal).
if [ -t 1 ]; then
    BOLD='\033[1m'
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    CYAN='\033[0;36m'
    RESET='\033[0m'
else
    BOLD='' RED='' GREEN='' YELLOW='' CYAN='' RESET=''
fi

# Ensure we are on macOS.
if [[ "$(uname)" != "Darwin" ]]; then
    echo "This demo requires macOS (for the dns-sd command)."
    echo "On Linux, use avahi-publish-service and avahi-browse instead."
    echo "See README.md for Linux commands."
    exit 1
fi

# Ensure dns-sd is available.
if ! command -v dns-sd &>/dev/null; then
    echo "dns-sd not found. This should be built into macOS."
    exit 1
fi

# Cleanup function: kill all background processes on exit.
PIDS=()
cleanup() {
    echo ""
    echo -e "${BOLD}Cleaning up...${RESET}"
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    done
    # Remove temp files.
    rm -f /tmp/aq-mdns-browse.log /tmp/aq-mdns-lookup-a.log /tmp/aq-mdns-lookup-b.log
    echo -e "${GREEN}Done. All mDNS services deregistered.${RESET}"
}
trap cleanup EXIT

# ---------- Header ----------

echo ""
echo -e "${BOLD}========================================${RESET}"
echo -e "${BOLD}  aq mDNS Transport Demo${RESET}"
echo -e "${BOLD}  Using macOS Bonjour (dns-sd)${RESET}"
echo -e "${BOLD}========================================${RESET}"
echo ""
echo "This demo shows two agents broadcasting their presence via mDNS"
echo "on the local network, and how conflict detection works when both"
echo "agents touch the same file in the same CPRR phase."
echo ""

# ---------- Step 1: Register Agent A ----------

echo -e "${CYAN}--- Step 1: Register Agent A ---${RESET}"
echo ""
echo "Agent A is working on conjecture C-1 (filesystem transport is sufficient)."
echo "Phase: proof. Files: auth.py, session.py."
echo ""
echo "Command:"
echo '  dns-sd -R "origin/feat-auth" "_aq._tcp" "local" 0 \'
echo '    conjecture=C-1 phase=proof status=prosecuting \'
echo '    files=auth.py,session.py worktree=feat-auth \'
echo '    claim=filesystem-transport-is-sufficient'
echo ""

dns-sd -R "origin/feat-auth" "_aq._tcp" "local" 0 \
    conjecture=C-1 \
    phase=proof \
    status=prosecuting \
    files=auth.py,session.py \
    worktree=feat-auth \
    claim=filesystem-transport-is-sufficient &
PIDS+=($!)

sleep 1
echo -e "${GREEN}Agent A registered.${RESET}"
echo ""

# ---------- Step 2: Register Agent B ----------

echo -e "${CYAN}--- Step 2: Register Agent B ---${RESET}"
echo ""
echo "Agent B is working on conjecture C-7 (heartbeat prevents TTL cliff)."
echo "Phase: proof. Files: auth.py (CONFLICT with Agent A!)."
echo ""
echo "Command:"
echo '  dns-sd -R "origin/feat-session" "_aq._tcp" "local" 0 \'
echo '    conjecture=C-7 phase=proof status=prosecuting \'
echo '    files=auth.py worktree=feat-session \'
echo '    claim=heartbeat-prevents-ttl-cliff'
echo ""

dns-sd -R "origin/feat-session" "_aq._tcp" "local" 0 \
    conjecture=C-7 \
    phase=proof \
    status=prosecuting \
    files=auth.py \
    worktree=feat-session \
    claim=heartbeat-prevents-ttl-cliff &
PIDS+=($!)

sleep 1
echo -e "${GREEN}Agent B registered.${RESET}"
echo ""

# ---------- Step 3: Browse for services ----------

echo -e "${CYAN}--- Step 3: Browse for aq agents ---${RESET}"
echo ""
echo "Querying the local network for all _aq._tcp services..."
echo ""
echo "Command:"
echo '  dns-sd -B _aq._tcp local'
echo ""

# Browse for 3 seconds and capture output.
timeout 3 dns-sd -B _aq._tcp local > /tmp/aq-mdns-browse.log 2>&1 || true

echo "Browse results:"
echo -e "${YELLOW}"
cat /tmp/aq-mdns-browse.log || echo "(no output captured)"
echo -e "${RESET}"

# ---------- Step 4: Look up Agent A details ----------

echo -e "${CYAN}--- Step 4: Look up Agent A broadcast payload ---${RESET}"
echo ""
echo "Fetching TXT records for Agent A..."
echo ""
echo "Command:"
echo '  dns-sd -L "origin/feat-auth" "_aq._tcp" "local"'
echo ""

timeout 3 dns-sd -L "origin/feat-auth" "_aq._tcp" "local" > /tmp/aq-mdns-lookup-a.log 2>&1 || true

echo "Agent A payload:"
echo -e "${YELLOW}"
cat /tmp/aq-mdns-lookup-a.log || echo "(no output captured)"
echo -e "${RESET}"

# ---------- Step 5: Look up Agent B details ----------

echo -e "${CYAN}--- Step 5: Look up Agent B broadcast payload ---${RESET}"
echo ""
echo "Fetching TXT records for Agent B..."
echo ""
echo "Command:"
echo '  dns-sd -L "origin/feat-session" "_aq._tcp" "local"'
echo ""

timeout 3 dns-sd -L "origin/feat-session" "_aq._tcp" "local" > /tmp/aq-mdns-lookup-b.log 2>&1 || true

echo "Agent B payload:"
echo -e "${YELLOW}"
cat /tmp/aq-mdns-lookup-b.log || echo "(no output captured)"
echo -e "${RESET}"

# ---------- Step 6: Conflict analysis ----------

echo -e "${CYAN}--- Step 6: Conflict Analysis ---${RESET}"
echo ""
echo "Analyzing broadcasts for semantic conflicts..."
echo ""
echo "  Agent A: conjecture=C-1, phase=proof, files=auth.py,session.py"
echo "  Agent B: conjecture=C-7, phase=proof, files=auth.py"
echo ""
echo -e "  Shared files: ${RED}auth.py${RESET}"
echo -e "  Both in phase: ${RED}proof${RESET}"
echo ""
echo -e "  ${RED}${BOLD}CONFLICT SEVERITY: HIGH${RESET}"
echo ""
echo "  Both agents are in the proof phase and touching the same file."
echo "  This is the highest conflict severity in the CPRR model."
echo "  Action: one agent should coordinate before proceeding."
echo ""

# ---------- Step 7: Simulate Agent A finishing ----------

echo -e "${CYAN}--- Step 7: Agent A finishes (deregister) ---${RESET}"
echo ""
echo "Agent A completes its work. Killing the registration process"
echo "deregisters the mDNS service, just like Ctrl-C in a real session."
echo ""

# Kill Agent A's dns-sd process (first PID).
if [ ${#PIDS[@]} -gt 0 ]; then
    kill "${PIDS[0]}" 2>/dev/null || true
    wait "${PIDS[0]}" 2>/dev/null || true
    echo -e "${GREEN}Agent A deregistered. Broadcast removed from network.${RESET}"
    # Remove from cleanup list.
    PIDS=("${PIDS[@]:1}")
fi
echo ""

# Give mDNS a moment to propagate the deregistration.
sleep 1

# Browse again to show Agent A is gone.
echo "Browsing again to confirm Agent A is gone..."
echo ""
timeout 3 dns-sd -B _aq._tcp local > /tmp/aq-mdns-browse.log 2>&1 || true
echo -e "${YELLOW}"
cat /tmp/aq-mdns-browse.log || echo "(no output captured)"
echo -e "${RESET}"

echo -e "${GREEN}Conflict cleared. Agent B can proceed with auth.py.${RESET}"
echo ""

# ---------- Summary ----------

echo -e "${BOLD}========================================${RESET}"
echo -e "${BOLD}  Demo Complete${RESET}"
echo -e "${BOLD}========================================${RESET}"
echo ""
echo "What happened:"
echo "  1. Agent A registered an aq broadcast via mDNS (_aq._tcp)"
echo "  2. Agent B registered on the same network"
echo "  3. Browsing discovered both agents (zero configuration)"
echo "  4. TXT record lookup revealed broadcast payloads"
echo "  5. Conflict detected: both agents in proof phase on auth.py"
echo "  6. Agent A deregistered (finished work)"
echo "  7. Conflict cleared automatically"
echo ""
echo "Key insight: this is exactly the same flow as filesystem-based aq,"
echo "but it works across machines on the same network. No server, no"
echo "broker, no configuration. Just mDNS multicast."
echo ""
echo "For the Go implementation, see mdns.go in this directory."
echo "For full documentation, see README.md."
echo ""
