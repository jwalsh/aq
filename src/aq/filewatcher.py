"""aq file-watcher RX adapter — detect filesystem broadcasts, dedup, deliver.

Closes the loop on the "disk is the universal bus" insight from
transport-mounts.org. The TX side fans out from filesystem to N
transports. This module is the RX side: watch the filesystem for new
broadcast files and push them to callbacks or webhooks.

The key insight: if ~/.aq/ sits on NFS, SMB, KBFS, syncthing, or any
shared/synced filesystem, writes from remote agents appear as new local
files. The filesystem itself IS the transport. No protocol needed —
just a file watcher.

Watcher backends (selected automatically by platform):
  - kqueue:   macOS / FreeBSD (no polling, kernel event)
  - inotify:  Linux (via inotify_simple or watchdog, kernel event)
  - polling:  fallback (os.scandir every N seconds)

Delivery modes:
  - callback:  in-process function invocation
  - webhook:   HTTP POST to a URL (e.g. Claude Code channel endpoint)
  - journal:   append to ~/.aq/state/rx-journal.jsonl (always, for audit)

Configuration:
  AQ_RX_WEBHOOK    URL to POST new broadcasts to
  AQ_RX_POLL_SEC   Polling interval in seconds (fallback watcher, default 2)
  AQ_RX_CHANNELS   Comma-separated channel names to watch (default: broadcast)
  AQ_HOME          Override ~/.aq root (default: ~/.aq)

Usage:
  # In-process, blocking:
  from aq.filewatcher import watch
  watch("broadcast", callback=lambda b: print(b.to_json()))

  # POST to webhook, blocking:
  from aq.filewatcher import watch_and_post
  watch_and_post("broadcast", "http://localhost:8080/aq/rx")

  # Daemon mode, multiple channels:
  from aq.filewatcher import watch_daemon
  watch_daemon(["broadcast", "private"], "http://localhost:8080/aq/rx")

  # CLI:
  python -m aq.filewatcher --channel broadcast --webhook http://localhost:8080/aq/rx
"""
from __future__ import annotations

import json
import logging
import os
import platform
import signal
import struct
import sys
import time
import urllib.error
import urllib.request
from dataclasses import asdict
from pathlib import Path
from threading import Thread, Event
from typing import Callable, Protocol

from .protocol import AQ_HOME, Broadcast, channel_path

logger = logging.getLogger("aq.filewatcher")

# --- Constants ---

DEFAULT_POLL_INTERVAL_SECONDS = 2.0
CHECKPOINT_FILENAME = "rx-checkpoint.json"
JOURNAL_FILENAME = "rx-journal.jsonl"
WEBHOOK_TIMEOUT_SECONDS = 10
WEBHOOK_MAX_RETRIES = 2
FILE_SETTLE_DELAY_SECONDS = 0.05  # wait for partial writes to finish
MAX_BROADCAST_SIZE_BYTES = 65536  # reject files larger than 64KB


# --- Types ---

BroadcastCallback = Callable[[Broadcast, Path], None]


class WatcherBackend(Protocol):
    """Interface for platform-specific filesystem watchers."""

    def watch(
        self,
        directory: Path,
        on_new_file: Callable[[Path], None],
        stop_event: Event,
    ) -> None:
        """Block until stop_event is set, calling on_new_file for each new file."""
        ...


# --- Checkpoint / Resume ---

def _state_dir() -> Path:
    """Return ~/.aq/state/, creating it if needed."""
    state_directory = AQ_HOME / "state"
    state_directory.mkdir(parents=True, exist_ok=True)
    return state_directory


def _load_checkpoint(channel: str) -> dict:
    """Load the last-processed checkpoint for a channel.

    Returns dict with 'last_mtime' (float) and 'seen_ids' (set of str).
    On first run or corrupt checkpoint, returns epoch + empty set.
    """
    checkpoint_path = _state_dir() / f"rx-checkpoint-{channel}.json"
    if not checkpoint_path.exists():
        return {"last_mtime": 0.0, "seen_ids": set()}
    try:
        data = json.loads(checkpoint_path.read_text())
        return {
            "last_mtime": float(data.get("last_mtime", 0.0)),
            "seen_ids": set(data.get("seen_ids", [])),
        }
    except (json.JSONDecodeError, ValueError, KeyError) as exc:
        logger.warning("corrupt checkpoint for channel %s, resetting: %s", channel, exc)
        return {"last_mtime": 0.0, "seen_ids": set()}


