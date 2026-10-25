-- v2.0.5 微信 Bot 上行幂等表
CREATE TABLE IF NOT EXISTS wechat_idem (
    idem        TEXT PRIMARY KEY,
    wxid        TEXT NOT NULL,
    response    JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wechat_idem_wxid ON wechat_idem(wxid);
