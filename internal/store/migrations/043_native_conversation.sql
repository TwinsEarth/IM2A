-- SPDX-License-Identifier: MIT
--
-- C2（task-23）：原生会话模型（n_conversations / n_conversation_members）。
--
-- 幂等纪律：只做 CREATE TABLE/INDEX IF NOT EXISTS，不做 ALTER、不建可能因历史数据失败的
-- UNIQUE INDEX（037 的教训）；迁移每次启动都会重跑。
--
-- 边界（D8=B）：本表只服务**原生通道**（Agent 消息/事件/原生客户端）；
-- 人类 IM 会话仍由腾讯云 IM PaaS 负责，这里只允许用 paas_conv_id **存引用**，不双写正文。
--
-- 语义：
--   * kind 决定成员上限：DIRECT=2 / GROUP=12 / TASK=8 / AGENT_NEGOTIATION=4；
--   * status 是单向状态机：active → archived（归档后写入接口拒绝）；
--   * client_conv_id 幂等键：**UNIQUE(owner_principal, client_conv_id)**（每个 owner 内唯一，
--     不同用户可各自使用同一 client id）；
--   * channel 只是通道归属标记（native / tencent-im 投影），C2 不可修改。

CREATE TABLE IF NOT EXISTS n_conversations (
    id               TEXT PRIMARY KEY,            -- "c_" + uuid（对外主键）
    kind             TEXT NOT NULL CHECK (kind IN ('DIRECT','GROUP','TASK','AGENT_NEGOTIATION')),
    channel          TEXT NOT NULL DEFAULT 'native' CHECK (channel IN ('native','tencent-im')),
    owner_principal  BIGINT NOT NULL REFERENCES n_principal(id),
    title            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    next_biz_seq     BIGINT NOT NULL DEFAULT 0,   -- C3 消息序号分配器（C2 不用）
    max_members      INT NOT NULL DEFAULT 2,
    client_conv_id   TEXT,                        -- 客户端幂等键（per owner 唯一）
    paas_conv_id     TEXT,                        -- 腾讯云 IM 会话 id（只存引用，不双写正文）
    metadata         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at      TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_n_conv_client_id
    ON n_conversations (owner_principal, client_conv_id) WHERE client_conv_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_n_conv_owner ON n_conversations (owner_principal);
CREATE INDEX IF NOT EXISTS idx_n_conv_kind_updated ON n_conversations (kind, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_n_conv_status ON n_conversations (status);

CREATE TABLE IF NOT EXISTS n_conversation_members (
    conversation_id TEXT NOT NULL REFERENCES n_conversations(id) ON DELETE CASCADE,
    principal_id    BIGINT NOT NULL REFERENCES n_principal(id),
    role            TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member','observer')),
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at         TIMESTAMPTZ,                  -- 退出=软删（保留历史可读）
    muted           BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (conversation_id, principal_id)
);

CREATE INDEX IF NOT EXISTS idx_n_members_principal ON n_conversation_members (principal_id);
CREATE INDEX IF NOT EXISTS idx_n_members_active ON n_conversation_members (conversation_id)
    WHERE left_at IS NULL;
