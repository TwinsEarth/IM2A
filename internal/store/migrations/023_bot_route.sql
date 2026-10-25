CREATE TABLE IF NOT EXISTS bot_route_messages (
  id          BIGSERIAL PRIMARY KEY,
  from_wxid   TEXT NOT NULL,
  to_wxid     TEXT NOT NULL,
  msg_type    TEXT NOT NULL,
  payload     JSONB NOT NULL,
  delivered   BOOLEAN NOT NULL DEFAULT false,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_bot_route_to ON bot_route_messages(to_wxid, delivered);
