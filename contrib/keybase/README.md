# Keybase/KBFS Transport for aq

KBFS is encrypted gossip over a shared filesystem. Each broadcast becomes a
JSON file in a Keybase team directory, E2E encrypted by Keybase's NaCl
sigchain, readable only by team members. No broker, no server -- just
`keybase fs write` and `keybase fs ls`.

This is the durable-artifact complement to the chat-based transport described
in `keybase-transport.org`. Chat handles live presence (push via `api-listen`);
KBFS handles persistence (poll via `fs ls`). Both coexist in the transport
mount system.

## Why KBFS Fits aq

| KBFS Concept         | aq Concept           | Why it maps                                  |
|----------------------|----------------------|----------------------------------------------|
| Team shared dir      | Broadcast channel    | All team members see the same directory.     |
| E2E encryption       | Identity-verified gossip | Sigchain-anchored, non-repudiable.        |
| File write           | `aq announce`        | One JSON file per broadcast.                 |
| File listing + read  | `aq status`          | Poll directory for active broadcasts.        |
| No daemon required   | Filesystem-first     | CLI commands only, no running service.       |

## Prerequisites

The `keybase` CLI must be installed and the user must be logged in:

```bash
keybase status   # verify logged in
keybase team list-memberships   # verify team access
```

## Usage

### Publish (TX)

Write a broadcast to the team's KBFS directory:

```bash
go run contrib/keybase/kbfs.go -publish -team amigosmalla \
    -agent origin/feat-auth \
    -conjecture C-1 \
    -claim "replacing session tokens with OAuth2 flow" \
    -phase proof \
    -files "auth.py,session.py"
```

This writes `aq-{ts}-{id}.json` to `/keybase/team/amigosmalla/aq/`.

### Subscribe (RX)

Poll the KBFS directory and ingest new broadcasts locally:

```bash
go run contrib/keybase/kbfs.go -subscribe -team amigosmalla
```

New broadcasts appear in `~/.aq/channels/broadcast/requests/`. The subscriber
deduplicates by broadcast ID and skips expired broadcasts (ts + ttl < now).

### Custom KBFS Path

For private conversations (not team directories):

```bash
# Publish to a private shared dir
go run contrib/keybase/kbfs.go -publish \
    -path /keybase/private/alice,bob/aq \
    -agent origin/main -conjecture C-7

# Subscribe from the same path
go run contrib/keybase/kbfs.go -subscribe \
    -path /keybase/private/alice,bob/aq
```

### Poll Interval

Default poll interval is 5 seconds. Adjust with `-poll-interval`:

```bash
go run contrib/keybase/kbfs.go -subscribe -team amigosmalla -poll-interval 10
```

## Flags

| Flag             | Default          | Description                                      |
|------------------|------------------|--------------------------------------------------|
| `-publish`       | false            | TX mode: write broadcast to KBFS                 |
| `-subscribe`     | false            | RX mode: poll KBFS and ingest locally             |
| `-team`          | (required*)      | Keybase team name                                 |
| `-path`          | (required*)      | KBFS path override (alternative to `-team`)       |
| `-poll-interval` | 5                | RX poll interval in seconds                       |
| `-agent`         | (required for TX)| Agent address (e.g. `origin/feat-auth`)           |
| `-conjecture`    | `C-0`            | Conjecture ID                                     |
| `-claim`         | `""`             | Plain language intent                             |
| `-phase`         | `conjecture`     | CPRR phase                                        |
| `-status`        | `prosecuting`    | Broadcast status                                  |
| `-files`         | `""`             | Comma-separated file list                         |
| `-ttl`           | 3600             | Broadcast TTL in seconds                          |

*One of `-team` or `-path` is required.

## KBFS Path Layout

```
/keybase/team/<team>/aq/
    aq-1711814400-1234567890123-0a1b.json
    aq-1711814460-1234567890456-c3d4.json
    ...
```

Each file is a complete aq Broadcast JSON payload matching the schema in `main.go`.

## Broadcast Lifecycle

1. Agent calls `-publish`. File lands in `/keybase/team/<team>/aq/`.
2. Keybase syncs the file to all team members' KBFS mounts.
3. Subscribers poll with `-subscribe`, detect new files, parse JSON.
4. Dedup by broadcast ID prevents re-ingestion across restarts.
5. TTL check skips stale broadcasts (ts + ttl in the past).
6. Valid broadcasts are written to `~/.aq/channels/broadcast/requests/`.

## Limitations

- **Polling, not push**: KBFS without FUSE has no filesystem events. The
  subscriber polls at the configured interval. Latency floor is the poll
  interval. For sub-second presence, use the chat transport instead.
- **No automatic cleanup**: Expired broadcasts remain on KBFS until manually
  deleted. A cron job or periodic `keybase fs rm` sweep is recommended.
- **Keybase CLI required**: All operations shell out to `keybase`. The CLI
  must be installed, logged in, and have access to the specified team.

## Conjectures

- **C-1** (filesystem-first): KBFS is filesystem semantics accessed via CLI.
  This transport validates that the filesystem-first constraint extends to
  encrypted remote filesystems.
- **C-3** (Wave presence without Wave data model): KBFS provides the durable
  artifact layer that chat's ephemeral messages cannot. Together they
  reconstruct Wave's presence + history without OT complexity.

## References

- [Keybase filesystem docs](https://book.keybase.io/docs/files)
- [keybase-transport.org](keybase-transport.org) -- full evaluation of Keybase surfaces for aq
- [MQTT transport](../mqtt/) -- sibling plugin following the same pattern
