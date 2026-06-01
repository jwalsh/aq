#!/usr/bin/env bash
# Publish a DNS-AID-style TXT record over mDNS (Linux/avahi only).
#
# This is the LAN mock for DNS-AID: it answers TXT queries for
# `_aq._agents.local` from any host on the same broadcast domain.
# Joining aq agents whose `dns_aid.enabled = true` will resolve the
# record via the mDNS tier and bootstrap their `http.url`.
#
# Usage:
#   ./publish-mdns.sh                                # publish defaults
#   ENDPOINT=http://gossip-sink:7738/gossip ./publish-mdns.sh
#
# Requires `avahi-publish-record` (Debian/Ubuntu: avahi-utils).
# On macOS, `dns-sd` cannot publish arbitrary TXT under non-DNS-SD
# names; use a Go mDNS responder instead (see dns-aid.org §8).
#
# This script runs in the foreground and keeps the record alive until
# you Ctrl-C. avahi removes the record on process exit (no manual
# cleanup needed).

set -euo pipefail

NAME="${NAME:-_aq._agents.local}"
ENDPOINT="${ENDPOINT:-http://localhost:7738/gossip}"
PROTOCOLS="${PROTOCOLS:-http,udp}"
VERSION="${VERSION:-1.1}"
TTL="${TTL:-3600}"
LABEL="${LABEL:-mdns-mock}"

if ! command -v avahi-publish-record >/dev/null 2>&1; then
  echo "publish-mdns.sh: avahi-publish-record not found" >&2
  echo "  install: sudo apt-get install avahi-utils  (Debian/Ubuntu)" >&2
  echo "  or:      sudo pkg install avahi             (FreeBSD)" >&2
  exit 1
fi

echo "publishing DNS-AID record over mDNS:"
echo "  name      = ${NAME}"
echo "  endpoint  = ${ENDPOINT}"
echo "  protocols = ${PROTOCOLS}"
echo "  version   = ${VERSION}"
echo "  ttl       = ${TTL}"
echo "  label     = ${LABEL}"
echo "(Ctrl-C to withdraw)"
echo

exec avahi-publish-record -v "${NAME}" TXT \
  "endpoint=${ENDPOINT}" \
  "protocols=${PROTOCOLS}" \
  "version=${VERSION}" \
  "ttl=${TTL}" \
  "label=${LABEL}"
