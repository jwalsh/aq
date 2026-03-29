"""aq-kbfs transport — broadcast via Keybase filesystem.

Best-effort write to shared KBFS directory. Never blocks aq announce.
Falls back gracefully when Keybase is not installed or not logged in.

Configuration:
  AQ_KBFS        Set to 1 to enable
  AQ_KBFS_DIR    Shared directory path (e.g. /keybase/private/user1,user2/aq)
"""
from __future__ import annotations

import json
import logging
import os
import subprocess
import time
from dataclasses import asdict
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .protocol import Broadcast

logger = logging.getLogger("aq.kbfs")


def is_enabled() -> bool:
    env_val = os.environ.get("AQ_KBFS")
    if env_val is not None:
        return env_val == "1"
    try:
        import json
        from pathlib import Path
        cfg = json.loads((Path.home() / ".aq" / "config.json").read_text())
        return cfg.get("kbfs", {}).get("enabled", False)
    except Exception:
        return False


def kbfs_publish(broadcast: "Broadcast", channel: str = "broadcast") -> bool:
    """Write broadcast to KBFS shared directory.

    Returns True if written, False if skipped or failed.
    Never raises.
    """
    kbfs_dir = os.environ.get("AQ_KBFS_DIR", "")
    if not kbfs_dir:
        try:
            import json
            from pathlib import Path
            cfg = json.loads((Path.home() / ".aq" / "config.json").read_text())
            kbfs_dir = cfg.get("kbfs", {}).get("dir", "")
        except Exception:
            pass
    if not kbfs_dir:
        logger.debug("AQ_KBFS_DIR not set and no dir in config, skipping")
        return False

    if not _find_keybase():
        logger.debug("keybase CLI not found, skipping")
        return False

    payload = json.dumps(asdict(broadcast))
    ts = int(time.time())
    agent = broadcast.agent.split("/")[-1] if "/" in broadcast.agent else broadcast.agent
    filename = f"aq-{agent}-{ts}.json"

    try:
        result = subprocess.run(
            ["keybase", "fs", "write", f"{kbfs_dir}/{filename}"],
            input=payload,
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode != 0:
            logger.debug("kbfs write failed: %s", result.stderr.strip())
            return False

        logger.info("kbfs published: %s/%s", kbfs_dir, filename)
        return True

    except subprocess.TimeoutExpired:
        logger.debug("kbfs write timed out")
        return False
    except Exception as exc:
        logger.debug("kbfs publish error: %s", exc)
        return False


def _find_keybase() -> bool:
    try:
        result = subprocess.run(
            ["which", "keybase"],
            capture_output=True, text=True, timeout=5,
        )
        return result.returncode == 0
    except Exception:
        return False
