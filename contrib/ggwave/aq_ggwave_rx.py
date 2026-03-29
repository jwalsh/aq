#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "ggwave",
#     "sounddevice",
#     "numpy",
# ]
# ///
"""aq-ggwave RX adapter — decode audio broadcasts, ingest to filesystem.

Listens on the system microphone for ggwave-encoded AMTP compact payloads.
Decoded broadcasts are written to ~/.aq/channels/broadcast/requests/ as JSON
files, where the standard filesystem transport picks them up.

This is the RX half. TX is handled by aq_ggwave_tx.py or the --ggwave flag
on `aq announce` (via SubprocessTransport shelling out to ggwave-cli).

Dependencies (not in aq core — contrib only):
  pip install ggwave sounddevice numpy

Configuration:
  AQ_GGWAVE_PROTOCOL   "ultrasonic" or "audible" (default: ultrasonic)
  AQ_GGWAVE_INTERVAL   Listen window in seconds (default: 5)
  AQ_GGWAVE_VOLUME     TX volume 0-100 (default: 50, unused by RX)
  AQ_HOME              Override ~/.aq root

AMTP compact wire format (same as Meshtastic/IRC):
  aq:AGENT/BRANCH CONJECTURE [PHASE] FILE1,FILE2

Examples:
  aq:jw/main C-1 [p] main.go
  aq:dt/m C-7 [r]
  aq:jw/f C-6 [p] a.py,b.py

Phase abbreviations:
  [c] = conjecture, [p] = proof, [r] = refutation, [n] = refinement, [d] = done

Usage:
  # Listen continuously, ingest to filesystem:
  python contrib/ggwave/aq_ggwave_rx.py

  # Listen with verbose logging:
  python contrib/ggwave/aq_ggwave_rx.py --verbose

  # Specify audible mode (listens for both by default):
  python contrib/ggwave/aq_ggwave_rx.py --protocol audible

  # Custom listen window:
  python contrib/ggwave/aq_ggwave_rx.py --interval 3
"""
from __future__ import annotations

import json
import logging
import os
import random
import signal
import string
import sys
import time
from dataclasses import asdict
from pathlib import Path
from threading import Event

logger = logging.getLogger("aq.ggwave.rx")

# --- Constants ---

SAMPLE_RATE = 48000
DEFAULT_LISTEN_INTERVAL = 5.0  # seconds per capture window
AQ_PREFIX = "aq:"

PHASE_ABBREV = {
    "c": "conjecture",
    "p": "proof",
    "r": "refutation",
    "n": "refinement",
    "d": "done",
    # Full names also accepted
    "conjecture": "conjecture",
    "proof": "proof",
    "refutation": "refutation",
    "refinement": "refinement",
}


# --- AMTP Compact Format Parser ---

def parse_amtp_compact(payload: str) -> dict | None:
    """Parse an AMTP compact payload into a broadcast dict.

    Format: aq:AGENT/BRANCH CONJECTURE [PHASE] FILE1,FILE2

    Returns None if the payload doesn't match the expected format.
    """
    if not payload.startswith(AQ_PREFIX):
        return None

    remainder = payload[len(AQ_PREFIX):]
    parts = remainder.split()

    if len(parts) < 2:
        logger.debug("amtp too short: %r", payload)
        return None

    agent_branch = parts[0]
    conjecture_id = parts[1]

    # Parse phase from [X] bracket notation
    phase = "proof"  # default
    files: list[str] = []
    file_start_index = 2

    for i in range(2, len(parts)):
        part = parts[i]
        if part.startswith("[") and part.endswith("]"):
            phase_abbrev = part[1:-1]
            phase = PHASE_ABBREV.get(phase_abbrev, phase_abbrev)
            file_start_index = i + 1
            break

    # Remaining parts are comma-separated file list
    if file_start_index < len(parts):
        file_string = " ".join(parts[file_start_index:])
        files = [f.strip() for f in file_string.split(",") if f.strip()]

    # Split agent/branch
    if "/" in agent_branch:
        agent = agent_branch
        branch = agent_branch.rsplit("/", 1)[-1]
    else:
        agent = agent_branch
        branch = agent_branch

    return {
        "agent": agent,
        "worktree": branch,
        "conjecture_id": conjecture_id,
        "conjecture_claim": f"ggwave rx: {conjecture_id}",
        "phase": phase,
        "status": "prosecuting",
        "files": files,
        "ts": time.time(),
        "ttl": 3600,
        "id": _generate_id(),
    }


def _generate_id() -> str:
    """Generate a broadcast ID matching the aq ULID format.

    12 hex chars of ms timestamp + 10 random lowercase alphanumeric.
    """
    ms_timestamp = format(int(time.time() * 1000), "012x")
    random_suffix = "".join(random.choices(string.ascii_lowercase + string.digits, k=10))
    return ms_timestamp + random_suffix


# --- Filesystem Ingest ---

def _aq_home() -> Path:
    """Return AQ_HOME, defaulting to ~/.aq."""
    return Path(os.environ.get("AQ_HOME", Path.home() / ".aq"))


def ingest_broadcast(broadcast_dict: dict) -> Path:
    """Write a decoded broadcast to the filesystem as a JSON file.

    The file lands in ~/.aq/channels/broadcast/requests/ where the
    standard read_active() picks it up.
    """
    requests_directory = _aq_home() / "channels" / "broadcast" / "requests"
    requests_directory.mkdir(parents=True, exist_ok=True)

    timestamp_padded = format(int(broadcast_dict["ts"]), "014d")
    broadcast_id = broadcast_dict["id"]
    filename = f"aq-{timestamp_padded}-{broadcast_id}.json"
    file_path = requests_directory / filename

    file_path.write_text(json.dumps(broadcast_dict) + "\n")
    logger.info("ingested: %s -> %s", broadcast_dict["conjecture_id"], filename)
    return file_path


