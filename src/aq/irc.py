"""aq-irc transport — broadcast via IRC PRIVMSG to a channel.

Best-effort publishing. Never blocks aq announce. Falls back gracefully
when no IRC client is available or the server is unreachable.

IRC was the first aq transport considered (Tier 3, FIT 55). The channel
#aq-presence on miniircd is the canonical target. Humans can /join the
channel and watch agents work --- this is the hybrid monitoring use case.

Configuration (in order of precedence):
  1. Environment variables:
       AQ_IRC           Set to 1 to enable IRC publishing
       AQ_IRC_HOST      IRC server host (default: localhost)
       AQ_IRC_PORT      IRC server port (default: 6667)
       AQ_IRC_CHANNEL   Channel name (default: #aq-presence)
       AQ_IRC_NICK      Bot nickname (default: aq-{pid})
  2. Config file: ~/.aq/config.json
       {
         "irc": {
           "enabled": true,
           "host": "192.168.86.100",
           "port": 6999,
           "channel": "#aq-presence",
           "nick": "aq-bot"
         }
       }
  3. Defaults: localhost:6667, #aq-presence, disabled
"""
from __future__ import annotations

import json
import logging
import os
import socket
import subprocess
import time
from dataclasses import asdict
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .protocol import Broadcast

logger = logging.getLogger("aq.irc")

DEFAULT_HOST = "localhost"
DEFAULT_PORT = 6667
DEFAULT_CHANNEL = "#aq-presence"
DEFAULT_NICK_PREFIX = "aq"

_config_cache: dict | None = None


def _load_config_file() -> dict:
    """Load ~/.aq/config.json if it exists."""
    global _config_cache
    if _config_cache is not None:
        return _config_cache

    config_path = Path.home() / ".aq" / "config.json"
    if config_path.exists():
        try:
            _config_cache = json.loads(config_path.read_text())
            return _config_cache
        except Exception as exc:
            logger.debug("failed to load config: %s", exc)

    _config_cache = {}
    return _config_cache


def is_enabled() -> bool:
    """Check if IRC publishing is enabled via environment or config."""
    env_val = os.environ.get("AQ_IRC")
    if env_val is not None:
        return env_val == "1"

    config = _load_config_file()
    return config.get("irc", {}).get("enabled", False)


def get_config() -> dict:
    """Get IRC configuration from environment, config file, or defaults."""
    file_config = _load_config_file().get("irc", {})

    nick_default = f"{DEFAULT_NICK_PREFIX}-{os.getpid()}"

    return {
        "host": os.environ.get("AQ_IRC_HOST") or file_config.get("host", DEFAULT_HOST),
        "port": int(os.environ.get("AQ_IRC_PORT") or file_config.get("port", DEFAULT_PORT)),
        "channel": os.environ.get("AQ_IRC_CHANNEL") or file_config.get("channel", DEFAULT_CHANNEL),
        "nick": os.environ.get("AQ_IRC_NICK") or file_config.get("nick", nick_default),
    }


def compact(broadcast: "Broadcast") -> str:
    """Compress a Broadcast to a compact IRC payload.

    Format matches the mesh compact format for consistency:
      aq:agent/branch C-id [phase] file1,file2

    IRC has no frame size limit like LoRa, but compact payloads are
    easier to read when a human is watching #aq-presence in an IRC client.
    """
    agent = broadcast.agent
    files = ",".join(os.path.basename(f) for f in broadcast.files) if broadcast.files else ""

    status_tag = broadcast.status if broadcast.status != "prosecuting" else ""
    phase_tag = f"[{broadcast.phase}]"
    if status_tag:
        phase_tag = f"[{broadcast.status}]"

    parts = [f"aq:{agent}", broadcast.conjecture_id, phase_tag]
    if files:
        parts.append(files)

    return " ".join(parts)


def irc_publish(broadcast: "Broadcast") -> bool:
    """Publish a broadcast to IRC via PRIVMSG.

    Tries three strategies in order:
      1. Raw socket send (connect, NICK, USER, JOIN, PRIVMSG, QUIT)
      2. ncat/nc subprocess (pipe IRC commands)
      3. Give up silently

    Returns True if sent, False if skipped or failed.
    Never raises. Never blocks aq announce.
    """
    config = get_config()
    message = compact(broadcast)

    logger.debug("irc publish: %s -> %s %s", config["channel"], config["host"], message)

    # Strategy 1: raw socket (no external dependencies)
    if _send_via_socket(config, message):
        logger.info("irc published (socket): %s", config["channel"])
        return True

    # Strategy 2: ncat/nc subprocess
    if _send_via_netcat(config, message):
        logger.info("irc published (netcat): %s", config["channel"])
        return True

    logger.debug("irc publish failed: no working strategy")
    return False


