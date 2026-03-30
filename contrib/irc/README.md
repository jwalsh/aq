# aq-irc: IRC Transport Plugin

Raw RFC 1459 IRC transport for aq broadcasts. Stdlib only, no external
dependencies. Implements the wire format from `irc-transport.org`.

## Prerequisites

A running IRC server. [miniircd](https://github.com/jrosdahl/miniircd)
is the recommended zero-config option:

```bash
pip install miniircd
miniircd --listen 0.0.0.0 --ports 6999
```

## Build

Not compiled into the main `aq` binary (`//go:build ignore`). Run directly:

```bash
go run contrib/irc/irc.go [flags]
```

Or build a standalone binary:

```bash
go build -o aq-irc contrib/irc/irc.go
```

## Modes

### Publish (TX)

Send a single broadcast as an IRC PRIVMSG in AMTP compact format, then
disconnect. Fire-and-forget, matching aq's lossy-ok semantics.

```bash
go run contrib/irc/irc.go -publish \
    -server localhost:6999 \
    -agent jwalsh/main \
    -conjecture C-42 \
    -phase proof \
    -files "cli.py,protocol.py"
```

What the channel sees:

```
< aq-12345> aq:jwalsh/main C-42 [proof] cli.py,protocol.py
```

### Subscribe (RX)

Long-running listener that joins the channel, parses incoming aq
broadcasts, and materializes them as JSON files in
`~/.aq/channels/broadcast/requests/`. Handles PING/PONG keepalive
and reconnects on disconnect.

```bash
go run contrib/irc/irc.go -subscribe \
    -server localhost:6999 \
    -channel "#aq-presence"
```

Received broadcasts appear as:

```
~/.aq/channels/broadcast/requests/aq-1711612800000-1711612800000.json
```

Each file contains a full Broadcast JSON matching `aq status` output:

```json
{
  "id": "1711612800000",
  "agent": "jwalsh/main",
  "worktree": "main",
  "conjecture_id": "C-42",
  "conjecture_claim": "(received via irc)",
  "phase": "proof",
  "status": "prosecuting",
  "files": ["cli.py", "protocol.py"],
  "ts": 1711612800.0,
  "ttl": 300
}
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-server` | `localhost:6999` | IRC server address (host:port) |
| `-channel` | `#aq-presence` | IRC channel |
| `-nick` | `aq-{pid}` | IRC nickname |
| `-publish` | false | TX mode |
| `-subscribe` | false | RX mode |
| `-agent` | (required for TX) | Agent address (e.g. `jwalsh/main`) |
| `-conjecture` | `C-0` | Conjecture ID |
| `-claim` | | Human-readable intent |
| `-phase` | `conjecture` | CPRR phase |
| `-status` | `prosecuting` | Work status |
| `-files` | | Comma-separated file list |

## Wire Format

The AMTP compact format used over IRC:

```
aq:{agent} {conjecture_id} [{phase}] {files}
```

Examples:

```
aq:jwalsh/main C-42 [proof] cli.py,protocol.py
aq:blake/feat-irc C-6 [conjecture] irc.py
aq:agent-3/fix-auth C-1 [refutation]
```

Under 80 bytes per message. Human-readable in any IRC client.

## Deduplication

The subscriber deduplicates incoming broadcasts using a composite key
of `agent + conjecture_id + phase`. If a non-expired broadcast with
the same key already exists in the requests directory, the write is
skipped. This prevents duplicates when an agent broadcasts to both
the filesystem and IRC simultaneously.

## Failure Modes

| Condition | Behavior |
|-----------|----------|
| Server not running | Exit with error (publish) or retry in 5s (subscribe) |
| Connection dropped | Reconnect after 5s (subscribe) |
| Malformed PRIVMSG | Skip silently |
| Nick collision | Use PID-based nick to avoid |
| PING timeout | Server disconnects; subscriber reconnects |

## Integration

Set environment variables to use with `aq announce --irc`:

```bash
export AQ_IRC=1
export AQ_IRC_HOST=localhost
export AQ_IRC_PORT=6999
export AQ_IRC_CHANNEL="#aq-presence"
```

Or add to `~/.aq/config.json`:

```json
{
  "irc": {
    "enabled": true,
    "host": "localhost",
    "port": 6999,
    "channel": "#aq-presence",
    "nick": "aq-nexus"
  }
}
```

## Watching from an IRC Client

The point of the IRC transport: join `#aq-presence` in irssi, weechat,
or anything else and watch agents work in real time.

```
14:23 < aq-12345> aq:jwalsh/main C-42 [proof] cli.py,protocol.py
14:25 < aq-67890> aq:blake/feat-irc C-6 [conjecture] irc.py
14:30 < aq-11111> aq:jwalsh/main C-42 [proof] cli.py,irc.py
```

Two agents touching `cli.py`. Conflict visible to the naked eye.
