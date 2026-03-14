"""cprr — conjecture/proof/refutation/refinement tracking."""
from __future__ import annotations
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional


class Phase(str, Enum):
    CONJECTURE = "conjecture"
    PROOF = "proof"
    REFUTATION = "refutation"
    REFINEMENT = "refinement"


class Status(str, Enum):
    OPEN = "open"
    PROSECUTING = "prosecuting"
    REFUTED = "refuted"
    REFINED = "refined"


@dataclass
class Conjecture:
    id: str
    claim: str
    phase: Phase = Phase.CONJECTURE
    status: Status = Status.OPEN
    files: list[str] = field(default_factory=list)
    agent: Optional[str] = None

    def is_compatible_with(self, other: "Conjecture") -> bool:
        return not bool(set(self.files) & set(other.files))

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "claim": self.claim,
            "phase": self.phase.value,
            "status": self.status.value,
            "files": self.files,
            "agent": self.agent,
        }
