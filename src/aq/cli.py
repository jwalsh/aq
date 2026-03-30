"""aq CLI — announce, check, status."""
from __future__ import annotations
import argparse, json, sys
from .sb import Sandbox
from .protocol import Broadcast, announce, read_active
from .conflict import check_conflicts
from .mesh import mesh_broadcast, is_enabled as mesh_is_enabled
from .mqtt import mqtt_publish, is_enabled as mqtt_is_enabled
from .kbfs import kbfs_publish, is_enabled as kbfs_is_enabled
from .ghissue import ghissue_publish, is_enabled as ghissue_is_enabled
from .irc import irc_publish, is_enabled as irc_is_enabled


def _fanout(broadcast: Broadcast, args: argparse.Namespace) -> list[str]:
    """Best-effort fanout to all enabled transports. Returns list of successes."""
    sent = []

    if getattr(args, "mesh", False) or mesh_is_enabled():
        via = getattr(args, "mesh_via", "serial")
        if mesh_broadcast(broadcast, via=via):
            sent.append("mesh")

    if getattr(args, "mqtt", False) or mqtt_is_enabled():
        if mqtt_publish(broadcast, subtopic="announce"):
            sent.append("mqtt")

    if getattr(args, "kbfs", False) or kbfs_is_enabled():
        if kbfs_publish(broadcast):
            sent.append("kbfs")

    if getattr(args, "ghissue", False) or ghissue_is_enabled():
        if ghissue_publish(broadcast):
            sent.append("ghissue")

    if getattr(args, "irc", False) or irc_is_enabled():
        if irc_publish(broadcast):
            sent.append("irc")

    return sent


def cmd_announce(args: argparse.Namespace) -> int:
    sb = Sandbox.detect()
    broadcast = Broadcast(
        agent=sb.agent_address,
        worktree=sb.branch,
        conjecture_id=args.conjecture,
        conjecture_claim=args.claim or f"working on {args.conjecture}",
        phase=args.phase,
        status=args.status,
        files=[f.strip() for f in args.files.split(",") if f.strip()],
        ttl=args.ttl,
    )
    path = announce(broadcast, args.channel)
    sent = _fanout(broadcast, args)

    if args.json:
        print(broadcast.to_json())
    else:
        suffix = f" (+ {', '.join(sent)})" if sent else ""
        print(f"announced: {broadcast.conjecture_id} \u2192 {path.name}{suffix}")
    return 0


def cmd_check(args: argparse.Namespace) -> int:
    sb = Sandbox.detect()
    files = [f.strip() for f in args.files.split(",") if f.strip()] if args.files else []
    me = Broadcast(
        agent=sb.agent_address, worktree=sb.branch,
        conjecture_id=args.conjecture or "C-?",
        conjecture_claim="", phase=args.phase,
        status="prosecuting", files=files,
    )
    signals = check_conflicts(me, args.channel)
    if not signals:
        print("no conflicts detected")
        return 0
    for signal in signals:
        print(signal.summary())
    return 1 if any(s.severity == "high" for s in signals) else 0


def cmd_status(args: argparse.Namespace) -> int:
    active = read_active(args.channel)
    if args.json:
        print(json.dumps([json.loads(b.to_json()) for b in active], indent=2))
        return 0
    if not active:
        print("no active broadcasts")
        return 0
    for broadcast in active:
        files = broadcast.files if isinstance(broadcast.files, list) else []
        print(f"  {broadcast.agent:50s}  {broadcast.conjecture_id}  [{broadcast.phase}]  {', '.join(files)}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(prog="aq", description="ambient agent queue")
    parser.add_argument("--channel", default="broadcast")
    parser.add_argument("--json", action="store_true")
    sub = parser.add_subparsers(dest="cmd")

    ann = sub.add_parser("announce", aliases=["ann", "a"])
    ann.add_argument("--conjecture", "-c", required=True)
    ann.add_argument("--files", "-f", default="")
    ann.add_argument("--claim", default="")
    ann.add_argument("--phase", default="proof",
                     choices=["conjecture", "proof", "refutation", "refinement"])
    ann.add_argument("--status", default="prosecuting",
                     choices=["prosecuting", "done", "blocked"])
    ann.add_argument("--ttl", type=int, default=3600)
    ann.add_argument("--mesh", action="store_true",
                     help="also broadcast via Meshtastic radio")
    ann.add_argument("--mesh-via", default="serial",
                     choices=["serial", "mqtt"],
                     help="mesh transport (default: serial)")
    ann.add_argument("--mqtt", action="store_true",
                     help="also publish to MQTT broker")
    ann.add_argument("--kbfs", action="store_true",
                     help="also write to Keybase filesystem")
    ann.add_argument("--ghissue", action="store_true",
                     help="also comment on GH issue (noisy, POC only)")
    ann.add_argument("--irc", action="store_true",
                     help="also broadcast via IRC channel")

    chk = sub.add_parser("check")
    chk.add_argument("--conjecture", "-c", default=None)
    chk.add_argument("--files", "-f", default="")
    chk.add_argument("--phase", default="proof")

    sub.add_parser("status", aliases=["ls"])

    args = parser.parse_args()
    if args.cmd in ("announce", "ann", "a"):
        return cmd_announce(args)
    elif args.cmd == "check":
        return cmd_check(args)
    elif args.cmd in ("status", "ls"):
        return cmd_status(args)
    else:
        parser.print_help()
        return 1


if __name__ == "__main__":
    sys.exit(main())
