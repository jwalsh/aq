"""aq-mqtt transport — publish broadcasts to MQTT broker.

Best-effort publishing. Never blocks aq announce. Falls back gracefully
when mosquitto_pub is unavailable or broker is unreachable.

Configuration (in order of precedence):
  1. Environment variables:
       AQ_MQTT_HOST    MQTT broker host
       AQ_MQTT_PORT    MQTT broker port
       AQ_MQTT_TOPIC   Topic prefix
       AQ_MQTT         Set to 1 to enable MQTT publishing
  2. Config file: ~/.aq/config.json
       {
         "mqtt": {
           "enabled": true,
           "host": "broker.local",
           "port": 1883,
           "topic": "aq"
         }
       }
  3. Defaults: localhost:1883, topic "aq", disabled
"""
from __future__ import annotations

import json
import logging
import os
import subprocess
from dataclasses import asdict
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .protocol import Broadcast

logger = logging.getLogger("aq.mqtt")

# Sensible defaults (localhost, not network-specific)
DEFAULT_HOST = "localhost"
DEFAULT_PORT = 1883
DEFAULT_TOPIC = "aq"

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
    """Check if MQTT publishing is enabled via environment or config."""
    # Environment takes precedence
    env_val = os.environ.get("AQ_MQTT")
    if env_val is not None:
        return env_val == "1"

    # Check config file
    config = _load_config_file()
    return config.get("mqtt", {}).get("enabled", False)


def get_config() -> dict:
    """Get MQTT configuration from environment, config file, or defaults."""
    file_config = _load_config_file().get("mqtt", {})

    return {
        "host": os.environ.get("AQ_MQTT_HOST") or file_config.get("host", DEFAULT_HOST),
        "port": int(os.environ.get("AQ_MQTT_PORT") or file_config.get("port", DEFAULT_PORT)),
        "topic": os.environ.get("AQ_MQTT_TOPIC") or file_config.get("topic", DEFAULT_TOPIC),
    }


def mqtt_publish(broadcast: "Broadcast", subtopic: str = "announce") -> bool:
    """Publish a broadcast to MQTT via mosquitto_pub.

    Returns True if published, False if skipped or failed.
    Never raises. Never blocks aq announce.
    """
    mosquitto_path = _find_mosquitto_pub()
    if not mosquitto_path:
        logger.debug("mosquitto_pub not found, skipping MQTT publish")
        return False

    config = get_config()
    topic = f"{config['topic']}/{subtopic}"
    payload = json.dumps(asdict(broadcast))

    logger.debug("mqtt publish: %s -> %s", topic, payload[:80])

    try:
        result = subprocess.run(
            [
                mosquitto_path,
                "-h", config["host"],
                "-p", str(config["port"]),
                "-t", topic,
                "-m", payload,
            ],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode != 0:
            logger.debug(
                "mosquitto_pub failed (rc=%d): %s",
                result.returncode,
                result.stderr.strip(),
            )
            return False

        logger.info("mqtt published: %s", topic)
        return True

    except subprocess.TimeoutExpired:
        logger.debug("mosquitto_pub timed out")
        return False
    except FileNotFoundError:
        logger.debug("mosquitto_pub binary not found")
        return False
    except Exception as exc:
        logger.debug("mqtt publish error: %s", exc)
        return False


def mqtt_session_announce(
    session_id: str,
    cwd: str,
    source: str = "startup",
    model: str = "",
    agent: str = "",
    hostname: str = "",
) -> bool:
    """Publish a session start announcement to MQTT.

    Designed for Claude Code hooks — publishes minimal session metadata.
    """
    mosquitto_path = _find_mosquitto_pub()
    if not mosquitto_path:
        return False

    import socket
    import time

    config = get_config()
    topic = f"{config['topic']}/session/{source}"

    # Extract project name from cwd
    project = cwd.rsplit("/", 1)[-1] if cwd else "unknown"

    payload = json.dumps({
        "session_id": session_id,
        "project": project,
        "cwd": cwd,
        "source": source,
        "model": model,
        "agent": agent or os.environ.get("USER", "unknown"),
        "hostname": hostname or socket.gethostname(),
        "ts": time.time(),
    })

    try:
        result = subprocess.run(
            [
                mosquitto_path,
                "-h", config["host"],
                "-p", str(config["port"]),
                "-t", topic,
                "-m", payload,
            ],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return result.returncode == 0
    except Exception:
        return False


def _find_mosquitto_pub() -> str | None:
    """Locate mosquitto_pub binary. Returns path or None."""
    try:
        result = subprocess.run(
            ["which", "mosquitto_pub"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass

    # Check common locations
    for path in ["/usr/local/bin/mosquitto_pub", "/usr/bin/mosquitto_pub"]:
        if os.path.isfile(path):
            return path

    return None
