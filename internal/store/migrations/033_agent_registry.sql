-- v2.3.3 A2：每用户 PG Agent 注册表。
-- 总 Agent（master）+ 子 Agent 按用户隔离注册；含信任档位与 VIP 配额。
CREATE TABLE IF NOT EXISTS agent_registry (
    id              BIGSERIAL PRIMARY KEY,
    user_wxid       TEXT NOT NULL,
    agent_id        TEXT NOT NULL,           -- master|calendar|purchase|legal|property|parent|chat
    role            TEXT NOT NULL DEFAULT 'sub', -- master|sub
    display_name    TEXT NOT NULL DEFAULT '',
    trust_level     INT  NOT NULL DEFAULT 1,   -- 0 陌生人 .. 5 熟人
    quota_max       INT  NOT NULL DEFAULT 50,  -- 每周期最大调用次数（VIP 更高）
    quota_used      INT  NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active', -- active|paused
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_wxid, agent_id)
);
CREATE INDEX IF NOT EXISTS agent_registry_user_idx ON agent_registry (user_wxid);
CREATE INDEX IF NOT EXISTS agent_registry_status_idx ON agent_registry (status);