# --- ggwave RX Loop ---

def listen_once(
    listen_duration: float = DEFAULT_LISTEN_INTERVAL,
) -> str | None:
    """Record audio for listen_duration seconds, attempt ggwave decode.

    Returns the decoded payload string, or None if nothing decoded.
    Requires: ggwave, sounddevice, numpy.
    """
    try:
        import ggwave
        import sounddevice as sd
        import numpy as np
    except ImportError as exc:
        logger.error(
            "missing dependency: %s — install with: pip install ggwave sounddevice numpy",
            exc,
        )
        return None

    instance = ggwave.init()
    try:
        sample_count = int(listen_duration * SAMPLE_RATE)
        logger.debug("recording %d samples (%.1fs)...", sample_count, listen_duration)

        recording = sd.rec(
            sample_count,
            samplerate=SAMPLE_RATE,
            channels=1,
            dtype="float32",
        )
        sd.wait()

        raw_bytes = recording.tobytes()
        result = ggwave.decode(instance, raw_bytes)

        if result is not None and len(result) > 0:
            try:
                decoded = result.decode("utf-8") if isinstance(result, bytes) else str(result)
                logger.debug("decoded: %r", decoded)
                return decoded
            except UnicodeDecodeError:
                logger.debug("non-utf8 payload, ignoring")
                return None

        return None
    finally:
        ggwave.free(instance)


def listen_loop(
    listen_duration: float = DEFAULT_LISTEN_INTERVAL,
    stop_event: Event | None = None,
) -> None:
    """Continuously listen for ggwave payloads and ingest aq broadcasts.

    Blocks until stop_event is set or SIGINT/SIGTERM.
    """
    if stop_event is None:
        stop_event = Event()

    seen_ids: set[str] = set()
    received_count = 0
    ignored_count = 0

    logger.info(
        "ggwave rx: listening (interval=%.1fs, sample_rate=%d)",
        listen_duration, SAMPLE_RATE,
    )

    while not stop_event.is_set():
        payload = listen_once(listen_duration)

        if payload is None:
            continue

        # Only process aq: prefixed payloads
        if not payload.startswith(AQ_PREFIX):
            ignored_count += 1
            logger.debug("non-aq payload ignored: %r", payload[:40])
            continue

        broadcast_dict = parse_amtp_compact(payload)
        if broadcast_dict is None:
            ignored_count += 1
            logger.debug("unparseable amtp: %r", payload[:60])
            continue

        # Dedup by content hash (same agent+conjecture+phase within window)
        dedup_key = f"{broadcast_dict['agent']}:{broadcast_dict['conjecture_id']}:{broadcast_dict['phase']}"
        if dedup_key in seen_ids:
            logger.debug("dedup: %s", dedup_key)
            continue
        seen_ids.add(dedup_key)

        # Prune dedup set periodically (keep last 1000)
        if len(seen_ids) > 1000:
            seen_ids = set(list(seen_ids)[-500:])

        ingest_broadcast(broadcast_dict)
        received_count += 1
        logger.info(
            "rx #%d: %s from %s [%s] files=%s",
            received_count,
            broadcast_dict["conjecture_id"],
            broadcast_dict["agent"],
            broadcast_dict["phase"],
            broadcast_dict.get("files", []),
        )


# --- CLI ---

def main() -> int:
    """CLI entry point for ggwave RX adapter."""
    import argparse

    parser = argparse.ArgumentParser(
        prog="aq-ggwave-rx",
        description="aq ggwave RX — listen for audio broadcasts, ingest to filesystem",
    )
    parser.add_argument(
        "--interval", "-i",
        type=float,
        default=float(os.environ.get("AQ_GGWAVE_INTERVAL", str(DEFAULT_LISTEN_INTERVAL))),
        help=f"listen window in seconds (default: {DEFAULT_LISTEN_INTERVAL})",
    )
    parser.add_argument(
        "--protocol", "-p",
        default=os.environ.get("AQ_GGWAVE_PROTOCOL", "ultrasonic"),
        choices=["ultrasonic", "audible"],
        help="ggwave protocol mode (default: ultrasonic). Note: RX decodes all protocols regardless.",
    )
    parser.add_argument(
        "--once",
        action="store_true",
        help="listen once and exit (don't loop)",
    )
    parser.add_argument(
        "--verbose", "-v",
        action="store_true",
        help="enable debug logging",
    )

    args = parser.parse_args()

    log_level = logging.DEBUG if args.verbose else logging.INFO
    logging.basicConfig(
        level=log_level,
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
        datefmt="%H:%M:%S",
    )

    stop_event = Event()

    def handle_signal(signum: int, frame: object) -> None:
        logger.info("received signal %d, stopping", signum)
        stop_event.set()

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    if args.once:
        payload = listen_once(args.interval)
        if payload and payload.startswith(AQ_PREFIX):
            broadcast_dict = parse_amtp_compact(payload)
            if broadcast_dict:
                path = ingest_broadcast(broadcast_dict)
                print(f"ingested: {path.name}")
                return 0
        print("no aq broadcast decoded")
        return 1

    listen_loop(listen_duration=args.interval, stop_event=stop_event)
    return 0


if __name__ == "__main__":
    sys.exit(main())
