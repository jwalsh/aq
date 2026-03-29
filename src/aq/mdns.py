"""aq-mdns transport -- zero-config LAN discovery + broadcast via mDNS/DNS-SD.

Tier 1.5 transport. Advertises agent presence as a _aq._tcp service with
broadcast metadata in TXT records. Discovers peers via mDNS browse and
delivers full broadcast payloads via HTTP POST to each peer's advertised port.

This module is entirely optional. If dns-sd (macOS) or avahi (Linux) is not
available, all functions return False silently. A failed mDNS operation must
never block or fail an aq announce.

Configuration (in order of precedence):
  1. Environment variables:
       AQ_MDNS          Set to 1 to enable mDNS transport
       AQ_MDNS_PORT     HTTP listener port (default: 0 = auto-assign)
       AQ_MDNS_IFACE    Network interface (default: all)
  2. Config file: ~/.aq/config.json
       {
         "mdns": {
           "enabled": true,
           "port": 5309,
           "interface": "en0"
         }
       }
  3. Defaults: disabled, port 0 (OS-assigned), all interfaces

Platform support:
  - macOS: dns-sd (Bonjour, built-in)
  - Linux: avahi-publish-service / avahi-browse (avahi-utils package)
  - FreeBSD: dns-sd via mDNSResponder port, or avahi

Design notes:
  - TXT records carry compact metadata for browse-only conflict detection.
    Peers that only browse (no HTTP) can still detect conflicts from TXT
    fields alone. This is the "passive" mode.
  - HTTP POST carries the full Broadcast JSON for peers that want structured
    payloads. This is the "active" mode. The advertised port is the HTTP
    listener port.
  - The dns-sd / avahi-publish-service process runs in the background for
    the lifetime of the registration. Killing it deregisters the service.
    This maps to aq's lifecycle: broadcast lives as long as the agent works.
"""
from __future__ import annotations

import json
import logging
import os
import platform
import socket
import subprocess
import threading
import time
from dataclasses import asdict
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .protocol import Broadcast

logger = logging.getLogger("aq.mdns")

# --- Constants ---

SERVICE_TYPE = "_aq._tcp"
DOMAIN = "local"
DEFAULT_PORT = 0  # OS-assigned
BROWSE_TIMEOUT = 3  # seconds
POST_TIMEOUT = 5  # seconds
TXT_MAX_VALUE_LEN = 200  # conservative limit per TXT value

# --- Config ---

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
    """Check if mDNS transport is enabled via environment or config."""
    env_val = os.environ.get("AQ_MDNS")
    if env_val is not None:
        return env_val == "1"

    config = _load_config_file()
    return config.get("mdns", {}).get("enabled", False)


def get_config() -> dict:
    """Get mDNS configuration from environment, config file, or defaults."""
    file_config = _load_config_file().get("mdns", {})

    return {
        "port": int(
            os.environ.get("AQ_MDNS_PORT")
            or file_config.get("port", DEFAULT_PORT)
        ),
        "interface": (
            os.environ.get("AQ_MDNS_IFACE")
            or file_config.get("interface", "")
        ),
    }


# --- Platform detection ---

def _detect_platform() -> str | None:
    """Detect which mDNS tool is available. Returns 'dns-sd', 'avahi', or None."""
    system = platform.system()

    if system == "Darwin":
        # dns-sd is always present on macOS
        if _which("dns-sd"):
            return "dns-sd"

    # Linux, FreeBSD, or macOS fallback
    if _which("avahi-publish-service"):
        return "avahi"

    # FreeBSD with mDNSResponder port
    if _which("dns-sd"):
        return "dns-sd"

    return None


