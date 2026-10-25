-- SPDX-License-Identifier: MIT
--
-- C3（task-25）：原生消息 + outbox + 读位点。
--
-- 硬约束（与 task-25 逐条对应）：
--   * **投递位点不另造**：per-device 位点仍是 im_device_cursor，补发日志仍是
--     im_delivery_log（B2-b，2000 帧/2MiB/7 天 + truncated 语义），本迁移不建任何投递游标；
--   * 本迁移只新增**读位点** n_read_cursor（per principal+conversation，GREATEST 单调）；
--   * bizSeq 是**会话内连续的业务序号**（n_conversations.next_biz_seq 事务内 +1）；
--     它与 im_delivery_log.seq（每 recipient 的投递序）是**两个不同的序**，不可混用；
--   * outbox 模式：事务内只写 n_messages + n_outbox；提交后才由 worker 投递；
--   * 幂等：UNIQUE(conversation_id, client_msg_id)；
--   * 正文 ≤32KiB 内联 body BYTEA（C3 不做 blob 存储；body_ref 预留给 C4 内容寻址），
--     body_size 用于上限校验/审计，不静默截断。

CREATE TABLE IF NOT EXISTS n_messages (
    id              TEXT PRIMARY KEY,              -- "m_" + uuid
    conversation_id TEXT NOT NULL REFERENCES n_conversations(id) ON DELETE CASCADE,
    sender_principal BIGINT NOT NULL REFERENCES n_principal(id),
    biz_seq         BIGINT NOT NULL,               -- 会话内连续业务序号（≠ deliverySeq）
    client_msg_id   TEXT,                          -- 幂等键（null=不幂等）
    kind            TEXT NOT NULL DEFAULT 'text',
    body            BYTEA NOT NULL DEFAULT ''::bytea,
    body_ref        TEXT,                          -- 预留：内容寻址引用（C3 为 NULL）
    body_size       INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    edited_at       TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    UNIQUE (conversation_id, biz_seq)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_n_messages_client
    ON n_messages (conversation_id, client_msg_id) WHERE client_msg_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_n_messages_conv_seq
    ON n_messages (conversation_id, biz_seq);
CREATE INDEX IF NOT EXISTS idx_n_messages_sender
    ON n_messages (sender_principal);

-- 读位点（只此一套；与投递位点 im_device_cursor 语义独立）。
CREATE TABLE IF NOT EXISTS n_read_cursor (
    principal_id    BIGINT NOT NULL REFERENCES n_principal(id),
    conversation_id TEXT NOT NULL REFERENCES n_conversations(id) ON DELETE CASCADE,
    read_biz_seq    BIGINT NOT NULL DEFAULT 0,
    read_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, conversation_id)
);

-- outbox：事务内与消息同一 tx 写入；提交后 worker 批量投递（失败指数退避/死信）。
CREATE TABLE IF NOT EXISTS n_outbox (
    id              BIGSERIAL PRIMARY KEY,
    message_id      TEXT NOT NULL UNIQUE REFERENCES n_messages(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','delivered','failed')),
    attempts        INT NOT NULL DEFAULT 0,
    next_try_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_n_outbox_due
    ON n_outbox (status, next_try_at) WHERE status = 'pending';
