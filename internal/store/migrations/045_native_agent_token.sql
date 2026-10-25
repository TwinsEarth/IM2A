-- SPDX-License-Identifier: MIT
--
-- C4（task-26）agent token 最小闭环：签发记录 + 吊销。
--   * token 本体是 C1 的 HMAC-SHA256 JWT（sub = n_principal(kind='agent').id，
--     iss = "im2a-agent" 等价 typ=agent）；
--   * 本表只存 jti/owner/过期/吊销，用于"可吊销"校验；不做 RBAC（agent.permissions 插件负责）；
--   * 幂等：CREATE TABLE/INDEX IF NOT EXISTS。

CREATE TABLE IF NOT EXISTS n_agent_token (
    id             BIGSERIAL PRIMARY KEY,
    jti            TEXT NOT NULL UNIQUE,
    agent_id       TEXT NOT NULL,
    owner_user_id  BIGINT NOT NULL,          -- 签发者 users.id（吊销作用域）
    principal_id   BIGINT NOT NULL,          -- n_principal(kind='agent').id（token.sub）
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_n_agent_token_owner ON n_agent_token (owner_user_id);
CREATE INDEX IF NOT EXISTS idx_n_agent_token_agent ON n_agent_token (agent_id);
