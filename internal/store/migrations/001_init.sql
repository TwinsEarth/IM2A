-- =============================================================================
-- AI 人机混合熟人社交 IM 平台 —— 初始 schema（v0.2 / Phase 1）
-- 对应技术设计基线 §4.2 核心数据表。
--
-- 设计原则：
--   1. 关系链、五档信任档位、付费账本是产品核心治理资产，必须自有（不托管 IM PaaS）；
--   2. 付费账本与审计事件为不可变（仅 INSERT/追加，状态机迁移不改写历史）；
--   3. 付费扣费以 message_id 为唯一键做幂等（任何重试不二次扣费，§10-R2）；
--   4. 所有涉及金额单位为分（cents），避免浮点误差。
--
-- 应用：psql "$DSN" -f deploy/migrations/001_init.sql
-- 幂等：可重复执行（IF NOT EXISTS / OR REPLACE）。
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 0. 扩展：pgvector（Agent 记忆向量；Phase 0-2 用 pgvector，§2.5）
--    未安装 pgvector 时此行可注释，不影响关系链/账本/审批等核心表。
-- -----------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS pgcrypto;     -- gen_random_uuid()，UUID 主键
-- CREATE EXTENSION IF NOT EXISTS vector;    -- pgvector 启用后取消注释（记忆库另见 002 迁移）

-- =============================================================================
-- 1. 用户主账号与外部身份映射（§4.1 auth.rpc）
-- =============================================================================
CREATE TABLE IF NOT EXISTS users (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- 主账号（手机号/身份证实名后建立）；外部身份（微信/手机号）映射到 account_identities
    phone_encrypted TEXT,                       -- 手机号加密存储（不存明文）
    status          SMALLINT NOT NULL DEFAULT 1, -- 1 正常 / 0 封禁
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS account_identities (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider    VARCHAR(32) NOT NULL,           -- wechat / phone / apple / oidc
    open_id     VARCHAR(128) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, open_id)
);
CREATE INDEX IF NOT EXISTS idx_account_identities_user ON account_identities(user_id);

-- =============================================================================
-- 2. 五档信任矩阵（§1.4 / §4.2）——关系链核心治理资产
--    level: 0 本人 / 1 家庭 / 2 信任 / 3 已知 / 4 陌生
-- =============================================================================
CREATE TABLE IF NOT EXISTS trust_levels (
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    level        SMALLINT NOT NULL CHECK (level IN (0,1,2,3,4)),
    -- 细分权限（可空，缺省按 §1.4 矩阵推导）；命名对应产品"查看/拜访/留言/范围/紧急联系"
    perm_view    BOOLEAN NOT NULL DEFAULT TRUE,
    perm_visit   BOOLEAN NOT NULL DEFAULT FALSE,
    perm_message BOOLEAN NOT NULL DEFAULT TRUE,
    perm_scope   VARCHAR(32) NOT NULL DEFAULT 'normal', -- normal / limited / restricted
    perm_emergency BOOLEAN NOT NULL DEFAULT FALSE,
    granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,                        -- 空=永久；到期由定时任务降档
    granted_by   BIGINT,
    PRIMARY KEY (user_id, target_id),
    CHECK (user_id <> target_id)
);
CREATE INDEX IF NOT EXISTS idx_trust_target ON trust_levels(target_id);
CREATE INDEX IF NOT EXISTS idx_trust_level ON trust_levels(level);

-- 信任档位变更审计（关系链权限每次变更都要可追溯）
CREATE TABLE IF NOT EXISTS trust_level_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    target_id   BIGINT NOT NULL,
    old_level   SMALLINT,
    new_level   SMALLINT NOT NULL,
    changed_by  BIGINT,
    reason      VARCHAR(256),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- =============================================================================
