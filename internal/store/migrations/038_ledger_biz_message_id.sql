-- =============================================================================
-- 038_ledger_biz_message_id.sql（v2.3.27 / task-12）
-- 目的：让 /v1/ledger/* 能落到权威账本 paid_message_ledger，同时**保住对外 messageId**。
-- =============================================================================
--
-- 背景（字段差异详见 docs/notes-ledger-consolidation.md）：
--   老实现 payment_ledger.go 把 HTTP 的任意字符串 messageId 直接写进退役表
--   payment_ledger_legacy_v09(message_id TEXT)。而权威账本 paid_message_ledger
--   （001_init.sql:103）的 message_id 是 **UUID NOT NULL UNIQUE**：
--     * 直接把字符串塞进去 → 类型错误；
--     * 只存派生 UUID → 列表/对账接口返回的是哈希，对外契约被破坏。
--   因此新增一列保存**原始业务 messageId**：message_id 仍是 UUID（表结构/幂等键不变），
--   biz_message_id 用于展示与按原串查询。这是纯增量列，不改任何既有语义。
--
-- 写入规则（payment_ledger.go）：
--   message_id     = md5('im2a:ledger:biz:'||$messageId)::uuid   -- 确定性、可重入
--   biz_message_id = $messageId                                  -- 原串，供展示/对账
-- 读取规则：WHERE biz_message_id=$1（列表/退款/查单），历史行（biz_message_id IS NULL）
--   回退到 message_id::text 显示（COALESCE），不影响既有数据。
-- =============================================================================

ALTER TABLE paid_message_ledger
    ADD COLUMN IF NOT EXISTS biz_message_id TEXT;

-- 幂等键：同一业务 messageId 只能有一行（历史行是 NULL，不参与唯一性）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_biz_message
    ON paid_message_ledger (biz_message_id)
    WHERE biz_message_id IS NOT NULL;

COMMENT ON COLUMN paid_message_ledger.biz_message_id IS
    'v2.3.27：/v1/ledger/* 的原始业务 messageId（message_id 列存派生 UUID，二者一一对应）';
