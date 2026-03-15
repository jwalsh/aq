-- aq Postgres Transport Schema
--
-- Tier 2 transport: Postgres LISTEN/NOTIFY as a pub/sub layer for aq broadcasts.
-- Filesystem (Tier 0) is always required. This is an optional overlay.
--
-- Usage:
--   createdb aq
--   psql aq < schema.sql

-- ---------- Broadcasts table ----------

CREATE TABLE IF NOT EXISTS broadcasts (
    -- Wire format fields (must match aq.Broadcast struct exactly)
    id              TEXT PRIMARY KEY,
    agent           TEXT NOT NULL,
    worktree        TEXT NOT NULL,
    conjecture_id   TEXT NOT NULL,
    conjecture_claim TEXT NOT NULL DEFAULT '',
    phase           TEXT NOT NULL CHECK (phase IN ('conjecture', 'proof', 'refutation', 'refinement')),
    status          TEXT NOT NULL CHECK (status IN ('prosecuting', 'done', 'blocked')),
    files           TEXT[] NOT NULL DEFAULT '{}',
    ts              DOUBLE PRECISION NOT NULL,
    ttl             INTEGER NOT NULL DEFAULT 300,

    -- Derived / storage columns
    payload         JSONB NOT NULL,
    expires_at      TIMESTAMP GENERATED ALWAYS AS
                        (to_timestamp(ts) + make_interval(secs => ttl)) STORED,
    created_at      TIMESTAMP NOT NULL DEFAULT now()
);

-- Index for active broadcast queries (expires_at > now).
CREATE INDEX IF NOT EXISTS idx_broadcasts_expires_at ON broadcasts (expires_at);

-- Index for conflict detection (file overlap lookups).
CREATE INDEX IF NOT EXISTS idx_broadcasts_files ON broadcasts USING GIN (files);

-- Index for agent lookups.
CREATE INDEX IF NOT EXISTS idx_broadcasts_agent ON broadcasts (agent);

-- Index for conjecture lookups.
CREATE INDEX IF NOT EXISTS idx_broadcasts_conjecture ON broadcasts (conjecture_id);

-- ---------- Active broadcasts view ----------
--
-- Gossip should be lazy about cleanup. Instead of eagerly deleting expired
-- broadcasts, we define a view that filters them out. Expired rows remain
-- in the table until explicitly purged (see maintenance section below).

CREATE OR REPLACE VIEW active_broadcasts AS
    SELECT id, agent, worktree, conjecture_id, conjecture_claim,
           phase, status, files, ts, ttl, payload, expires_at
    FROM broadcasts
    WHERE expires_at > now();

-- ---------- NOTIFY trigger ----------
--
-- On every INSERT, fire a NOTIFY on channel 'aq_broadcast' with the
-- broadcast ID as the payload. Subscribers use LISTEN aq_broadcast to
-- receive real-time notifications without polling.

CREATE OR REPLACE FUNCTION notify_aq_broadcast()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('aq_broadcast', NEW.payload::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_notify_aq_broadcast ON broadcasts;
CREATE TRIGGER trg_notify_aq_broadcast
    AFTER INSERT ON broadcasts
    FOR EACH ROW
    EXECUTE FUNCTION notify_aq_broadcast();

-- ---------- Maintenance ----------
--
-- Optional: purge expired broadcasts older than 24 hours.
-- Run this periodically via pg_cron or a manual cron job.
-- Gossip is lazy about cleanup -- expired broadcasts are harmless,
-- just taking space. The view already filters them out.

-- DELETE FROM broadcasts WHERE expires_at < now() - interval '24 hours';

-- ---------- Convenience functions ----------

-- Insert a broadcast from a JSON payload (matches aq wire format).
CREATE OR REPLACE FUNCTION insert_broadcast(payload JSONB)
RETURNS TEXT AS $$
DECLARE
    broadcast_id TEXT;
BEGIN
    broadcast_id := payload->>'id';
    INSERT INTO broadcasts (
        id, agent, worktree, conjecture_id, conjecture_claim,
        phase, status, files, ts, ttl, payload
    ) VALUES (
        broadcast_id,
        payload->>'agent',
        payload->>'worktree',
        payload->>'conjecture_id',
        COALESCE(payload->>'conjecture_claim', ''),
        payload->>'phase',
        payload->>'status',
        ARRAY(SELECT jsonb_array_elements_text(COALESCE(payload->'files', '[]'::jsonb))),
        (payload->>'ts')::double precision,
        COALESCE((payload->>'ttl')::integer, 300),
        payload
    )
    ON CONFLICT (id) DO UPDATE SET
        status = EXCLUDED.status,
        phase = EXCLUDED.phase,
        files = EXCLUDED.files,
        ts = EXCLUDED.ts,
        ttl = EXCLUDED.ttl,
        payload = EXCLUDED.payload;
    RETURN broadcast_id;
END;
$$ LANGUAGE plpgsql;
