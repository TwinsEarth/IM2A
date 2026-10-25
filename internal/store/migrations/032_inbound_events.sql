-- v2.3.1 A1：webhook 异步入队（PG OUTBOX，崩溃不丢消息）。
CREATE TABLE IF NOT EXISTS inbound_events (
    id              BIGSERIAL PRIMARY KEY,
    idem_key        TEXT NOT NULL UNIQUE,
    account         TEXT NOT NULL,
    from_wxid       TEXT NOT NULL,
    text            TEXT NOT NULL,
    nego_session_id TEXT NOT NULL DEFAULT '',
    nego_intent     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'queued', -- queued|processing|done|error
    error           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS inbound_events_status_idx ON inbound_events (status, created_at);
