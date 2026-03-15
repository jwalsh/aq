"""aq protocol — broadcast payload and channel I/O."""
from __future__ import annotations
import json, time, os, random, string
from dataclasses import dataclass, field, asdict
from pathlib import Path

AQ_HOME = Path(os.environ.get("AQ_HOME", Path.home() / ".aq"))
DEFAULT_TTL = 3600


def ulid() -> str:
    ts = format(int(time.time() * 1000), "012x")
    rand = "".join(random.choices(string.ascii_lowercase + string.digits, k=10))
    return f"{ts}{rand}"


@dataclass
class Broadcast:
    """
    Ambient presence payload.

    Lifecycle:
      1. announce() before touching files
      2. re-announce every TTL/2 while working
      3. announce(status="done") when finished
    """
    agent: str
    worktree: str
    conjecture_id: str
    conjecture_claim: str
    phase: str
    status: str
    files: list[str]
    ts: float = field(default_factory=time.time)
    ttl: int = DEFAULT_TTL
    id: str = field(default_factory=ulid)

    def is_expired(self) -> bool:
        return time.time() > self.ts + self.ttl

    def overlaps(self, other: "Broadcast") -> bool:
        return bool(set(self.files) & set(other.files))

    def to_json(self) -> str:
        return json.dumps(asdict(self))

    @classmethod
    def from_json(cls, s: str) -> "Broadcast":
        return cls(**json.loads(s))


def channel_path(channel: str = "broadcast") -> Path:
    return AQ_HOME / "channels" / channel


def announce(broadcast: Broadcast, channel: str = "broadcast") -> Path:
    requests = channel_path(channel) / "requests"
    requests.mkdir(parents=True, exist_ok=True)
    ts = format(int(broadcast.ts), "014d")
    path = requests / f"aq-{ts}-{broadcast.id}.json"
    path.write_text(broadcast.to_json() + "\n")
    return path


def read_active(channel: str = "broadcast") -> list[Broadcast]:
    requests = channel_path(channel) / "requests"
    if not requests.exists():
        return []
    out: list[Broadcast] = []
    for f in sorted(requests.glob("aq-*.json")):
        try:
            broadcast = Broadcast.from_json(f.read_text().strip())
            if broadcast.is_expired():
                archive = channel_path(channel) / "archive"
                archive.mkdir(exist_ok=True)
                f.rename(archive / f.name)
            else:
                out.append(broadcast)
        except Exception:
            pass
    return out
