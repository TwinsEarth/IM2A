-- =============================================================================
-- 2026-10-25 P0 修复（task-11）：补 im_webhook_events 基础表
-- =============================================================================
-- 背景（实测）：
--   004_im.sql 第 4-5 行对 im_webhook_events 执行 ALTER TABLE ADD COLUMN，
--   第 6-8 行在其上建部分唯一索引；但全仓迁移里**没有任何一处 CREATE TABLE
--   im_webhook_events**（`grep -i webhook internal/store/migrations/*.sql` 只命中
--   004/005/006/032，均非建表）。
--   后果：全新 PG 库执行到 004 时直接报 `42P01 relation "im_webhook_events" does
--   not exist` → store.Migrate 返回 error（migrate.go:65-67 不吞错）→ NewDeps 失败
--   → 配置了 PG_DSN 的网关根本无法从零启动。历史库因为当年手工建过表而"看起来正常"。
--
-- 为什么文件名是 003a 而不是顺延到 035：
--   migrate.go:41 `sort.Strings(names)` 按文件名字典序执行，本表必须在 004 之前存在，
--   才能让 004 的 ALTER/INDEX 成立。003a 排序在 "003_embedding.sql" 之后、
--   "004_im.sql" 之前。对已存在该表的库，CREATE TABLE IF NOT EXISTS 是空操作。
--
-- 字段来源（逐个对应真实读写点，不做想象扩展）：
--   internal/api/im.go:146-151   INSERT (callback_type, from_user, to_user, group_id,
--                                        msg_key, sdk_app_id, payload)
--   internal/api/admin.go:51     SELECT count(*) FROM im_webhook_events
--   internal/api/admin.go:112    WHERE received_at >= now() - make_interval(days=>$1)
--   internal/api/cleanup_helpers_test.go:30  DELETE ... WHERE from_user/to_user/group_id/msg_key
--   internal/api/v12_real_test.go:137        WHERE msg_key=$1
--   004_im.sql:4                 msg_key VARCHAR(128)（类型必须与此处一致，否则 ALTER 后类型不一致）
--   004_im.sql:5                 sdk_app_id BIGINT
-- =============================================================================

CREATE TABLE IF NOT EXISTS im_webhook_events (
    id             BIGSERIAL PRIMARY KEY,
    callback_type  TEXT NOT NULL DEFAULT '',
    from_user      TEXT NOT NULL DEFAULT '',
    to_user        TEXT NOT NULL DEFAULT '',
    group_id       TEXT NOT NULL DEFAULT '',
    -- msg_key 允许 NULL：C2C 回调可能没有 MsgKey（im.go:151 用 nullIfEmpty 显式写 NULL），
    -- 004 的部分唯一索引正是为此写成 WHERE msg_key IS NOT NULL。
    msg_key        VARCHAR(128),
    sdk_app_id     BIGINT  NOT NULL DEFAULT 0,
    payload        JSONB   NOT NULL DEFAULT '{}'::jsonb,
    received_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- admin.go:112 按时间窗聚合（webhook 趋势图）走这条索引。
CREATE INDEX IF NOT EXISTS idx_im_webhook_events_received
    ON im_webhook_events (received_at DESC);

-- 注意：msg_key 的唯一性由 004_im.sql 的 uq_im_webhook_msgkey（部分唯一索引）负责，
-- 这里不重复创建，避免两处定义漂移。
