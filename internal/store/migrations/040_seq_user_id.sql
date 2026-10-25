-- =============================================================================
-- 040_seq_user_id.sql（v2.3.28 / 真库首验缺陷 3）
-- 补建微信身份号序列 seq_user_id —— 全新库上它从未被任何迁移创建过。
-- =============================================================================
--
-- 证据（读写点反推）：
--   internal/wechatid/wechatid.go:27-28
--     INSERT INTO wechat_identity(wxid,user_id) VALUES($1::text, nextval('seq_user_id'))
--   internal/api/wechat_webhook.go:216-217
--     INSERT INTO wechat_identity(wxid,user_id) VALUES($1, nextval('seq_user_id'))
-- 列类型：wechat_identity.user_id INTEGER NOT NULL（019_v201.sql:2-7，无外键）。
--   → 序列按 INTEGER 生成，与列同域；耗尽时显式报错，而不是在 CAST 处悄悄越界。
-- 语义：wxid → 用户号的**全局单调生成器**；与 users.id（GENERATED ALWAYS AS IDENTITY）
--   相互独立 —— 这是既有设计（微信身份号先于主账号建立，wechat_identity.user_id 无 FK）。
--
-- 幂等（服务每次启动都会全量重跑迁移，见 internal/store/migrate.go:59-69）：
--   * CREATE SEQUENCE IF NOT EXISTS：已有则不动；
--   * 只在序列**从未被调用**（pg_sequences/序列的 is_called=false）时播种，
--     避免重跑迁移把已用号码回退（回退会在删除/清理过的库上造成号码重用）。
--   * 升级已有库时播种到 max(wechat_identity.user_id)，保证下一个号大于历史已用值。
-- =============================================================================

CREATE SEQUENCE IF NOT EXISTS seq_user_id AS INTEGER;

DO $$
DECLARE
    max_used BIGINT := 0;
    called   BOOLEAN;
BEGIN
    SELECT is_called INTO called FROM seq_user_id;
    IF called IS NULL OR called = FALSE THEN
        SELECT COALESCE(max(user_id), 0) INTO max_used FROM wechat_identity;
        IF max_used > 0 THEN
            PERFORM setval('seq_user_id', max_used, true);
        ELSE
            PERFORM setval('seq_user_id', 1, false);
        END IF;
    END IF;
END $$;

COMMENT ON SEQUENCE seq_user_id IS
    'v2.3.28：wechat_identity.user_id 的生成器（internal/wechatid/wechatid.go、internal/api/wechat_webhook.go）';