def _save_checkpoint(channel: str, last_mtime: float, seen_ids: set[str]) -> None:
    """Persist checkpoint so we can resume after restart.

    We keep only the most recent 10000 IDs to bound memory and disk.
    Older IDs are safe to drop — they would be TTL-expired anyway.
    """
    checkpoint_path = _state_dir() / f"rx-checkpoint-{channel}.json"
    # Keep bounded set of recent IDs
    truncated_ids = sorted(seen_ids)[-10000:]
    payload = json.dumps({
        "last_mtime": last_mtime,
        "seen_ids": truncated_ids,
        "updated_at": time.time(),
    })
    # Atomic write via rename
    tmp_path = checkpoint_path.with_suffix(".tmp")
    tmp_path.write_text(payload)
    tmp_path.rename(checkpoint_path)


# --- Journal ---

def _append_journal(broadcast: Broadcast, source_path: Path, channel: str) -> None:
    """Append a received broadcast to the RX journal (JSONL).

    The journal is the audit trail of everything the watcher has processed.
    One line per broadcast, with reception metadata.
    """
    journal_path = _state_dir() / JOURNAL_FILENAME
    entry = {
        "received_at": time.time(),
        "channel": channel,
        "source_file": str(source_path),
        "broadcast": asdict(broadcast),
    }
    try:
        with open(journal_path, "a") as journal_file:
            journal_file.write(json.dumps(entry) + "\n")
    except OSError as exc:
        logger.warning("failed to write journal: %s", exc)


# --- Broadcast Validation ---

def _validate_and_parse(file_path: Path) -> Broadcast | None:
    """Read a file and attempt to parse it as a valid Broadcast.

    Returns None on any failure (partial write, bad JSON, missing fields,
    oversized file, injected garbage). Never raises.
    """
    try:
        # Size check before reading — reject obviously bogus files
        file_stat = file_path.stat()
        if file_stat.st_size == 0:
            logger.debug("skipping empty file: %s", file_path.name)
            return None
        if file_stat.st_size > MAX_BROADCAST_SIZE_BYTES:
            logger.warning("rejecting oversized file (%d bytes): %s", file_stat.st_size, file_path.name)
            return None

        raw_content = file_path.read_text(encoding="utf-8")
    except FileNotFoundError:
        # File disappeared between detection and read (archived by another reader)
        logger.debug("file vanished before read: %s", file_path.name)
        return None
    except (OSError, UnicodeDecodeError) as exc:
        logger.debug("cannot read file %s: %s", file_path.name, exc)
        return None

    # Strip whitespace — broadcast files end with \n per protocol.py
    raw_content = raw_content.strip()
    if not raw_content:
        return None

    try:
        data = json.loads(raw_content)
    except json.JSONDecodeError as exc:
        # Partial write on NFS: file exists but JSON is incomplete.
        # Log at debug — this is expected during write races.
        logger.debug("invalid JSON in %s (partial write?): %s", file_path.name, exc)
        return None

    if not isinstance(data, dict):
        logger.debug("JSON is not an object in %s", file_path.name)
        return None

    # Validate required fields. The Broadcast dataclass requires these.
    required_field_names = {
        "agent", "worktree", "conjecture_id", "conjecture_claim",
        "phase", "status", "files",
    }
    missing_fields = required_field_names - set(data.keys())
    if missing_fields:
        logger.debug(
            "missing required fields in %s: %s",
            file_path.name, ", ".join(sorted(missing_fields)),
        )
        return None

    # Reject unknown fields that are not part of the Broadcast schema.
    # This prevents injection of arbitrary data via crafted JSON files.
    allowed_field_names = required_field_names | {"ts", "ttl", "id"}
    unknown_fields = set(data.keys()) - allowed_field_names
    if unknown_fields:
        logger.warning(
            "rejecting broadcast with unknown fields in %s: %s",
            file_path.name, ", ".join(sorted(unknown_fields)),
        )
        return None

    # Validate field types
    if not isinstance(data.get("files"), list):
        logger.debug("'files' is not a list in %s", file_path.name)
        return None
    if not isinstance(data.get("agent"), str):
        logger.debug("'agent' is not a string in %s", file_path.name)
        return None

    try:
        broadcast = Broadcast.from_json(raw_content)
    except (TypeError, KeyError, ValueError) as exc:
        logger.debug("cannot construct Broadcast from %s: %s", file_path.name, exc)
        return None

    # Sanity: broadcast ID must be non-empty
    if not broadcast.id:
        logger.debug("broadcast has empty ID in %s", file_path.name)
        return None

    return broadcast


