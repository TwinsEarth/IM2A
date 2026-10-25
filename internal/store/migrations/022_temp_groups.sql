CREATE TABLE IF NOT EXISTS temp_groups (
  id          TEXT PRIMARY KEY,
  topic       TEXT NOT NULL,
  created_by   TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'active',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at   TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS temp_group_members (
  group_id   TEXT NOT NULL REFERENCES temp_groups(id) ON DELETE CASCADE,
  agent_id   TEXT NOT NULL,
  joined_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, agent_id)
);
CREATE TABLE IF NOT EXISTS temp_messages (
  id          BIGSERIAL PRIMARY KEY,
  group_id    TEXT NOT NULL,
  from_agent  TEXT NOT NULL,
  payload     JSONB NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_temp_messages_group ON temp_messages(group_id, created_at);