def _which(binary: str) -> str | None:
    """Locate a binary. Returns path or None."""
    try:
        result = subprocess.run(
            ["which", binary],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return None


# --- TXT record encoding ---

def broadcast_to_txt(broadcast: "Broadcast") -> dict[str, str]:
    """Convert a Broadcast to TXT record key-value pairs.

    TXT records have a 255-byte-per-string limit (key=value counts as one
    string). We truncate long values rather than omitting fields.

    Fields carried in TXT:
      conjecture  - conjecture ID (e.g., C-1)
      phase       - CPRR phase
      status      - prosecuting|done|blocked
      files       - comma-separated basenames
      worktree    - branch/worktree name
      claim       - conjecture claim (truncated)
      id          - broadcast ULID
      ts          - unix timestamp
      ttl         - TTL in seconds
    """
    files_value = ",".join(
        os.path.basename(f) for f in broadcast.files
    )
    if len(files_value) > TXT_MAX_VALUE_LEN:
        files_value = files_value[:TXT_MAX_VALUE_LEN].rsplit(",", 1)[0]

    claim_value = broadcast.conjecture_claim
    if len(claim_value) > TXT_MAX_VALUE_LEN:
        claim_value = claim_value[:TXT_MAX_VALUE_LEN]

    return {
        "conjecture": broadcast.conjecture_id,
        "phase": broadcast.phase,
        "status": broadcast.status,
        "files": files_value,
        "worktree": broadcast.worktree,
        "claim": claim_value,
        "id": broadcast.id,
        "ts": str(int(broadcast.ts)),
        "ttl": str(broadcast.ttl),
    }


def txt_to_fields(txt_records: list[str]) -> dict[str, str]:
    """Parse TXT record strings (key=value) into a dict."""
    fields: dict[str, str] = {}
    for record in txt_records:
        parts = record.split("=", 1)
        if len(parts) == 2:
            fields[parts[0].strip()] = parts[1].strip()
    return fields


# --- HTTP receiver (active mode) ---

class _BroadcastReceiver(BaseHTTPRequestHandler):
    """Minimal HTTP handler that accepts POST /aq/broadcast with JSON body."""

    # Class-level callback; set before server starts.
    on_receive = None

    def do_POST(self):
        if self.path != "/aq/broadcast":
            self.send_response(404)
            self.end_headers()
            return

        content_length = int(self.headers.get("Content-Length", 0))
        if content_length == 0 or content_length > 8192:
            self.send_response(400)
            self.end_headers()
            return

        body = self.rfile.read(content_length)
        try:
            payload = json.loads(body)
        except (json.JSONDecodeError, UnicodeDecodeError):
            self.send_response(400)
            self.end_headers()
            return

        self.send_response(202)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"accepted"}\n')

        if _BroadcastReceiver.on_receive:
            try:
                _BroadcastReceiver.on_receive(payload)
            except Exception as exc:
                logger.debug("on_receive callback error: %s", exc)

    def do_GET(self):
        """Health check endpoint."""
        if self.path == "/aq/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status":"ok","transport":"mdns"}\n')
            return
        self.send_response(404)
        self.end_headers()

    def log_message(self, format, *args):
        """Suppress default stderr logging; route through aq logger."""
        logger.debug("http: %s", format % args)


class MdnsReceiver:
    """HTTP server that listens for broadcast POSTs from mDNS peers.

    Start this before registering the mDNS service so the port is known.
    """

    def __init__(self, port: int = 0, on_receive=None):
        _BroadcastReceiver.on_receive = on_receive
        self._server = HTTPServer(("0.0.0.0", port), _BroadcastReceiver)
        self.port = self._server.server_address[1]
        self._thread: threading.Thread | None = None

    def start(self):
        """Start the HTTP server in a daemon thread."""
        self._thread = threading.Thread(
            target=self._server.serve_forever,
            daemon=True,
            name="aq-mdns-http",
        )
        self._thread.start()
        logger.debug("mdns http receiver listening on port %d", self.port)

    def stop(self):
        """Shutdown the HTTP server."""
        self._server.shutdown()
        if self._thread:
            self._thread.join(timeout=2)
        logger.debug("mdns http receiver stopped")


# --- Service registration ---

_active_registration: subprocess.Popen | None = None
_active_receiver: MdnsReceiver | None = None


def mdns_register(
    instance_name: str,
    broadcast: "Broadcast",
    port: int | None = None,
    on_receive=None,
) -> bool:
    """Register an aq service via mDNS with TXT records.

    Starts an HTTP receiver on the given port (or auto-assigned) and
    registers a _aq._tcp service advertising that port. The dns-sd or
    avahi-publish-service process runs in the background.

    Returns True if registration started, False otherwise.
    Never raises.
    """
    global _active_registration, _active_receiver

    tool = _detect_platform()
    if not tool:
        logger.debug("no mDNS tool available, skipping registration")
        return False

    config = get_config()
    listen_port = port if port is not None else config["port"]

    # Start HTTP receiver first to get the actual port
    try:
        receiver = MdnsReceiver(port=listen_port, on_receive=on_receive)
        receiver.start()
        actual_port = receiver.port
    except Exception as exc:
        logger.debug("failed to start http receiver: %s", exc)
        return False

    txt_fields = broadcast_to_txt(broadcast)

    try:
        if tool == "dns-sd":
            cmd = _build_dns_sd_register(instance_name, actual_port, txt_fields)
        else:
            cmd = _build_avahi_register(instance_name, actual_port, txt_fields)

        proc = subprocess.Popen(
            cmd,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )

        # Give the process a moment to fail immediately
        time.sleep(0.1)
        if proc.poll() is not None:
            logger.debug("mDNS registration process exited immediately (rc=%d)", proc.returncode)
            receiver.stop()
            return False

        # Deregister any previous registration
        mdns_deregister()

        _active_registration = proc
        _active_receiver = receiver

        logger.info(
            "mdns registered: %s on port %d (%s)",
            instance_name, actual_port, tool,
        )
        return True

    except Exception as exc:
        logger.debug("mdns registration error: %s", exc)
        receiver.stop()
        return False