def irc_session_announce(
    session_id: str,
    cwd: str,
    source: str = "startup",
    model: str = "",
    agent: str = "",
    hostname: str = "",
) -> bool:
    """Publish a session announcement to IRC.

    Designed for Claude Code hooks --- publishes session metadata as a
    human-readable IRC message.
    """
    config = get_config()
    project = cwd.rsplit("/", 1)[-1] if cwd else "unknown"
    actual_hostname = hostname or socket.gethostname()
    actual_agent = agent or os.environ.get("USER", "unknown")

    message = f"session:{source} {actual_agent}@{actual_hostname} {project} [{session_id[:8]}]"
    if model:
        message += f" model={model}"

    return _send_via_socket(config, message) or _send_via_netcat(config, message)


def _build_irc_commands(nick: str, channel: str, message: str) -> str:
    """Build the raw IRC protocol commands for connect-send-quit.

    This implements the minimal IRC handshake:
      NICK -> USER -> JOIN -> PRIVMSG -> QUIT

    We do not wait for server responses (fire-and-forget). miniircd
    and ngircd both accept this pattern.
    """
    lines = [
        f"NICK {nick}",
        f"USER {nick} 0 * :aq presence bot",
        f"JOIN {channel}",
        f"PRIVMSG {channel} :{message}",
        "QUIT :aq broadcast complete",
    ]
    return "\r\n".join(lines) + "\r\n"


def _send_via_socket(config: dict, message: str) -> bool:
    """Send IRC message using a raw TCP socket.

    Opens a connection, sends the IRC handshake + PRIVMSG, and closes.
    Timeout is aggressive (3 seconds total) to avoid blocking aq announce.
    """
    host = config["host"]
    port = config["port"]
    nick = config["nick"]
    channel = config["channel"]

    irc_payload = _build_irc_commands(nick, channel, message)

    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(3.0)
        sock.connect((host, port))
        sock.sendall(irc_payload.encode("utf-8"))
        # Brief pause to let the server process before we close
        time.sleep(0.1)
        sock.close()
        return True

    except socket.timeout:
        logger.debug("irc socket timed out connecting to %s:%d", host, port)
        return False
    except ConnectionRefusedError:
        logger.debug("irc connection refused: %s:%d", host, port)
        return False
    except OSError as exc:
        logger.debug("irc socket error: %s", exc)
        return False
    except Exception as exc:
        logger.debug("irc socket unexpected error: %s", exc)
        return False


def _send_via_netcat(config: dict, message: str) -> bool:
    """Send IRC message using ncat/nc as a subprocess fallback.

    Pipes the IRC protocol commands to ncat stdin. This works when
    raw sockets are restricted but ncat is available.
    """
    ncat_path = _find_netcat()
    if not ncat_path:
        logger.debug("ncat/nc not found, skipping netcat strategy")
        return False

    host = config["host"]
    port = config["port"]
    nick = config["nick"]
    channel = config["channel"]

    irc_payload = _build_irc_commands(nick, channel, message)

    try:
        result = subprocess.run(
            [ncat_path, host, str(port)],
            input=irc_payload,
            capture_output=True,
            text=True,
            timeout=5,
        )
        # nc returns 0 even if the server rejects; we just check it ran
        if result.returncode != 0:
            logger.debug("netcat failed (rc=%d): %s", result.returncode, result.stderr.strip())
            return False
        return True

    except subprocess.TimeoutExpired:
        logger.debug("netcat timed out")
        return False
    except FileNotFoundError:
        logger.debug("netcat binary not found at runtime")
        return False
    except Exception as exc:
        logger.debug("netcat error: %s", exc)
        return False


def _find_netcat() -> str | None:
    """Locate ncat or nc binary. Prefers ncat (nmap version) over BSD nc."""
    for binary_name in ["ncat", "nc"]:
        try:
            result = subprocess.run(
                ["which", binary_name],
                capture_output=True,
                text=True,
                timeout=5,
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception:
            pass

    # Check common locations
    for path in ["/usr/bin/ncat", "/usr/local/bin/ncat", "/usr/bin/nc", "/usr/local/bin/nc"]:
        if os.path.isfile(path):
            return path

    return None