# --- Webhook Delivery ---

def _post_to_webhook(webhook_url: str, broadcast: Broadcast, source_path: Path) -> bool:
    """POST broadcast JSON to a webhook URL.

    Returns True on 2xx response, False otherwise. Never raises.
    Retries once on transient failure (connection error, 5xx).
    """
    payload = broadcast.to_json().encode("utf-8")

    for attempt in range(1, WEBHOOK_MAX_RETRIES + 1):
        try:
            request = urllib.request.Request(
                webhook_url,
                data=payload,
                headers={
                    "Content-Type": "application/json",
                    "User-Agent": "aq-filewatcher/0.1",
                    "X-AQ-Source-File": source_path.name,
                    "X-AQ-Broadcast-ID": broadcast.id,
                },
                method="POST",
            )
            with urllib.request.urlopen(request, timeout=WEBHOOK_TIMEOUT_SECONDS) as response:
                if 200 <= response.status < 300:
                    logger.debug("webhook delivered: %s (attempt %d)", broadcast.id, attempt)
                    return True
                else:
                    logger.debug(
                        "webhook returned %d for %s (attempt %d)",
                        response.status, broadcast.id, attempt,
                    )
        except urllib.error.HTTPError as exc:
            status_code = exc.code
            if status_code >= 500 and attempt < WEBHOOK_MAX_RETRIES:
                logger.debug("webhook 5xx (%d), retrying: %s", status_code, broadcast.id)
                time.sleep(0.5)
                continue
            logger.debug("webhook HTTP error %d for %s", status_code, broadcast.id)
            return False
        except (urllib.error.URLError, OSError) as exc:
            if attempt < WEBHOOK_MAX_RETRIES:
                logger.debug("webhook connection error, retrying: %s", exc)
                time.sleep(0.5)
                continue
            logger.debug("webhook failed for %s: %s", broadcast.id, exc)
            return False

    return False


# --- Watcher Backends ---

class KqueueWatcher:
    """macOS/FreeBSD file watcher using kqueue.

    Watches a directory for new files via NOTE_WRITE on the directory fd.
    When the directory changes, we diff the current listing against the
    previously known set to find new files.
    """

    def watch(
        self,
        directory: Path,
        on_new_file: Callable[[Path], None],
        stop_event: Event,
    ) -> None:
        import select

        directory.mkdir(parents=True, exist_ok=True)
        dir_fd = os.open(str(directory), os.O_RDONLY)
        try:
            kq = select.kqueue()
            kevent = select.kevent(
                dir_fd,
                filter=select.KQ_FILTER_VNODE,
                flags=select.KQ_EV_ADD | select.KQ_EV_CLEAR,
                fflags=select.KQ_NOTE_WRITE,
            )

            known_files = {entry.name for entry in directory.iterdir() if entry.is_file()}

            while not stop_event.is_set():
                # Block for up to 1 second, then check stop_event
                events = kq.control([kevent], 1, 1.0)
                if events:
                    # Directory was modified — find new files
                    time.sleep(FILE_SETTLE_DELAY_SECONDS)
                    current_files = set()
                    try:
                        current_files = {
                            entry.name for entry in directory.iterdir()
                            if entry.is_file()
                        }
                    except OSError:
                        continue

                    new_file_names = current_files - known_files
                    for file_name in sorted(new_file_names):
                        if file_name.endswith(".json"):
                            on_new_file(directory / file_name)
                    known_files = current_files

            kq.close()
        finally:
            os.close(dir_fd)


