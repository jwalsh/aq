# Serf / memberlist Transport for aq

Tier 2.2 -- the first transport where aq's gossip semantics match the
transport's gossip semantics.

## Why This Fits

Every other aq transport *simulates* gossip over something else:

- Filesystem: gossip-over-shared-directory (poll or watch for new files)
- mDNS: gossip-over-multicast-DNS (abuse TXT records for payload)
- NATS: gossip-over-broker (pub/sub with a central daemon)
- Postgres: gossip-over-RDBMS (LISTEN/NOTIFY on a table)

HashiCorp [memberlist](https://github.com/hashicorp/memberlist) IS a
gossip protocol. It implements SWIM (Scalable Weakly-consistent
Infection-style Membership) -- protocol-level epidemic broadcast with
protocol periods, suspicion, and failure detection. Serf builds on
memberlist to add user events and queries.

aq broadcasts map directly to Serf user events. No impedance mismatch
on the *sending* side. The only mismatch: memberlist is ephemeral
(messages propagate and disappear), but `Active()` needs current state.
Solution: local in-memory cache populated by received events, pruned by
TTL. See `serf.go` for the sketch.

## Mapping

| aq concept       | Serf / memberlist concept     |
|------------------|-------------------------------|
| `aq announce`    | `serf.UserEvent`              |
| `aq status`      | `serf.Query` (request/reply)  |
| `aq check`       | Local cache lookup            |
| Broadcast TTL    | Event + local TTL pruning     |
| Agent identity   | memberlist `Node.Name` + tags |
| Conjecture/phase | Node tags (key-value metadata)|
| Conflict detect  | Compare tags on cached events |

## Impedance Mismatch: Ephemeral vs. Active()

memberlist propagates events but does not persist them. A node that
joins the cluster after an event was sent will never see it. aq's
`Active()` method needs to return the set of currently-active broadcasts.

The solution is a local TTL-pruned cache:
1. On event receive, insert into `map[string]Broadcast` keyed by agent.
2. On `Active()`, prune expired entries and return the remainder.
3. New nodes receive current state via node tags (conjecture, phase)
   plus a `serf.Query` on join to request recent broadcasts.

See `serf.go` for the implementation sketch.

## Files

| File          | Description                                              |
|---------------|----------------------------------------------------------|
| `README.md`   | This file.                                              |
| `config.toml` | Mock config for a 3-node aq deployment over memberlist. |
| `serf.go`     | Sketch of SerfChannel implementing the Channel interface.|
