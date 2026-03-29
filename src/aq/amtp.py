"""AMTP — Amigosmalla Text Protocol parser and formatter.

Wire format: @AGENT|TYPE|SEQ|PAYLOAD (pipe-delimited, 200 byte max)
"""
from __future__ import annotations

import re
from dataclasses import dataclass

TYPES = {"HELO", "ACK", "PROP", "VOTE", "SYNC", "TASK", "DONE", "NOTE", "PING"}
MAX_PAYLOAD = 200
PATTERN = re.compile(r"^@([A-Z]{2,8})\|([A-Z]{2,4})\|(\d+)\|(.*)$")


@dataclass
class Message:
    agent: str
    type: str
    seq: int
    payload: str

    def encode(self) -> str:
        msg = f"@{self.agent}|{self.type}|{self.seq}|{self.payload}"
        if len(msg.encode("utf-8")) > MAX_PAYLOAD:
            raise ValueError(f"message too long: {len(msg.encode('utf-8'))}b > {MAX_PAYLOAD}b")
        return msg

    @classmethod
    def parse(cls, text: str) -> Message | None:
        text = text.strip()
        match = PATTERN.match(text)
        if not match:
            return None
        agent, msg_type, seq_str, payload = match.groups()
        if msg_type not in TYPES:
            return None
        return cls(agent=agent, type=msg_type, seq=int(seq_str), payload=payload)

    def is_proposal(self) -> bool:
        return self.type == "PROP"

    def is_vote(self) -> bool:
        return self.type == "VOTE"

    def vote_verdict(self) -> tuple[int, str, str] | None:
        """Parse VOTE payload: seq:+1;reason -> (ref_seq, verdict, reason)"""
        if not self.is_vote():
            return None
        try:
            ref, rest = self.payload.split(":", 1)
            verdict, reason = rest.split(";", 1)
            return int(ref), verdict.strip(), reason.strip()
        except (ValueError, AttributeError):
            return None

    def sync_state(self) -> dict[str, str] | None:
        """Parse SYNC payload: key=val,key=val -> dict"""
        if self.type != "SYNC":
            return None
        return dict(kv.split("=", 1) for kv in self.payload.split(",") if "=" in kv)


def helo(agent: str, seq: int, full_name: str, node: str, caps: str) -> Message:
    return Message(agent, "HELO", seq, f"{full_name};{node};{caps}")


def ack(agent: str, seq: int, ref_seq: int) -> Message:
    return Message(agent, "ACK", seq, str(ref_seq))


def prop(agent: str, seq: int, topic: str, content: str) -> Message:
    return Message(agent, "PROP", seq, f"{topic}:{content}")


def vote(agent: str, seq: int, ref_seq: int, verdict: str, reason: str) -> Message:
    return Message(agent, "VOTE", seq, f"{ref_seq}:{verdict};{reason}")


def sync(agent: str, seq: int, **kwargs: str) -> Message:
    payload = ",".join(f"{k}={v}" for k, v in kwargs.items())
    return Message(agent, "SYNC", seq, payload)


def ping(agent: str, seq: int, uptime: str, battery: str, nodes: str) -> Message:
    return Message(agent, "PING", seq, f"{uptime};{battery};{nodes}")


def note(agent: str, seq: int, text: str) -> Message:
    return Message(agent, "NOTE", seq, text)


def task(agent: str, seq: int, task_id: str, description: str) -> Message:
    return Message(agent, "TASK", seq, f"{task_id}:{description}")


def done(agent: str, seq: int, task_id: str, result: str) -> Message:
    return Message(agent, "DONE", seq, f"{task_id}:{result}")