class InotifyWatcher:
    """Linux file watcher using inotify_simple.

    Falls back to polling if inotify_simple is not installed.
    """

    def watch(
        self,
        directory: Path,
        on_new_file: Callable[[Path], None],
        stop_event: Event,
    ) -> None:
        try:
            import inotify_simple
        except ImportError:
            logger.info("inotify_simple not installed, falling back to polling")
            PollingWatcher().watch(directory, on_new_file, stop_event)
            return

        directory.mkdir(parents=True, exist_ok=True)
        inotify_fd = inotify_simple.INotify()
        watch_flags = (
            inotify_simple.flags.CREATE
            | inotify_simple.flags.MOVED_TO
            | inotify_simple.flags.CLOSE_WRITE
        )
        inotify_fd.add_watch(str(directory), watch_flags)

        seen_pending: set[str] = set()

        while not stop_event.is_set():
            # Timeout in ms — check stop_event periodically
            events = inotify_fd.read(timeout=1000)
            for event in events:
                file_name = event.name
                if not file_name or not file_name.endswith(".json"):
                    continue

                # CREATE fires before write completes. Wait for CLOSE_WRITE.
                if event.mask & inotify_simple.flags.CREATE:
                    seen_pending.add(file_name)
                    continue

                is_close_write = event.mask & inotify_simple.flags.CLOSE_WRITE
                is_moved_to = event.mask & inotify_simple.flags.MOVED_TO

                if is_close_write or is_moved_to:
                    seen_pending.discard(file_name)
                    file_path = directory / file_name
                    if file_path.exists():
                        on_new_file(file_path)

        inotify_fd.close()


class PollingWatcher:
    """Fallback watcher using os.scandir on an interval.

    Works everywhere. Higher latency than kernel watchers but correct.
    Detects files by comparing mtime against last poll time.
    """

    def __init__(self, interval_seconds: float | None = None):
        self.interval_seconds = interval_seconds or _poll_interval()

    def watch(
        self,
        directory: Path,
        on_new_file: Callable[[Path], None],
        stop_event: Event,
    ) -> None:
        directory.mkdir(parents=True, exist_ok=True)
        known_files: set[str] = {
            entry.name for entry in directory.iterdir() if entry.is_file()
        }

        while not stop_event.is_set():
            stop_event.wait(self.interval_seconds)
            if stop_event.is_set():
                break

            try:
                current_files: set[str] = set()
                for entry in os.scandir(directory):
                    if entry.is_file():
                        current_files.add(entry.name)
            except OSError:
                continue

            new_file_names = current_files - known_files
            for file_name in sorted(new_file_names):
                if file_name.endswith(".json"):
                    on_new_file(directory / file_name)
            known_files = current_files


def _poll_interval() -> float:
    """Get polling interval from environment or default."""
    try:
        return float(os.environ.get("AQ_RX_POLL_SEC", str(DEFAULT_POLL_INTERVAL_SECONDS)))
    except ValueError:
        return DEFAULT_POLL_INTERVAL_SECONDS


def _select_watcher_backend() -> WatcherBackend:
    """Select the best watcher backend for this platform.

    Preference: kqueue (macOS/FreeBSD) > inotify (Linux) > polling.
    """
    system_name = platform.system()

    if system_name in ("Darwin", "FreeBSD"):
        try:
            import select
            if hasattr(select, "kqueue"):
                logger.debug("using kqueue watcher backend")
                return KqueueWatcher()
        except ImportError:
            pass

    if system_name == "Linux":
        try:
            import inotify_simple  # noqa: F401
            logger.debug("using inotify watcher backend")
            return InotifyWatcher()
        except ImportError:
            logger.debug("inotify_simple not available, using polling")

    logger.debug("using polling watcher backend (interval=%.1fs)", _poll_interval())
    return PollingWatcher()


# --- Core Watch Loop ---

