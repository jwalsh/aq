# aq Postgres Transport

Tier 2 transport for aq using PostgreSQL LISTEN/NOTIFY as the pub/sub layer.

**Postgres is NOT required.** The filesystem transport (Tier 0) is always
available and is the only required transport. This contrib module is for
teams that already run Postgres and want real-time broadcast delivery
without filesystem polling.

## Why Postgres?

Postgres LISTEN/NOTIFY is a built-in pub/sub mechanism. When a broadcast
is INSERTed, a trigger fires `pg_notify('aq_broadcast', payload)`, and all
connected listeners receive it immediately. This gives sub-millisecond
delivery without polling, using infrastructure you likely already have.

The `active_broadcasts` view handles TTL filtering via a generated
`expires_at` column, so expired broadcasts are filtered lazily (no
cleanup daemon needed).

## Setup

### 1. Create the database

```bash
createdb aq
```

### 2. Apply the schema

```bash
psql aq < contrib/postgres/schema.sql
```

This creates:
- `broadcasts` table with all aq wire format fields
- `expires_at` generated column (ts + ttl interval)
- `active_broadcasts` view (filters expired rows)
- `notify_aq_broadcast()` trigger (fires NOTIFY on INSERT)
- `insert_broadcast(jsonb)` convenience function

### 3. Set the connection string

```bash
export AQ_POSTGRES_URL='postgres://localhost:5432/aq?sslmode=disable'
```

### 4. Run the demo

```bash
go run contrib/postgres/aq-pg.go
```

This publishes a test broadcast, listens for the NOTIFY, queries active
broadcasts, and runs conflict detection.

## How It Works

### Channel Interface

The `PostgresChannel` implements the three-method Channel interface from
the transport research doc:

```go
type Channel interface {
    Publish(broadcast Broadcast) error
    Subscribe(ctx context.Context) <-chan Broadcast
    Active() ([]Broadcast, error)
}
```

- **Publish**: INSERTs a row into `broadcasts`. The trigger fires
  `pg_notify('aq_broadcast', payload)` automatically.
- **Subscribe**: Uses `LISTEN aq_broadcast` via lib/pq. Returns a Go
  channel that emits broadcasts as they arrive.
- **Active**: Queries the `active_broadcasts` view (WHERE expires_at > now()).

### Wire Format

The `broadcasts` table stores each field from aq's Broadcast struct as a
typed column, plus the full JSON in a `payload` JSONB column. This allows
both structured queries (e.g., "all broadcasts touching auth.py") and
pass-through of the original wire format.

### TTL and Expiry

The `expires_at` column is a Postgres generated column:

```sql
expires_at TIMESTAMP GENERATED ALWAYS AS
    (to_timestamp(ts) + make_interval(secs => ttl)) STORED
```

The `active_broadcasts` view filters by `expires_at > now()`. Expired
broadcasts remain in the table until explicitly purged. Gossip is lazy
about cleanup -- stale rows are harmless.

### Conflict Detection

File overlap detection uses the `files TEXT[]` column with GIN index:

```sql
-- Find broadcasts touching the same files as a given broadcast
SELECT * FROM active_broadcasts
WHERE files && ARRAY['auth.py', 'models.py']
  AND agent != 'origin/feat-auth';
```

## Architecture: Tier System

```
Tier 0: Filesystem (~/.aq/channels/broadcast/)
        Always available. No dependencies. The ground truth.

Tier 2: Postgres (this module)
        Optional overlay. Real-time NOTIFY delivery.
        Requires a running Postgres instance.
```

In a multi-channel setup, both tiers publish simultaneously. Reads
deduplicate by broadcast ID. If Postgres is down, the filesystem
transport continues working. Gossip tolerates transport failure.

## Maintenance

Expired broadcasts accumulate in the table. To purge them:

```sql
-- Delete broadcasts expired more than 24 hours ago
DELETE FROM broadcasts WHERE expires_at < now() - interval '24 hours';
```

Or use pg_cron for automatic cleanup:

```sql
SELECT cron.schedule('aq-cleanup', '0 * * * *',
    $$DELETE FROM broadcasts WHERE expires_at < now() - interval '24 hours'$$);
```

## Dependencies

This module requires `github.com/lib/pq` for Postgres LISTEN/NOTIFY
support. It is NOT included in aq's go.mod because this is a contrib
module, not a core dependency. To build:

```bash
cd contrib/postgres
go mod init aq-postgres
go mod tidy
go run aq-pg.go
```
