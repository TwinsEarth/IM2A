-- SPDX-License-Identifier: MIT
--
-- C1（task-21）：原生通道身份 + presence 快照。
--
-- 幂等纪律：只做 CREATE TABLE/INDEX IF NOT EXISTS，不做 ALTER、不建可能因历史数据失败的
-- UNIQUE INDEX（037 的教训）；迁移每次启动都会重跑。
--
-- 说明：
--   * n_principal 是 Wave C 的统一 actor/投递键（agents/纯微信身份/人类）；
--     C1 的身份权威值仍是 JWT.sub（task-21 验收口径），principal 映射在 C2/C3 收敛。
--   * n_presence 只是"快照/观测"（每 30s upsert 活跃 user），**不是 presence 真相源**：
--     真相在内核内存状态机（TTL 45s / 15s 刷新），重启后 45s 内自愈。

CREATE TABLE IF NOT EXISTS n_principal (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('human','agent')),
    user_id      BIGINT,
    wxid         TEXT,
    agent_id     TEXT,
    display_name TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'active',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_n_principal_user
    ON n_principal (kind, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_n_principal_wxid
    ON n_principal (kind, wxid) WHERE wxid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_n_principal_agent
    ON n_principal (kind, agent_id) WHERE agent_id IS NOT NULL;

-- presence 快照（活跃 principal ≤5k 行；只用于重启后观测/最后已知状态）。
CREATE TABLE IF NOT EXISTS n_presence (
    user_id       BIGINT PRIMARY KEY,
    state         TEXT NOT NULL DEFAULT 'offline' CHECK (state IN ('online','away','offline')),
    device_count  INT NOT NULL DEFAULT 0,
    last_seen_at  TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_n_presence_updated
    ON n_presence (updated_at DESC);
