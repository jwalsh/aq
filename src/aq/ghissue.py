"""aq-ghissue transport — broadcast via GitHub issue comments.

Best-effort comment on a configured GH issue. Useful as POC but
noisy for production — disable with AQ_GHISSUE=0 once you have
better transports.

Configuration:
  AQ_GHISSUE       Set to 1 to enable (default: 0)
  AQ_GHISSUE_REPO  Repo in owner/name format
  AQ_GHISSUE_NUM   Issue number to comment on
"""
from __future__ import annotations

import json
import logging
import os
import subprocess
from dataclasses import asdict
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .protocol import Broadcast

logger = logging.getLogger("aq.ghissue")


def is_enabled() -> bool:
    env_val = os.environ.get("AQ_GHISSUE")
    if env_val is not None:
        return env_val == "1"
    try:
        import json
        from pathlib import Path
        cfg = json.loads((Path.home() / ".aq" / "config.json").read_text())
        return cfg.get("ghissue", {}).get("enabled", False)
    except Exception:
        return False


def ghissue_publish(broadcast: "Broadcast") -> bool:
    """Comment broadcast summary on configured GH issue.

    Returns True if commented, False if skipped or failed.
    Never raises.
    """
    repo = os.environ.get("AQ_GHISSUE_REPO", "")
    issue_num = os.environ.get("AQ_GHISSUE_NUM", "")
    if not repo or not issue_num:
        logger.debug("AQ_GHISSUE_REPO or AQ_GHISSUE_NUM not set, skipping")
        return False

    if not _find_gh():
        logger.debug("gh CLI not found, skipping")
        return False

    # Compact summary for issue comment
    files = ", ".join(broadcast.files) if broadcast.files else "(none)"
    body = (
        f"**aq announce** `{broadcast.conjecture_id}` [{broadcast.phase}]\n\n"
        f"- agent: `{broadcast.agent}`\n"
        f"- claim: {broadcast.conjecture_claim}\n"
        f"- files: `{files}`\n"
        f"- status: {broadcast.status}"
    )

    try:
        result = subprocess.run(
            ["gh", "issue", "comment", issue_num,
             "--repo", repo, "--body", body],
            capture_output=True,
            text=True,
            timeout=15,
        )
        if result.returncode != 0:
            logger.debug("gh issue comment failed: %s", result.stderr.strip())
            return False

        logger.info("ghissue published: %s#%s", repo, issue_num)
        return True

    except subprocess.TimeoutExpired:
        logger.debug("gh issue comment timed out")
        return False
    except Exception as exc:
        logger.debug("ghissue publish error: %s", exc)
        return False


def _find_gh() -> bool:
    try:
        result = subprocess.run(
            ["which", "gh"],
            capture_output=True, text=True, timeout=5,
        )
        return result.returncode == 0
    except Exception:
        return False