class ChannelWatcher:
    """Watch a single channel's requests directory for new broadcasts.

    Handles dedup, validation, checkpoint/resume, journal, and delivery
    to callback and/or webhook.
    """

    def __init__(
        self,
        channel: str,
        callback: BroadcastCallback | None = None,
        webhook_url: str | None = None,
        backend: WatcherBackend | None = None,
    ):
        self.channel = channel
        self.callback = callback
        self.webhook_url = webhook_url
        self.backend = backend or _select_watcher_backend()
        self.stop_event = Event()

        # Load checkpoint for resume
        checkpoint = _load_checkpoint(channel)
        self.seen_ids: set[str] = checkpoint["seen_ids"]
        self.last_mtime: float = checkpoint["last_mtime"]

        # Stats
        self.received_count: int = 0
        self.dedup_count: int = 0
        self.error_count: int = 0

    def _process_file(self, file_path: Path) -> None:
        """Process a single new broadcast file. Called by the watcher backend."""
        broadcast = _validate_and_parse(file_path)
        if broadcast is None:
            self.error_count += 1
            return

        # Dedup by broadcast ID
        if broadcast.id in self.seen_ids:
            self.dedup_count += 1
            logger.debug("dedup: already seen %s", broadcast.id)
            return

        # Skip expired broadcasts (stale files from before restart)
        if broadcast.is_expired():
            logger.debug("skipping expired broadcast: %s (age=%.0fs, ttl=%d)",
                         broadcast.id, time.time() - broadcast.ts, broadcast.ttl)
            self.seen_ids.add(broadcast.id)
            return

        # Mark as seen
        self.seen_ids.add(broadcast.id)
        self.received_count += 1

        logger.info(
            "rx: %s from %s [%s] %s",
            broadcast.conjecture_id,
            broadcast.agent,
            broadcast.phase,
            broadcast.id,
        )

        # Journal (always, for audit trail)
        _append_journal(broadcast, file_path, self.channel)

        # Callback delivery
        if self.callback is not None:
            try:
                self.callback(broadcast, file_path)
            except Exception as exc:
                logger.warning("callback error for %s: %s", broadcast.id, exc)

        # Webhook delivery
        if self.webhook_url:
            delivered = _post_to_webhook(self.webhook_url, broadcast, file_path)
            if not delivered:
                logger.warning("webhook delivery failed for %s", broadcast.id)

        # Update checkpoint
        try:
            file_mtime = file_path.stat().st_mtime
            self.last_mtime = max(self.last_mtime, file_mtime)
        except OSError:
            pass
        _save_checkpoint(self.channel, self.last_mtime, self.seen_ids)

    def _scan_backlog(self, requests_directory: Path) -> None:
        """On startup, process any files newer than our last checkpoint.

        This handles the case where the watcher died and files accumulated
        while it was down. Only processes files with mtime > last checkpoint.
        """
        if not requests_directory.exists():
            return

        backlog_count = 0
        try:
            for entry in sorted(requests_directory.glob("aq-*.json")):
                try:
                    file_mtime = entry.stat().st_mtime
                except OSError:
                    continue
                if file_mtime > self.last_mtime:
                    self._process_file(entry)
                    backlog_count += 1
        except OSError as exc:
            logger.warning("backlog scan failed: %s", exc)

        if backlog_count > 0:
            logger.info("processed %d backlog files for channel %s", backlog_count, self.channel)

    def run(self) -> None:
        """Start watching. Blocks until stop() is called."""
        requests_directory = channel_path(self.channel) / "requests"
        requests_directory.mkdir(parents=True, exist_ok=True)

        # Process backlog from downtime
        self._scan_backlog(requests_directory)

        logger.info(
            "watching %s (backend=%s, webhook=%s)",
            requests_directory,
            type(self.backend).__name__,
            self.webhook_url or "none",
        )

        # Delegate to platform-specific watcher
        try:
            self.backend.watch(requests_directory, self._process_file, self.stop_event)
        except Exception as exc:
            if not self.stop_event.is_set():
                logger.error("watcher crashed: %s", exc)
                raise

        # Final checkpoint on clean exit
        _save_checkpoint(self.channel, self.last_mtime, self.seen_ids)
        logger.info(
            "watcher stopped for %s (received=%d, dedup=%d, errors=%d)",
            self.channel, self.received_count, self.dedup_count, self.error_count,
        )

    def stop(self) -> None:
        """Signal the watcher to stop."""
        self.stop_event.set()


# --- Public API ---

def watch(
    channel: str = "broadcast",
    callback: BroadcastCallback | None = None,
) -> None:
    """Watch a channel for new broadcasts, calling callback for each.

    Blocks until interrupted (SIGINT/SIGTERM). Handles checkpoint/resume
    and journaling automatically.

    Args:
        channel: Channel name to watch (default: "broadcast").
        callback: Function(broadcast, file_path) called for each new broadcast.
                  If None, broadcasts are only journaled.
    """
    watcher = ChannelWatcher(channel=channel, callback=callback)

    def handle_signal(signum: int, frame: object) -> None:
        logger.info("received signal %d, stopping", signum)
        watcher.stop()

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    watcher.run()


def watch_and_post(
    channel: str = "broadcast",
    webhook_url: str = "",
    callback: BroadcastCallback | None = None,
) -> None:
    """Watch a channel and POST new broadcasts to a webhook URL.

    Blocks until interrupted. Optionally also calls a callback.

    Args:
        channel: Channel name to watch.
        webhook_url: URL to POST broadcast JSON to.
        callback: Optional additional callback.
    """
    if not webhook_url:
        webhook_url = os.environ.get("AQ_RX_WEBHOOK", "")
    if not webhook_url:
        raise ValueError("webhook_url required (or set AQ_RX_WEBHOOK)")

    watcher = ChannelWatcher(channel=channel, callback=callback, webhook_url=webhook_url)

    def handle_signal(signum: int, frame: object) -> None:
        watcher.stop()

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    watcher.run()