def mdns_deregister() -> None:
    """Deregister the active mDNS service and stop the HTTP receiver."""
    global _active_registration, _active_receiver

    if _active_registration is not None:
        try:
            _active_registration.terminate()
            _active_registration.wait(timeout=2)
        except Exception:
            try:
                _active_registration.kill()
            except Exception:
                pass
        _active_registration = None

    if _active_receiver is not None:
        try:
            _active_receiver.stop()
        except Exception:
            pass
        _active_receiver = None


def _build_dns_sd_register(
    instance_name: str, port: int, txt_fields: dict[str, str]
) -> list[str]:
    """Build dns-sd -R command for macOS."""
    cmd = ["dns-sd", "-R", instance_name, SERVICE_TYPE, DOMAIN, str(port)]
    for key, value in txt_fields.items():
        cmd.append(f"{key}={value}")
    return cmd


def _build_avahi_register(
    instance_name: str, port: int, txt_fields: dict[str, str]
) -> list[str]:
    """Build avahi-publish-service command for Linux."""
    cmd = ["avahi-publish-service", instance_name, SERVICE_TYPE, str(port)]
    for key, value in txt_fields.items():
        cmd.append(f"{key}={value}")
    return cmd


# --- Service discovery ---

def mdns_browse(timeout: int = BROWSE_TIMEOUT) -> list[dict]:
    """Browse for _aq._tcp services on the local network.

    Returns a list of dicts with keys: instance, host, port, txt.
    The txt value is a dict of parsed TXT record fields.

    Uses dns-sd -Z (zone dump) on macOS or avahi-browse -rpt on Linux
    for machine-parseable output.

    Never raises. Returns [] on any failure.
    """
    tool = _detect_platform()
    if not tool:
        logger.debug("no mDNS tool available, skipping browse")
        return []

    try:
        if tool == "dns-sd":
            return _browse_dns_sd(timeout)
        else:
            return _browse_avahi(timeout)
    except Exception as exc:
        logger.debug("mdns browse error: %s", exc)
        return []


def _browse_dns_sd(timeout: int) -> list[dict]:
    """Browse using macOS dns-sd. Two-phase: browse for names, then lookup each."""
    # Phase 1: browse for instance names
    try:
        result = subprocess.run(
            ["dns-sd", "-B", SERVICE_TYPE, DOMAIN],
            capture_output=True,
            text=True,
            timeout=timeout + 1,
        )
        # dns-sd -B doesn't exit on its own; timeout kills it
    except subprocess.TimeoutExpired as exc:
        # Expected: dns-sd -B runs until killed
        result_stdout = exc.stdout if exc.stdout else ""
        if isinstance(result_stdout, bytes):
            result_stdout = result_stdout.decode("utf-8", errors="replace")
    else:
        result_stdout = result.stdout

    instances = _parse_dns_sd_browse(result_stdout)
    if not instances:
        return []

    # Phase 2: lookup TXT records for each instance
    peers = []
    for instance_name in instances:
        info = _lookup_dns_sd(instance_name, timeout=2)
        if info:
            peers.append(info)

    return peers


def _parse_dns_sd_browse(output: str) -> list[str]:
    """Parse dns-sd -B output to extract instance names.

    Output format:
      Browsing for _aq._tcp.local
      DATE: ---Mon 14 Mar 2026---
       3:42:15.123  ...DIFFERING...  Add  2  4  local.  _aq._tcp.  instance-name
    """
    instances = []
    for line in output.splitlines():
        line = line.strip()
        # Look for lines containing the service type that have an instance name
        if SERVICE_TYPE in line and ("Add" in line or "add" in line):
            # Instance name is the last whitespace-delimited field after _aq._tcp.
            parts = line.split(f"{SERVICE_TYPE}.")
            if len(parts) >= 2:
                instance_name = parts[-1].strip()
                if instance_name and instance_name not in instances:
                    instances.append(instance_name)
    return instances


