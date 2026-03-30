#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "ggwave",
#     "sounddevice",
#     "numpy",
# ]
# ///
"""aq-ggwave TX — encode an aq broadcast as audio and play through speakers.

Takes an AMTP compact payload (or builds one from flags) and chirps it
out the system speaker via ggwave FSK encoding. Fire-and-forget.

Run with uv:
  uv run contrib/ggwave/aq_ggwave_tx.py "aq:jw/main C-1 [p] main.go"
  uv run contrib/ggwave/aq_ggwave_tx.py -c C-1 -f main.go --phase proof

Or install deps and run directly:
  uv pip install ggwave sounddevice numpy
  python3 contrib/ggwave/aq_ggwave_tx.py "aq:jw/main C-1 [p] main.go"
"""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time

try:
    import ggwave
    import numpy as np
    import sounddevice as sd
except ImportError as _import_err:
    print(
        f"[ggwave-tx] missing dependency: {_import_err}\n"
        f"  install with: pip install ggwave sounddevice numpy",
        file=sys.stderr,
    )
    sys.exit(2)  # permanent failure: missing dependency

SAMPLE_RATE = 48000

# Protocol IDs discovered empirically from ggwave 0.4.x:
#   0-2: audible (normal/fast/fastest)
#   3-5: ultrasound (normal/fast/fastest)
#   6-8: dual-tone (normal/fast/fastest)
PROTOCOLS = {
    "audible": 0,
    "audible-fast": 1,
    "audible-fastest": 2,
    "ultrasonic": 3,
    "ultrasonic-fast": 4,
    "ultrasonic-fastest": 5,
    "dt": 6,
    "dt-fast": 7,
    "dt-fastest": 8,
}

PHASE_ABBREV = {
    "conjecture": "c",
    "proof": "p",
    "refutation": "r",
    "refinement": "n",
}


def detect_agent() -> str:
    """Detect agent address from git, abbreviated for payload size."""
    try:
        branch = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"],
            capture_output=True, text=True,
        ).stdout.strip() or "main"

        remote_url = subprocess.run(
            ["git", "remote", "get-url", "origin"],
            capture_output=True, text=True,
        ).stdout.strip()

        if remote_url:
            # github.com/jwalsh/aq → jwalsh
            parts = remote_url.replace("git@github.com:", "").replace("https://github.com/", "").removesuffix(".git").split("/")
            owner = parts[0] if parts else "local"
            # Abbreviate: first 2 chars of owner + / + first char of branch
            abbrev = owner[:2] if len(owner) >= 2 else owner
            branch_abbrev = branch[0] if branch else "m"
            return f"{abbrev}/{branch_abbrev}"
        return f"local/{branch[0] if branch else 'm'}"
    except Exception:
        return "lo/m"


def build_amtp_payload(
    agent: str,
    conjecture_id: str,
    phase: str,
    files: list[str],
) -> str:
    """Build AMTP compact payload, respecting byte limits."""
    phase_char = PHASE_ABBREV.get(phase, phase[0] if phase else "p")
    file_suffix = f" {','.join(files)}" if files else ""
    return f"aq:{agent} {conjecture_id} [{phase_char}]{file_suffix}"