def watch_daemon(
    channels: list[str] | None = None,
    webhook_url: str = "",
    callback: BroadcastCallback | None = None,
) -> None:
    """Watch multiple channels in background threads. Blocks until interrupted.

    Each channel gets its own ChannelWatcher in a daemon thread.
    The main thread waits for SIGINT/SIGTERM and then stops all watchers.

    Args:
        channels: List of channel names. Defaults to ["broadcast"].
        webhook_url: Optional webhook URL for all channels.
        callback: Optional callback for all channels.
    """
    if channels is None:
        channel_names_env = os.environ.get("AQ_RX_CHANNELS", "broadcast")
        channels = [name.strip() for name in channel_names_env.split(",") if name.strip()]

    if not webhook_url:
        webhook_url = os.environ.get("AQ_RX_WEBHOOK", "")

    watchers: list[ChannelWatcher] = []
    threads: list[Thread] = []

    for channel_name in channels:
        channel_watcher = ChannelWatcher(
            channel=channel_name,
            callback=callback,
            webhook_url=webhook_url or None,
        )
        watchers.append(channel_watcher)

        watcher_thread = Thread(
            target=channel_watcher.run,
            name=f"aq-rx-{channel_name}",
            daemon=True,
        )
        threads.append(watcher_thread)

    shutdown_event = Event()

    def handle_signal(signum: int, frame: object) -> None:
        logger.info("received signal %d, stopping all watchers", signum)
        for channel_watcher in watchers:
            channel_watcher.stop()
        shutdown_event.set()

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    # Start all watcher threads
    for watcher_thread in threads:
        watcher_thread.start()

    logger.info("daemon started: watching %d channel(s): %s", len(channels), ", ".join(channels))

    # Wait for shutdown signal
    shutdown_event.wait()

    # Wait for threads to finish (they should stop quickly after stop_event is set)
    for watcher_thread in threads:
        watcher_thread.join(timeout=5.0)

    logger.info("daemon stopped")


# --- CLI ---

def _cli_main() -> int:
    """CLI entry point for standalone file watcher."""
    import argparse

    parser = argparse.ArgumentParser(
        prog="aq-filewatcher",
        description="aq file-watcher RX adapter — watch for broadcast files and deliver them",
    )
    parser.add_argument(
        "--channel", "-c",
        default="broadcast",
        help="channel to watch (default: broadcast)",
    )
    parser.add_argument(
        "--channels",
        help="comma-separated list of channels (daemon mode)",
    )
    parser.add_argument(
        "--webhook", "-w",
        default="",
        help="URL to POST new broadcasts to",
    )
    parser.add_argument(
        "--daemon", "-d",
        action="store_true",
        help="run in daemon mode (multiple channels, background threads)",
    )
    parser.add_argument(
        "--poll-interval",
        type=float,
        default=None,
        help="polling interval in seconds (fallback watcher)",
    )
    parser.add_argument(
        "--verbose", "-v",
        action="store_true",
        help="enable debug logging",
    )

    args = parser.parse_args()

    # Configure logging
    log_level = logging.DEBUG if args.verbose else logging.INFO
    logging.basicConfig(
        level=log_level,
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
        datefmt="%H:%M:%S",
    )

    if args.poll_interval is not None:
        os.environ["AQ_RX_POLL_SEC"] = str(args.poll_interval)

    if args.daemon or args.channels:
        channel_list = None
        if args.channels:
            channel_list = [name.strip() for name in args.channels.split(",")]
        watch_daemon(channels=channel_list, webhook_url=args.webhook)
    elif args.webhook:
        watch_and_post(channel=args.channel, webhook_url=args.webhook)
    else:
        # Default: watch and print to stdout
        def print_broadcast(broadcast: Broadcast, source_path: Path) -> None:
            print(f"[rx] {broadcast.conjecture_id} from {broadcast.agent} [{broadcast.phase}] {broadcast.id}")

        watch(channel=args.channel, callback=print_broadcast)

    return 0


if __name__ == "__main__":
    sys.exit(_cli_main())
