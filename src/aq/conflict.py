"""aq conflict detection."""
from __future__ import annotations
from dataclasses import dataclass
from .protocol import Broadcast, read_active


@dataclass
class ConflictSignal:
    a: Broadcast
    b: Broadcast
    shared_files: list[str]
    severity: str  # low | medium | high

    def summary(self) -> str:
        files = ", ".join(self.shared_files)
        return (
            f"[{self.severity.upper()}] {self.a.agent} ({self.a.conjecture_id})"
            f" \u2194 {self.b.agent} ({self.b.conjecture_id})"
            f" \u2014 shared: {files}"
        )


def check_conflicts(
    me: Broadcast,
    channel: str = "broadcast",
) -> list[ConflictSignal]:
    signals = []
    for other in read_active(channel):
        if other.agent == me.agent:
            continue
        shared = list(set(me.files) & set(other.files))
        if not shared:
            continue
        both_proof = me.phase == "proof" and other.phase == "proof"
        one_proof = me.phase == "proof" or other.phase == "proof"
        severity = "high" if both_proof else ("medium" if one_proof else "low")
        signals.append(ConflictSignal(a=me, b=other, shared_files=shared, severity=severity))
    return sorted(signals, key=lambda s: {"high": 0, "medium": 1, "low": 2}[s.severity])