def transmit(payload: str, protocol_name: str = "ultrasonic", volume: int = 50) -> None:
    """Encode payload as audio and play through speakers."""
    payload_bytes = len(payload.encode("utf-8"))
    protocol_id = PROTOCOLS.get(protocol_name)
    if protocol_id is None:
        print(f"[ggwave-tx] unknown protocol: {protocol_name}", file=sys.stderr)
        print(f"[ggwave-tx] available: {', '.join(PROTOCOLS.keys())}", file=sys.stderr)
        sys.exit(1)

    # Ultrasonic limit check
    if "ultrasonic" in protocol_name and payload_bytes > 25:
        print(f"[ggwave-tx] warn: payload is {payload_bytes} bytes, ultrasonic max is ~25", file=sys.stderr)
        print(f"  payload: {payload}", file=sys.stderr)
        print(f"  consider: --protocol audible", file=sys.stderr)

    # ggwave 0.4.x API: encode(payload, protocolId=, volume=)
    # Returns bytes of 16-bit signed integer PCM samples
    try:
        waveform = ggwave.encode(payload, protocolId=protocol_id, volume=volume)
    except Exception as exc:
        print(f"[ggwave-tx] TX: ggwave encode failed: {exc}", file=sys.stderr)
        sys.exit(1)

    if waveform is None or len(waveform) == 0:
        print("[ggwave-tx] TX: ggwave encode returned empty waveform", file=sys.stderr)
        sys.exit(1)

    # Convert from int16 PCM to float32 for sounddevice
    samples = np.frombuffer(waveform, dtype=np.int16).astype(np.float32) / 32768.0
    duration_seconds = len(samples) / SAMPLE_RATE

    print(f"[ggwave-tx] TX: {payload}", file=sys.stderr)
    print(f"[ggwave-tx] TX: protocol={protocol_name}, {payload_bytes}B, {duration_seconds:.1f}s, vol={volume}", file=sys.stderr)

    try:
        sd.play(samples, samplerate=SAMPLE_RATE)
        sd.wait()
    except sd.PortAudioError as exc:
        print(f"[ggwave-tx] TX: audio hardware error: {exc}", file=sys.stderr)
        print("[ggwave-tx] TX: check that an audio output device is available", file=sys.stderr)
        sys.exit(1)
    except Exception as exc:
        print(f"[ggwave-tx] TX: playback failed: {exc}", file=sys.stderr)
        sys.exit(1)

    print("[ggwave-tx] TX: done", file=sys.stderr)


def main() -> int:
    parser = argparse.ArgumentParser(
        prog="aq-ggwave-tx",
        description="aq ggwave TX — chirp an aq broadcast through the speaker",
    )

    parser.add_argument(
        "payload",
        nargs="?",
        help="AMTP compact payload (e.g. 'aq:jw/main C-1 [p] main.go')",
    )
    parser.add_argument(
        "-c", "--conjecture",
        help="conjecture ID (builds payload from flags if no positional arg)",
    )
    parser.add_argument(
        "-f", "--files",
        default="",
        help="comma-separated file list",
    )
    parser.add_argument(
        "--phase",
        default="proof",
        choices=["conjecture", "proof", "refutation", "refinement"],
    )
    parser.add_argument(
        "--agent",
        default=None,
        help="agent address (auto-detected from git if omitted)",
    )
    parser.add_argument(
        "--protocol",
        default=os.environ.get("AQ_GGWAVE_PROTOCOL", "ultrasonic"),
        choices=list(PROTOCOLS.keys()),
        help="ggwave protocol (default: ultrasonic)",
    )
    parser.add_argument(
        "--volume",
        type=int,
        default=int(os.environ.get("AQ_GGWAVE_VOLUME", "50")),
        help="TX volume 0-100 (default: 50)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="print payload but don't play audio",
    )

    args = parser.parse_args()

    # Build or use payload
    if args.payload:
        payload = args.payload
    elif args.conjecture:
        agent = args.agent or detect_agent()
        files = [f.strip() for f in args.files.split(",") if f.strip()]
        payload = build_amtp_payload(agent, args.conjecture, args.phase, files)
    else:
        parser.print_help()
        print("\nExamples:")
        print("  uv run contrib/ggwave/aq_ggwave_tx.py 'aq:jw/main C-1 [p] main.go'")
        print("  uv run contrib/ggwave/aq_ggwave_tx.py -c C-1 -f main.go")
        print("  uv run contrib/ggwave/aq_ggwave_tx.py -c C-7 --protocol audible")
        return 0

    if not payload.startswith("aq:"):
        print(f"warn: payload doesn't start with 'aq:' — receivers will ignore it", file=sys.stderr)

    if args.dry_run:
        print(f"dry-run: {payload} ({len(payload.encode('utf-8'))}B, {args.protocol})")
        return 0

    transmit(payload, protocol_name=args.protocol, volume=args.volume)
    return 0


if __name__ == "__main__":
    sys.exit(main())
