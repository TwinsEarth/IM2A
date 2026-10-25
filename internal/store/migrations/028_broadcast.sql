-- v2.2.2 全量广播：广播任务 + 逐条投递
CREATE TABLE IF NOT EXISTS wechat_broadcasts (
    id             TEXT PRIMARY KEY,
    text           TEXT NOT NULL,
    sender_account TEXT NOT NULL DEFAULT '',
    sender_wxid    TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending',   -- pending/running/completed/partial/failed
    total          INTEGER NOT NULL DEFAULT 0,
    sent           INTEGER NOT NULL DEFAULT 0,
    failed         INTEGER NOT NULL DEFAULT 0,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS wechat_broadcast_deliveries (
    id           BIGSERIAL PRIMARY KEY,
    broadcast_id TEXT NOT NULL REFERENCES wechat_broadcasts(id) ON DELETE CASCADE,
    to_wxid      TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',     -- pending/sent/failed
    message_id   TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (broadcast_id, to_wxid)
);
CREATE INDEX IF NOT EXISTS idx_bc_del_broadcast ON wechat_broadcast_deliveries(broadcast_id);
