# aq-ggwave: Audio/Ultrasonic Gossip Transport

Data over sound. Your laptop chirps, your workstation's microphone hears it.
An aq broadcast crosses the room via sound waves. Tier omega.

See `ggwave-transport.org` for the full spec.

## Files

- `aq_ggwave_tx.py` — TX: encode AMTP payload as audio, play through speakers
- `aq_ggwave_rx.py` — RX: listen on microphone, decode, ingest to filesystem
- `aq_ggwave_debug.py` — debug harness: test AMTP parsing without audio deps
- `ggwave-transport.org` — full transport specification

## Quick start with uv (no pip install needed)

```bash
# TX — chirp a broadcast (audible, you'll hear it):
uv run contrib/ggwave/aq_ggwave_tx.py -c C-1 -f main.go --protocol audible

# TX — ultrasonic (inaudible to most adults):
uv run contrib/ggwave/aq_ggwave_tx.py -c C-1 -f main.go

# TX — raw AMTP payload:
uv run contrib/ggwave/aq_ggwave_tx.py "aq:jw/main C-1 [p] main.go"

# TX — dry-run (no sound):
uv run contrib/ggwave/aq_ggwave_tx.py --dry-run -c C-1 -f main.go

# RX — listen continuously, ingest to filesystem:
uv run contrib/ggwave/aq_ggwave_rx.py

# RX — listen once:
uv run contrib/ggwave/aq_ggwave_rx.py --once
```

## Debug (no audio deps, stdlib only)

```bash
# Synthetic broadcasts — test ingest pipeline:
python3 contrib/ggwave/aq_ggwave_debug.py --synthetic 3
./aq status

# Parse from stdin:
echo 'aq:jw/main C-1 [p] main.go' | python3 contrib/ggwave/aq_ggwave_debug.py

# Dry-run:
echo 'aq:jw/main C-1 [p] main.go' | python3 contrib/ggwave/aq_ggwave_debug.py --dry-run
```

## AMTP compact format

Shared wire format with Meshtastic, IRC, and BLE transports:

```
aq:AGENT/BRANCH CONJECTURE [PHASE] FILE1,FILE2
```

Phase abbreviations: `[c]` conjecture, `[p]` proof, `[r]` refutation, `[n]` refinement, `[d]` done.

Ultrasonic: ~25-byte limit. Audible: ~140-byte limit.

## Protocols

| Name | ID | Frequency | Audible | Notes |
|------|----|-----------|---------|-------|
| audible | 0 | 1-8 kHz | Yes | clearly audible tones |
| audible-fast | 1 | 1-8 kHz | Yes | shorter duration |
| audible-fastest | 2 | 1-8 kHz | Yes | shortest |
| ultrasonic | 3 | 15-20 kHz | No* | default, inaudible to most adults |
| ultrasonic-fast | 4 | 15-20 kHz | No* | shorter |
| ultrasonic-fastest | 5 | 15-20 kHz | No* | shortest |
| dt | 6 | 1-8 kHz | Yes | dual-tone (DTMF-like) |

*Dogs can hear ultrasonic. This is documented.

## How it works

1. TX encodes AMTP compact payload via ggwave FSK → plays PCM through speaker
2. Sound propagates through air (~343 m/s, 1-10m range)
3. RX captures audio via microphone in rolling windows
4. ggwave decodes FSK tones back into bytes
5. If payload starts with `aq:`, parse AMTP → write `aq-*.json` to filesystem
6. The Go binary's `readActive()` picks it up like any other broadcast

ggwave is just a transport. Once decoded, the gossip protocol handles it.

## monit (keep RX alive on the host)

```
check process aq-ggwave-rx matching "aq_ggwave_rx"
  start program = "/opt/homebrew/bin/uv run /path/to/contrib/ggwave/aq_ggwave_rx.py"
  stop program = "/bin/kill -TERM"
  if 3 restarts within 5 cycles then alert
```
