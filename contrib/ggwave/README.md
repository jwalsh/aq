# aq-ggwave: Audio/Ultrasonic Gossip Transport

Data over sound. Your laptop chirps, your mini's microphone hears it.
An aq broadcast crosses the room via sound waves. Tier omega.

See `ggwave-transport.org` for the full spec.

## Files

- `aq_ggwave_rx.py` — RX adapter: listen on microphone, decode, ingest to filesystem
- `aq_ggwave_debug.py` — debug harness: test AMTP parsing without audio hardware
- `ggwave-transport.org` — full transport specification

## Quick start (no audio hardware needed)

```bash
# Generate synthetic broadcasts and verify aq sees them:
python3 contrib/ggwave/aq_ggwave_debug.py --synthetic 3
./aq status

# Parse AMTP payloads from stdin:
echo 'aq:jw/main C-1 [p] main.go' | python3 contrib/ggwave/aq_ggwave_debug.py

# Dry-run (parse but don't write):
echo 'aq:jw/main C-1 [p] main.go' | python3 contrib/ggwave/aq_ggwave_debug.py --dry-run
```

## With audio hardware

```bash
# Install dependencies:
pip install ggwave sounddevice numpy

# Listen continuously:
python3 contrib/ggwave/aq_ggwave_rx.py

# Listen once:
python3 contrib/ggwave/aq_ggwave_rx.py --once

# TX from another machine (requires ggwave-cli):
echo "aq:jw/main C-1 [p]" | ggwave-cli -t
```

## AMTP compact format

The wire format is shared with Meshtastic, IRC, and BLE transports:

```
aq:AGENT/BRANCH CONJECTURE [PHASE] FILE1,FILE2
```

Phase abbreviations: `[c]` conjecture, `[p]` proof, `[r]` refutation, `[n]` refinement, `[d]` done.

Ultrasonic mode: 25-byte limit. Truncate agent names and file lists.
Audible mode: 140-byte limit. Full AMTP fits comfortably.

## How it works

1. RX captures audio in rolling windows (default 5s)
2. ggwave decodes FSK tones into bytes
3. If payload starts with `aq:`, parse AMTP compact format
4. Write standard JSON broadcast to `~/.aq/channels/broadcast/requests/`
5. The Go binary's `readActive()` picks it up like any other broadcast

ggwave is just a transport. Once decoded, the gossip protocol handles it.
