-- v2.1.2 OpenClaw 扫码绑定会话
CREATE TABLE IF NOT EXISTS openclaw_qr_sessions (
  qr_id        TEXT PRIMARY KEY,
  owner_user   TEXT NOT NULL,
  device_type  TEXT NOT NULL DEFAULT 'unknown',
  status       TEXT NOT NULL DEFAULT 'pending',
  wxid         TEXT,
  user_id      BIGINT,
  group_id     TEXT,
  error        TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  scanned_at   TIMESTAMPTZ,
  CONSTRAINT chk_qr_status CHECK (status IN ('pending','scanned','bound','expired','failed'))
);
CREATE INDEX IF NOT EXISTS idx_qr_wxid ON openclaw_qr_sessions(wxid) WHERE wxid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_qr_owner ON openclaw_qr_sessions(owner_user, created_at DESC);
