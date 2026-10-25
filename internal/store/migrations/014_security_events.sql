-- v1.8.3 安全合规：请求级安全审计事件流（与业务审计 audit_events 分离，专用于安全监控/等保）
CREATE TABLE IF NOT EXISTS security_events (
  id BIGSERIAL PRIMARY KEY,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  event_type TEXT NOT NULL,                 -- request / auth_failed / rate_limited / cors_blocked / blocked
  method TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  status_code INT NOT NULL DEFAULT 0,
  client_ip TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sec_time ON security_events(occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_sec_type ON security_events(event_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_sec_ip ON security_events(client_ip, occurred_at DESC);
