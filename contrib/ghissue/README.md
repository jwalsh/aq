# ghissue — GitHub Issues transport for aq

Tier 4 transport. Human-readable on GitHub. Rate-limited. For when
everything else is down.

Posts aq broadcasts as comments on a dedicated GitHub issue. Polls the
same issue for incoming broadcasts from other agents. Zero dependencies
beyond `gh` (GitHub CLI).

## Prerequisites

- [GitHub CLI](https://cli.github.com/) installed and authenticated (`gh auth login`)
- A dedicated issue on the target repo for aq gossip traffic

### Create the gossip issue

```bash
gh issue create -R jwalsh/aq --title "aq gossip channel" \
    --body "Machine-readable aq broadcast thread. Do not close."
```

Note the issue number for the `-issue` flag.

## Usage

### Publish (TX)

Post a broadcast as an issue comment:

```bash
go run ghissue.go -repo jwalsh/aq -issue 42 -publish \
    -agent origin/feat-auth \
    -conjecture C-1 \
    -claim "replacing session tokens with OAuth2 flow" \
    -phase proof \
    -files "auth.py,session.py"
```

The comment body is the broadcast JSON, parseable by the subscriber.

### Subscribe (RX)

Poll the issue for new broadcasts and write them to the local aq channel:

```bash
go run ghissue.go -repo jwalsh/aq -issue 42 -subscribe
```

New broadcasts appear in `~/.aq/channels/broadcast/requests/aq-<id>.json`.

### Options

| Flag             | Default        | Description                              |
|------------------|----------------|------------------------------------------|
| `-repo`          | (required)     | GitHub repository (`owner/repo`)         |
| `-issue`         | (required)     | Issue number for the gossip thread       |
| `-publish`       | false          | TX mode: post broadcast as comment       |
| `-subscribe`     | false          | RX mode: poll comments, write locally    |
| `-poll-interval` | 30             | Seconds between polls (subscribe mode)   |
| `-agent`         | (required for TX) | Agent address (e.g. `origin/feat-auth`)  |
| `-conjecture`    | `C-0`          | Conjecture ID                            |
| `-claim`         | (empty)        | Conjecture claim in plain language       |
| `-phase`         | `conjecture`   | CPRR phase                               |
| `-status`        | `prosecuting`  | Broadcast status                         |
| `-files`         | (empty)        | Comma-separated file list                |
| `-ttl`           | 3600           | TTL in seconds                           |

## Rate limits

GitHub API allows 5000 requests/hour for authenticated users. At the
default 30-second poll interval, the subscriber makes ~120 requests/hour
(2.4% of quota). Adjust `-poll-interval` if running multiple subscribers
against the same repo.

## How it works

**TX path**: Marshals the broadcast to JSON, posts it as an issue comment
via `gh issue comment`. The comment is the broadcast — no encoding, no
wrapping. Human-readable in the GitHub UI.

**RX path**: On startup, fetches all existing comments to build a seen-set
(no replay on restart). Then polls on the configured interval. Each poll
fetches all comments, parses bodies as broadcast JSON, skips known IDs,
and writes new broadcasts to the local filesystem channel. Non-JSON
comments (human discussion) are silently ignored.

## Limitations

- **Latency**: Minimum 30s poll interval. Not for real-time.
- **Rate limits**: GitHub API quota applies. One subscriber per repo is fine.
  Ten is pushing it.
- **No deletion**: Broadcasts persist as issue comments forever. TTL expiry
  is local-only (the subscriber writes the file; aq's normal TTL pruning
  handles expiry).
- **Pagination**: Large threads (1000+ comments) will slow down polls.
  Consider closing and rotating the gossip issue periodically.
- **Authentication**: Requires `gh auth login`. No anonymous access.

## Why

When the filesystem is on a different machine, MQTT is down, mDNS doesn't
cross subnets, and UDP multicast is blocked by corporate firewalls... GitHub
Issues still works. Every developer already has `gh` installed. The issue
thread is auditable, human-readable, and backed by GitHub's infrastructure.

Tier 4: the cockroach of transports.
