-- v1.8.6 数据主体权利（DSR）：个人数据导出/删除请求，可追溯、可响应（个保法 §44-47、等保 2.0）。
CREATE TABLE IF NOT EXISTS data_subject_requests (
    id           BIGSERIAL PRIMARY KEY,
    request_id   TEXT UNIQUE NOT NULL,
    user_id      BIGINT NOT NULL,
    kind         TEXT NOT NULL,                 -- export / delete
    status       TEXT NOT NULL DEFAULT 'pending', -- pending / completed / rejected
    result       JSONB,                          -- 导出数据（export）或删除报告（delete）
    reason       TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_dsr_user   ON data_subject_requests(user_id);
CREATE INDEX IF NOT EXISTS idx_dsr_status ON data_subject_requests(status);
CREATE INDEX IF NOT EXISTS idx_dsr_kind   ON data_subject_requests(kind);
