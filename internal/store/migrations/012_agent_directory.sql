-- v1.8.1：Agent 通讯录一等公民（IMClaw trust/tags + Attention levels；Bloome Agent-as-contact）。
-- 关键：trust_level 为继承快照（来自 Agent 背后的人），Agent 不独立积累信任分。
CREATE TABLE IF NOT EXISTS agent_directory (
    agent_id            TEXT PRIMARY KEY,
    owner_user          TEXT NOT NULL,
    display_name        TEXT NOT NULL,
    alias               TEXT NOT NULL DEFAULT '',
    attention_level     SMALLINT NOT NULL DEFAULT 1,
    tags                JSONB NOT NULL DEFAULT '[]'::jsonb,
    monthly_budget_cents BIGINT NOT NULL DEFAULT 0,
    trust_level         SMALLINT NOT NULL DEFAULT 4,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_directory_owner ON agent_directory(owner_user, attention_level DESC);
