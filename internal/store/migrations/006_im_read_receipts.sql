-- v1.7.2 已读回执（C2C 已读回调真实落库）
CREATE TABLE IF NOT EXISTS im_read_receipts (
    owner_id TEXT NOT NULL,
    peer_id  TEXT NOT NULL,
    msg_key  TEXT NOT NULL,
    read_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    source   TEXT NOT NULL DEFAULT 'webhook',
    PRIMARY KEY (owner_id, peer_id, msg_key)
);
