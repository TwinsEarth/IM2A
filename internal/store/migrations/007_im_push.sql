-- v1.7.3 离线推送：任务队列 + 推送凭据（真实落库）
CREATE TABLE IF NOT EXISTS im_push_credentials (
    user_id    TEXT NOT NULL,
    platform   TEXT NOT NULL,            -- apns / hmp / huawei / xiaomi ...
    cert_ref   TEXT NOT NULL DEFAULT '', -- 腾讯云证书/推送标识引用（证书接入时填）
    device_token TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, platform)
);
CREATE TABLE IF NOT EXISTS im_push_tasks (
    id           BIGSERIAL PRIMARY KEY,
    user_id      TEXT NOT NULL,
    msg_key      TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending', -- pending / sent / failed
    attempts     INT NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_im_push_tasks_status ON im_push_tasks (status, id);