-- 3. Agent 能力声明（§3.3 / §6）——权限门控的数据来源
--    每次工具调用在 PreToolUse 钩子用本表做 capability 检查（代码层强制）
-- =============================================================================
CREATE TABLE IF NOT EXISTS agent_capabilities (
    agent_id                    VARCHAR(64) PRIMARY KEY,
    owner_id                    BIGINT REFERENCES users(id) ON DELETE CASCADE, -- 可空：能力可在用户引导前注册
    kind                        VARCHAR(16) NOT NULL DEFAULT 'sub',  -- master / sub
    trust_level_min             SMALLINT NOT NULL DEFAULT 4,
    allowed_skills              TEXT[] NOT NULL DEFAULT '{}',
    budget_cents_per_day        BIGINT NOT NULL DEFAULT 0,           -- 0 = 不启用
    requires_approval_above_cents BIGINT NOT NULL DEFAULT 0,         -- 0 = 不启用
    financial_impact            VARCHAR(16) NOT NULL DEFAULT 'none', -- none/read_only/spend_limit
    approval_policy             VARCHAR(16) NOT NULL DEFAULT 'auto', -- auto/approve/dual
    enabled                     BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_cap_owner ON agent_capabilities(owner_id);

-- =============================================================================
-- 4. 付费消息账本（§1.3 / §4.2 / §10-R2）——不可变，仅 INSERT/状态推进
--    billing_mode: 1 发送即扣费 / 2 回复才扣费
--    status:       0 PENDING / 1 CHARGED / 2 REFUNDED
-- =============================================================================
CREATE TABLE IF NOT EXISTS paid_message_ledger (
    id               BIGSERIAL PRIMARY KEY,
    message_id       UUID NOT NULL,
    sender_id        BIGINT NOT NULL,
    receiver_id      BIGINT NOT NULL,
    billing_mode     SMALLINT NOT NULL CHECK (billing_mode IN (1,2)),
    amount_cents     BIGINT NOT NULL CHECK (amount_cents >= 0),
    status           SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0,1,2)),
    charged_at       TIMESTAMPTZ,
    refunded_at      TIMESTAMPTZ,
    refund_reason    VARCHAR(256),
    approval_card_id VARCHAR(64),               -- 关联审批单（预审通过后才有）
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id)                         -- 幂等键：重复回调不二次扣费
);
-- 单日发送上限（按发送方+自然日）由应用层以 (sender_id, created_at::date) 计数，
-- 这里建索引支撑日限查询。
CREATE INDEX IF NOT EXISTS idx_ledger_sender_day ON paid_message_ledger(sender_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ledger_status ON paid_message_ledger(status);

-- 账本状态迁移历史（不可变；charge/refund 各写一条，保证可追溯）
CREATE TABLE IF NOT EXISTS paid_message_events (
    id          BIGSERIAL PRIMARY KEY,
    message_id  UUID NOT NULL,
    from_status SMALLINT NOT NULL,
    to_status   SMALLINT NOT NULL,
    note        VARCHAR(256),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ledger_events_msg ON paid_message_events(message_id);

-- =============================================================================
-- 5. 审批单（§7.3 / §9.2）——长生命周期、可寻址、事件溯源
-- =============================================================================
CREATE TABLE IF NOT EXISTS approval_tickets (
    id              VARCHAR(64) PRIMARY KEY,    -- 引擎生成 t%06d（生产可替换为 UUID）
    user_id         BIGINT NOT NULL,
    source_agent_id VARCHAR(64) NOT NULL,
    ticket_type     VARCHAR(32) NOT NULL,  -- NEGOTIATION/PAYMENT/SKILL_CALL/EXTERNAL_ACTION
    state           VARCHAR(32) NOT NULL,  -- PENDING/REVIEWING/APPROVED/REJECTED/MODIFIED/EXECUTING/COMPLETED/FAILED
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    escalation      SMALLINT NOT NULL DEFAULT 0,  -- 0 无 / 1 手表 / 2 短信
    deadline        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_approval_user_state ON approval_tickets(user_id, state);
CREATE INDEX IF NOT EXISTS idx_approval_deadline ON approval_tickets(deadline)
    WHERE state IN ('PENDING','REVIEWING');   -- 超时扫描只查未决单

-- 审批事件流（不可变；用户通过/驳回/修改 + 超时升级都追加一条）
CREATE TABLE IF NOT EXISTS approval_events (
    id          BIGSERIAL PRIMARY KEY,
    ticket_id   VARCHAR(64) NOT NULL REFERENCES approval_tickets(id) ON DELETE CASCADE,
    event_type  VARCHAR(32) NOT NULL,       -- CREATED/APPROVED/REJECTED/MODIFIED/EXECUTED/FAILED/ESCALATED
    by_who      VARCHAR(64) NOT NULL,       -- user / agent / system
    note        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_approval_events_ticket ON approval_events(ticket_id);

-- =============================================================================
-- 6. Agent 协商会话（§2.4）——8 态 Task 状态机 + 轮次/token 熔断
-- =============================================================================
CREATE TABLE IF NOT EXISTS negotiation_sessions (
    id                 VARCHAR(64) PRIMARY KEY,
    state              VARCHAR(32) NOT NULL,
    from_agent_id      VARCHAR(64) NOT NULL,
    to_agent_id       VARCHAR(64) NOT NULL,
    max_rounds         INT NOT NULL DEFAULT 5,
    round              INT NOT NULL DEFAULT 0,
    token_used         BIGINT NOT NULL DEFAULT 0,
    token_budget       BIGINT NOT NULL DEFAULT 0,
    summary            JSONB NOT NULL DEFAULT '{}'::jsonb,
    approval_ticket_id VARCHAR(64),
    deadline           TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_negotiation_state ON negotiation_sessions(state);

CREATE TABLE IF NOT EXISTS negotiation_turns (
    id            BIGSERIAL PRIMARY KEY,
    session_id    VARCHAR(64) NOT NULL REFERENCES negotiation_sessions(id) ON DELETE CASCADE,
    seq           INT NOT NULL,
    from_agent_id VARCHAR(64) NOT NULL,
    intent        VARCHAR(32) NOT NULL,     -- PROPOSE/COUNTER/ACCEPT/REJECT/REQUEST_INFO/REQUEST_AUTH
    payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
    artifacts     TEXT[] NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);

-- =============================================================================
-- 7. 审计事件（§8.3 / §10）——不可变追加，只 INSERT，不 UPDATE/DELETE
--    每次工具调用、门控放行/拦截、扣费/退款、审批决策、内容审核写一条。
-- =============================================================================
CREATE TABLE IF NOT EXISTS audit_events (
    id          BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    trace_id    VARCHAR(64),               -- 关联全链路 trace
    message_id  VARCHAR(64),
    agent_id    VARCHAR(64),
    user_id     BIGINT,
    action      VARCHAR(32) NOT NULL,      -- TOOL_CALL/GATE_*/CHARGE/REFUND/APPROVE/REJECT/CONTENT_*
    decision    VARCHAR(32),
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_events(occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_agent ON audit_events(agent_id);
CREATE INDEX IF NOT EXISTS idx_audit_trace ON audit_events(trace_id);
CREATE INDEX IF NOT EXISTS idx_audit_message ON audit_events(message_id);

-- 防篡改：禁止 UPDATE/DELETE（应用账号只授 INSERT/SELECT）。
-- 注意：DO 块用 IF EXISTS 避免重复 DROP 报错。
CREATE OR REPLACE FUNCTION audit_no_modify() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only; % is not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_no_update ON audit_events;
CREATE TRIGGER trg_audit_no_update BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_no_modify();
DROP TRIGGER IF EXISTS trg_audit_no_delete ON audit_events;
CREATE TRIGGER trg_audit_no_delete BEFORE DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_no_modify();

-- =============================================================================
-- 8. updated_at 自动维护
-- =============================================================================
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_updated ON users;
CREATE TRIGGER trg_users_updated BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
DROP TRIGGER IF EXISTS trg_agent_cap_updated ON agent_capabilities;
CREATE TRIGGER trg_agent_cap_updated BEFORE UPDATE ON agent_capabilities
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
DROP TRIGGER IF EXISTS trg_negotiation_updated ON negotiation_sessions;
CREATE TRIGGER trg_negotiation_updated BEFORE UPDATE ON negotiation_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- =============================================================================
-- 迁移结束。
-- 最小权限建议（生产）：应用账号对账本/审计仅 INSERT/SELECT：
--   GRANT SELECT, INSERT ON paid_message_ledger, paid_message_events,
--       audit_events, approval_events, negotiation_turns TO app_user;
--   GRANT SELECT, INSERT, UPDATE ON trust_levels, agent_capabilities,
--       approval_tickets, negotiation_sessions TO app_user;
-- =============================================================================
