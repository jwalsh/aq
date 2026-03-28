"""aq-mesh bridge — best-effort broadcast to Meshtastic via am CLI.

This module is entirely optional. If am is not installed, no device is
connected, or MESH_PSK is unset, all functions return False silently.
A failed mesh broadcast must never block or fail an aq announce.
"""
from __future__ import annotations

import logging
import os
import subprocess

from .protocol import Broadcast

logger = logging.getLogger("aq.mesh")

# Compact wire format for ~200 byte Meshtastic text payloads:
#   aq:agent/branch C-id [phase] file1,file2
_MAX_AGENT_LEN = 40
_MAX_FILES_LEN = 130


def is_enabled() -> bool:
    """Check if mesh broadcasting is enabled via environment."""
    return os.environ.get("AQ_MESH", "0") == "1"


def compact(broadcast: Broadcast) -> str:
    """Compress a Broadcast to fit in a Meshtastic text frame (~200 bytes).

    Format: aq:agent/branch C-id [phase] file1,file2
    """
    agent = broadcast.agent
    if len(agent) > _MAX_AGENT_LEN:
        agent = agent[-_MAX_AGENT_LEN:]

    files = ",".join(os.path.basename(f) for f in broadcast.files)
    if len(files) > _MAX_FILES_LEN:
        files = files[:_MAX_FILES_LEN].rsplit(",", 1)[0]

    status_tag = broadcast.status if broadcast.status != "prosecuting" else ""
    phase_tag = f"[{broadcast.phase}]"
    if status_tag:
        phase_tag = f"[{broadcast.status}]"

    parts = [f"aq:{agent}", broadcast.conjecture_id, phase_tag]
    if files:
        parts.append(files)

    return " ".join(parts)


def parse(text: str) -> dict | None:
    """Parse a compact mesh broadcast back to fields. Returns None if not an aq broadcast."""
    if not text.startswith("aq:"):
        return None
    try:
        rest = text[3:]
        tokens = rest.split()
        if len(tokens) < 2:
            return None
        agent = tokens[0]
        conjecture_id = tokens[1]
        phase = "proof"
        files = []
        for token in tokens[2:]:
            if token.startswith("[") and token.endswith("]"):
                phase = token[1:-1]
            else:
                files = [f.strip() for f in token.split(",") if f.strip()]
        return {
            "agent": agent,
            "conjecture_id": conjecture_id,
            "phase": phase,
            "files": files,
        }
    except Exception:
        return None


def mesh_broadcast(broadcast: Broadcast, via: str = "serial") -> bool:
    """Best-effort mesh broadcast via am CLI.

    Returns True if sent, False if skipped for any reason.
    Never raises. Never blocks aq announce.
    """
    # Check am is available
    am_path = _find_am()
    if not am_path:
        logger.debug("am CLI not found, skipping mesh broadcast")
        return False

    message = compact(broadcast)
    logger.debug("mesh broadcast: %s", message)

    try:
        cmd = [am_path, "send"]
        if via == "mqtt":
            cmd.extend(["--via", "mqtt"])
        cmd.append(message)

        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            logger.debug("am send failed (rc=%d): %s", result.returncode, result.stderr.strip())
            return False

        logger.info("mesh broadcast sent: %s", message)
        return True

    except subprocess.TimeoutExpired:
        logger.debug("am send timed out")
        return False
    except FileNotFoundError:
        logger.debug("am binary not found")
        return False
    except Exception as exc:
        logger.debug("mesh broadcast error: %s", exc)
        return False


def _find_am() -> str | None:
    """Locate the am CLI binary. Returns path or None."""
    try:
        result = subprocess.run(
            ["which", "am"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return None
