-- v1.0.0: PG 持久化（记忆/蒸馏/消息/事件）

-- 工作记忆（KV，带 TTL）
CREATE TABLE IF NOT EXISTS working_memory (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    agent_id    TEXT NOT NULL,
    key         TEXT NOT NULL,
    value_json  JSONB NOT NULL,
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, agent_id, key)
);
CREATE INDEX IF NOT EXISTS idx_working_memory_scope ON working_memory(user_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_working_memory_expires ON working_memory(expires_at) WHERE expires_at IS NOT NULL;

-- 情景记忆（追加写时间线）
CREATE TABLE IF NOT EXISTS episodes (
    id          TEXT PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    agent_id    TEXT NOT NULL,
    timestamp   BIGINT NOT NULL,
    kind        TEXT NOT NULL,
    summary     TEXT NOT NULL,
    result      TEXT NOT NULL DEFAULT 'pending',
    tags        TEXT[] NOT NULL DEFAULT '{}',
    meta        JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_episodes_scope ON episodes(user_id, agent_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_episodes_kind ON episodes(user_id, agent_id, kind);
CREATE INDEX IF NOT EXISTS idx_episodes_tags ON episodes USING GIN(tags);

-- 语义记忆（事实/偏好，带置信度）
CREATE TABLE IF NOT EXISTS facts (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    agent_id    TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,
    confidence  REAL NOT NULL DEFAULT 0.5,
    source      TEXT NOT NULL DEFAULT 'manual',
    updated_at  BIGINT NOT NULL,
    meta        JSONB NOT NULL DEFAULT '{}',
    UNIQUE(user_id, agent_id, key)
);
CREATE INDEX IF NOT EXISTS idx_facts_scope ON facts(user_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_facts_confidence ON facts(user_id, agent_id, confidence DESC);

-- 蒸馏改进记录
CREATE TABLE IF NOT EXISTS improvements (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    agent_id    TEXT NOT NULL,
    scenario    TEXT NOT NULL,
    decision    TEXT NOT NULL,
    feedback    TEXT NOT NULL,
    evidence    JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_improvements_scope ON improvements(user_id, agent_id, created_at DESC);

-- 阈值补丁
CREATE TABLE IF NOT EXISTS threshold_patches (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL,
    agent_id        TEXT NOT NULL,
    capability      TEXT NOT NULL,
    old_threshold   REAL NOT NULL,
    new_threshold   REAL NOT NULL,
    confidence      REAL NOT NULL,
    reason          TEXT NOT NULL,
    auto_applied    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_threshold_patches_scope ON threshold_patches(user_id, agent_id);

-- 消息持久化
CREATE TABLE IF NOT EXISTS messages (
    id              BIGSERIAL PRIMARY KEY,
    message_id      TEXT NOT NULL UNIQUE,
    sender_id       BIGINT NOT NULL,
    recipient_id    BIGINT NOT NULL,
    biz_type        TEXT NOT NULL DEFAULT 'text',
    content         TEXT NOT NULL,
    agent_context   JSONB,
    seq             BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_recipient ON messages(recipient_id, created_at DESC);
