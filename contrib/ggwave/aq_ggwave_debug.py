#!/usr/bin/env python3
"""aq-ggwave debug harness — test AMTP parsing and ingest without audio hardware.

Simulates the ggwave RX pipeline: takes AMTP compact payloads on stdin (or
generates synthetic ones) and pushes them through parse_amtp_compact →
ingest_broadcast → filesystem. The aq gossip protocol handles it from there.

No dependencies beyond stdlib. No ggwave, no sounddevice, no numpy.

Usage:
  # Parse a single payload:
  echo 'aq:jw/main C-1 [p] main.go' | python contrib/ggwave/aq_ggwave_debug.py

  # Generate N synthetic broadcasts and ingest:
  python contrib/ggwave/aq_ggwave_debug.py --synthetic 5

  # Parse from file (one AMTP payload per line):
  python contrib/ggwave/aq_ggwave_debug.py < payloads.txt

  # Verify round-trip: ingest then check with aq status:
  python contrib/ggwave/aq_ggwave_debug.py --synthetic 3
  ./aq status

  # Test contention: run alongside a real aq announce:
  python contrib/ggwave/aq_ggwave_debug.py --synthetic 1 &
  ./aq announce -c C-2 -f main.go --claim "real announce"
  ./aq status   # should show both
"""
from __future__ import annotations

import json
import os
import random
import string
import sys
import time
from pathlib import Path


# Inline the pieces we need from aq_ggwave_rx.py so this file is standalone.
# No imports from aq or aq_ggwave_rx — zero coupling.

AQ_PREFIX = "aq:"

PHASE_ABBREV = {
    "c": "conjecture",
    "p": "proof",
    "r": "refutation",
    "n": "refinement",
    "d": "done",
    "conjecture": "conjecture",
    "proof": "proof",
    "refutation": "refutation",
    "refinement": "refinement",
}


def generate_id() -> str:
    ms_timestamp = format(int(time.time() * 1000), "012x")
    random_suffix = "".join(random.choices(string.ascii_lowercase + string.digits, k=10))
    return ms_timestamp + random_suffix


def parse_amtp_compact(payload: str) -> dict | None:
    """Parse AMTP compact format into a broadcast dict."""
    if not payload.startswith(AQ_PREFIX):
        return None

    remainder = payload[len(AQ_PREFIX):]
    parts = remainder.split()

    if len(parts) < 2:
        return None

    agent_branch = parts[0]
    conjecture_id = parts[1]

    phase = "proof"
    files: list[str] = []
    file_start_index = 2

    for i in range(2, len(parts)):
        part = parts[i]
        if part.startswith("[") and part.endswith("]"):
            phase_abbrev = part[1:-1]
            phase = PHASE_ABBREV.get(phase_abbrev, phase_abbrev)
            file_start_index = i + 1
            break

    if file_start_index < len(parts):
        file_string = " ".join(parts[file_start_index:])
        files = [f.strip() for f in file_string.split(",") if f.strip()]

    if "/" in agent_branch:
        branch = agent_branch.rsplit("/", 1)[-1]
    else:
        branch = agent_branch

    return {
        "agent": agent_branch,
        "worktree": branch,
        "conjecture_id": conjecture_id,
        "conjecture_claim": f"ggwave debug: {conjecture_id}",
        "phase": phase,
        "status": "prosecuting",
        "files": files,
        "ts": time.time(),
        "ttl": 3600,
        "id": generate_id(),
    }


def aq_home() -> Path:
    return Path(os.environ.get("AQ_HOME", Path.home() / ".aq"))


def ingest_broadcast(broadcast_dict: dict) -> Path:
    """Write broadcast to filesystem where aq read_active() finds it."""
    requests_directory = aq_home() / "channels" / "broadcast" / "requests"
    requests_directory.mkdir(parents=True, exist_ok=True)

    timestamp_padded = format(int(broadcast_dict["ts"]), "014d")
    broadcast_id = broadcast_dict["id"]
    filename = f"aq-{timestamp_padded}-{broadcast_id}.json"
    file_path = requests_directory / filename

    file_path.write_text(json.dumps(broadcast_dict) + "\n")
    return file_path


# --- Synthetic Payloads ---

SYNTHETIC_AGENTS = [
    "jw/main", "dt/feat-auth", "kk/fix-api", "ap/main", "sjg/refactor"
]
SYNTHETIC_CONJECTURES = ["C-1", "C-2", "C-3", "C-4", "C-6", "C-7", "C-8"]
SYNTHETIC_PHASES = ["c", "p", "r", "n"]
SYNTHETIC_FILES = [
    "main.go", "protocol.py", "auth.py", "cli.py", "conflict.py",
    "spec.org", "transport.go", "mesh.py", "README.md",
]


def generate_synthetic_payload() -> str:
    """Generate a random AMTP compact payload."""
    agent = random.choice(SYNTHETIC_AGENTS)
    conjecture = random.choice(SYNTHETIC_CONJECTURES)
    phase = random.choice(SYNTHETIC_PHASES)
    num_files = random.randint(0, 3)
    files = random.sample(SYNTHETIC_FILES, min(num_files, len(SYNTHETIC_FILES)))
    file_suffix = f" {','.join(files)}" if files else ""
    return f"aq:{agent} {conjecture} [{phase}]{file_suffix}"


# --- Main ---

def main() -> int:
    import argparse

    parser = argparse.ArgumentParser(
        prog="aq-ggwave-debug",
        description="debug harness for ggwave AMTP → filesystem pipeline (no audio deps)",
    )
    parser.add_argument(
        "--synthetic", "-s",
        type=int,
        default=0,
        metavar="N",
        help="generate N synthetic broadcasts and ingest them",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="parse and print but don't write to filesystem",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="output parsed broadcasts as JSON",
    )

    args = parser.parse_args()

    payloads: list[str] = []

    if args.synthetic > 0:
        payloads = [generate_synthetic_payload() for _ in range(args.synthetic)]
    elif not sys.stdin.isatty():
        payloads = [line.strip() for line in sys.stdin if line.strip()]
    else:
        parser.print_help()
        print("\nExamples:")
        print("  echo 'aq:jw/main C-1 [p] main.go' | python contrib/ggwave/aq_ggwave_debug.py")
        print("  python contrib/ggwave/aq_ggwave_debug.py --synthetic 5")
        return 0

    ingested = 0
    failed = 0

    for payload in payloads:
        broadcast_dict = parse_amtp_compact(payload)

        if broadcast_dict is None:
            print(f"  FAIL  {payload}", file=sys.stderr)
            failed += 1
            continue

        if args.json:
            print(json.dumps(broadcast_dict, indent=2))
        elif args.dry_run:
            files = ",".join(broadcast_dict["files"]) or "(none)"
            print(f"  OK    {broadcast_dict['agent']:20s}  {broadcast_dict['conjecture_id']}  [{broadcast_dict['phase']}]  {files}")
        else:
            file_path = ingest_broadcast(broadcast_dict)
            files = ",".join(broadcast_dict["files"]) or "(none)"
            print(f"  OK    {broadcast_dict['conjecture_id']}  [{broadcast_dict['phase']}]  {files}  -> {file_path.name}")
            ingested += 1

    if not args.json:
        action = "parsed" if args.dry_run else "ingested"
        total = ingested if not args.dry_run else len(payloads) - failed
        print(f"\n{total} {action}, {failed} failed")
        if ingested > 0:
            print("verify: ./aq status")

    return 1 if failed > 0 else 0


if __name__ == "__main__":
    sys.exit(main())