def _lookup_dns_sd(instance_name: str, timeout: int = 2) -> dict | None:
    """Look up a specific mDNS service instance to get port and TXT records."""
    try:
        result = subprocess.run(
            ["dns-sd", "-L", instance_name, SERVICE_TYPE, DOMAIN],
            capture_output=True,
            text=True,
            timeout=timeout + 1,
        )
        output = result.stdout
    except subprocess.TimeoutExpired as exc:
        output = exc.stdout if exc.stdout else ""
        if isinstance(output, bytes):
            output = output.decode("utf-8", errors="replace")

    if not output:
        return None

    return _parse_dns_sd_lookup(instance_name, output)


def _parse_dns_sd_lookup(instance_name: str, output: str) -> dict | None:
    """Parse dns-sd -L output for host, port, and TXT records.

    Output format:
      Lookup instance._aq._tcp.local
      DATE: ---Mon 14 Mar 2026---
       3:42:20.789  instance._aq._tcp.local. can be reached at host.local.:PORT
       conjecture=C-1 phase=proof status=prosecuting files=auth.py worktree=feat-auth
    """
    host = None
    port = 0
    txt_fields: dict[str, str] = {}

    for line in output.splitlines():
        line = line.strip()

        # Parse "can be reached at host:port" line
        if "can be reached at" in line:
            # Extract host and port
            reach_part = line.split("can be reached at")[-1].strip()
            # Format: hostname.local.:PORT
            if ":" in reach_part:
                host_part, port_str = reach_part.rsplit(":", 1)
                host = host_part.rstrip(".")
                try:
                    port = int(port_str)
                except ValueError:
                    port = 0

        # Parse TXT record lines (key=value pairs separated by spaces)
        if "=" in line and "can be reached at" not in line and "DATE" not in line:
            # This line contains TXT records
            for token in _split_txt_line(line):
                if "=" in token:
                    key, _, value = token.partition("=")
                    key = key.strip()
                    value = value.strip()
                    if key and not key[0].isdigit():
                        txt_fields[key] = value

    if not host and not txt_fields:
        return None

    return {
        "instance": instance_name,
        "host": host or "unknown",
        "port": port,
        "txt": txt_fields,
    }


def _split_txt_line(line: str) -> list[str]:
    """Split a TXT record line into individual key=value tokens.

    Handles both space-separated (dns-sd) and quoted (avahi) formats.
    """
    tokens = []
    current = []
    in_escape = False

    for char in line:
        if in_escape:
            current.append(char)
            in_escape = False
        elif char == "\\":
            in_escape = True
        elif char == " " and current:
            token = "".join(current)
            if "=" in token:
                tokens.append(token)
            current = []
        else:
            current.append(char)

    if current:
        token = "".join(current)
        if "=" in token:
            tokens.append(token)

    return tokens


def _browse_avahi(timeout: int) -> list[dict]:
    """Browse using avahi-browse with parseable output.

    avahi-browse -rpt outputs tab-separated fields:
      +;iface;proto;name;type;domain;hostname;address;port;txt
    """
    try:
        result = subprocess.run(
            [
                "avahi-browse",
                "-rpt",  # resolve, parseable, terminate when done
                SERVICE_TYPE,
            ],
            capture_output=True,
            text=True,
            timeout=timeout + 2,
        )
    except subprocess.TimeoutExpired as exc:
        result_stdout = exc.stdout if exc.stdout else ""
        if isinstance(result_stdout, bytes):
            result_stdout = result_stdout.decode("utf-8", errors="replace")
        return _parse_avahi_browse(result_stdout)

    return _parse_avahi_browse(result.stdout)


def _parse_avahi_browse(output: str) -> list[dict]:
    """Parse avahi-browse -rpt output.

    Resolved entries start with '=' and have fields:
      =;iface;proto;name;type;domain;hostname;address;port;"key=value" "key=value"
    """
    peers = []
    seen_instances: set[str] = set()

    for line in output.splitlines():
        if not line.startswith("="):
            continue

        fields = line.split(";")
        if len(fields) < 10:
            continue

        instance_name = fields[3]
        if instance_name in seen_instances:
            continue
        seen_instances.add(instance_name)

        hostname = fields[6]
        address = fields[7]
        try:
            port = int(fields[8])
        except ValueError:
            port = 0

        # TXT records are in field 9+, quoted
        txt_raw = ";".join(fields[9:])
        txt_fields = _parse_avahi_txt(txt_raw)

        peers.append({
            "instance": instance_name,
            "host": address or hostname.rstrip("."),
            "port": port,
            "txt": txt_fields,
        })

    return peers


def _parse_avahi_txt(raw: str) -> dict[str, str]:
    """Parse avahi TXT field format: "key=value" "key=value" ..."""
    fields: dict[str, str] = {}
    # Remove surrounding quotes and split
    for token in raw.replace('"', "").split():
        if "=" in token:
            key, _, value = token.partition("=")
            if key:
                fields[key] = value
    return fields


# --- Publish (fanout entry point) ---

def mdns_publish(broadcast: "Broadcast") -> bool:
    """Publish a broadcast to all discovered mDNS peers via HTTP POST.

    This is the fanout entry point matching the pattern in mesh.py, mqtt.py,
    and kbfs.py. It:
      1. Browses for _aq._tcp peers on the LAN
      2. POSTs the full broadcast JSON to each peer's /aq/broadcast endpoint
      3. Returns True if at least one peer received it

    The browse step adds latency (~3s). For latency-sensitive paths, use
    mdns_publish_to_known_peers() with a cached peer list.

    Never raises. Never blocks aq announce (called with timeout).
    """
    tool = _detect_platform()
    if not tool:
        logger.debug("no mDNS tool available, skipping publish")
        return False

    peers = mdns_browse(timeout=BROWSE_TIMEOUT)
    if not peers:
        logger.debug("no mDNS peers discovered")
        return False

    return mdns_publish_to_known_peers(broadcast, peers)


def mdns_publish_to_known_peers(
    broadcast: "Broadcast",
    peers: list[dict],
) -> bool:
    """POST broadcast JSON to a list of known peers.

    Each peer dict must have 'host' and 'port' keys.
    Returns True if at least one peer accepted the broadcast.
    """
    payload = json.dumps(asdict(broadcast)).encode("utf-8")
    hostname = socket.gethostname()
    delivered = 0

    for peer in peers:
        peer_host = peer.get("host", "")
        peer_port = peer.get("port", 0)

        if not peer_host or not peer_port:
            continue

        # Skip self (same hostname, same port)
        if _is_self(peer_host, peer_port):
            continue

        if _http_post_broadcast(peer_host, peer_port, payload):
            delivered += 1

    if delivered > 0:
        logger.info("mdns published to %d peer(s)", delivered)
        return True

    logger.debug("mdns publish: no peers accepted the broadcast")
    return False


def _is_self(peer_host: str, peer_port: int) -> bool:
    """Best-effort check whether a peer is this machine's own receiver."""
    global _active_receiver
    if _active_receiver is None:
        return False

    hostname = socket.gethostname()
    local_port = _active_receiver.port

    if peer_port != local_port:
        return False

    # Hostname match (exact or with .local suffix)
    peer_host_clean = peer_host.rstrip(".")
    hostname_clean = hostname.rstrip(".")

    if peer_host_clean == hostname_clean:
        return True
    if peer_host_clean == f"{hostname_clean}.local":
        return True
    if f"{peer_host_clean}.local" == hostname_clean:
        return True

    return False


def _http_post_broadcast(host: str, port: int, payload: bytes) -> bool:
    """POST broadcast payload to a peer's HTTP endpoint.

    Uses urllib to avoid adding requests as a dependency.
    """
    import urllib.request
    import urllib.error

    url = f"http://{host}:{port}/aq/broadcast"

    try:
        request = urllib.request.Request(
            url,
            data=payload,
            headers={
                "Content-Type": "application/json",
                "User-Agent": "aq-mdns/0.1",
            },
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=POST_TIMEOUT) as response:
            if response.status in (200, 202):
                logger.debug("delivered to %s:%d", host, port)
                return True
            else:
                logger.debug("peer %s:%d returned %d", host, port, response.status)
                return False
    except urllib.error.URLError as exc:
        logger.debug("post to %s:%d failed: %s", host, port, exc)
        return False
    except Exception as exc:
        logger.debug("post to %s:%d error: %s", host, port, exc)
        return False


# --- Convenience: combined register + publish ---

def mdns_announce(broadcast: "Broadcast", on_receive=None) -> bool:
    """Register this agent's broadcast via mDNS and start the HTTP receiver.

    This is the "long-running" entry point for agents that want to maintain
    presence on the LAN for the duration of their session. Call
    mdns_deregister() when done.

    For one-shot fanout (announce to peers and exit), use mdns_publish().

    Never raises.
    """
    try:
        from .sb import Sandbox
        sandbox = Sandbox.detect()
        instance_name = sandbox.agent_address
    except Exception:
        instance_name = f"aq-{socket.gethostname()}-{os.getpid()}"

    return mdns_register(
        instance_name=instance_name,
        broadcast=broadcast,
        on_receive=on_receive,
    )
