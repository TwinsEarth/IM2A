-- =============================================================================
-- 2026-10-25 P0 修复（task-11）：补 6 张「代码在读写、迁移里从未建过」的表
-- =============================================================================
-- 做法：对全仓 Go 源码提取 `INTO/FROM/UPDATE/JOIN <table>` 与 migrations 的
-- `CREATE TABLE` 做差集（脚本与结果见 docs/notes-seq-audit.md 附的差集表），
-- 排除「测试夹具自建」「EXTRACT(... FROM 列名)」等噪声后，剩余 9 张真实缺表：
--   devices（→ 035_devices.sql）
--   im_webhook_events（→ 003a_im_webhook_events.sql，须早于 004）
--   im_groups, im_group_members, ticket_state, offline_approvals,
--   trust_relations, blacklist（→ 本文件）
--   payment_ledger_legacy_v09（**故意不建**，见文件末尾说明）
--
-- 每张表的字段全部来自真实读写点，逐个标注；未在任何代码里出现的列不添加，
-- 避免"迁移里有、代码不用"的第二种漂移。
-- =============================================================================


-- -----------------------------------------------------------------------------
-- 1) im_groups —— 群（internal/api/im.go:272 建群；im.go:295 群成员列表）
--    INSERT INTO im_groups(group_id,name,owner_id) ... ON CONFLICT (group_id) DO NOTHING
--    删除路径：cleanup_helpers_test.go:26 / privacy.go:57（owner_id 归属）
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS im_groups (
    group_id    TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    owner_id    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_im_groups_owner ON im_groups (owner_id);


-- -----------------------------------------------------------------------------
-- 2) im_group_members —— 群成员（internal/api/im.go:283 建群加成员，
--    im.go:505 入群回调、im.go:511 退群回调；im.go:25 成员列表按 role 排序）
--    INSERT ... ON CONFLICT DO NOTHING 无目标列 → 依赖主键(group_id,user_id) 做冲突判定。
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS im_group_members (
    group_id    TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'member',   -- owner | admin | member
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_im_group_members_user ON im_group_members (user_id);


-- -----------------------------------------------------------------------------
-- 3) ticket_state —— 多端审批状态的**唯一事实源**（internal/api/multidevice.go:3 自述）
--    internal/api/multidevice.go:105  INSERT (ticket_id,user_id,decision,version,via_device,updated_at)
--                                     ON CONFLICT (ticket_id) DO UPDATE ...
--    multidevice.go:72                 SELECT ... FOR UPDATE（LWW 版本号）
--    internal/api/watch_approval.go:87 / approval.go 手机端同步也写本表
--    重要：updated_at 是 **BIGINT 毫秒时间戳**（客户端 clientTS，Go 侧扫进 int64），
--          不是 TIMESTAMPTZ —— 写成时间类型会让 LWW 比较直接失败。
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ticket_state (
    ticket_id   TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    decision    TEXT NOT NULL DEFAULT 'pending',  -- pending | approved | rejected
    version     BIGINT NOT NULL DEFAULT 0,
    via_device  TEXT NOT NULL DEFAULT '',
    updated_at  BIGINT NOT NULL DEFAULT 0,        -- 客户端毫秒时间戳（LWW 依据）
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_state_user ON ticket_state (user_id);


-- -----------------------------------------------------------------------------
-- 4) offline_approvals —— 离线审批本地队列（internal/api/offline_cache.go:31-127）
--    INSERT (user_id, ticket_id, decision, synced=false)
--    SELECT ... WHERE user_id=$1 AND synced=false；UPDATE ... SET synced=true
--    SELECT EXTRACT(EPOCH FROM created_at)*1000 → created_at 必须是时间类型
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS offline_approvals (
    id          BIGSERIAL PRIMARY KEY,
    user_id     TEXT NOT NULL,
    ticket_id   TEXT NOT NULL,
    decision    TEXT NOT NULL,
    synced      BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 主查询：某用户未同步的审批（offline_cache.go:58/60）——复合索引直接命中。
CREATE INDEX IF NOT EXISTS idx_offline_approvals_user_synced
    ON offline_approvals (user_id, synced);
CREATE INDEX IF NOT EXISTS idx_offline_approvals_ticket
    ON offline_approvals (ticket_id);


-- -----------------------------------------------------------------------------
-- 5) trust_relations —— 五档信任矩阵的关系条目（与 001 的 trust_levels 是**两套并存**）
--    internal/api/relation.go:38   INSERT (owner_id, contact_id, trust_level, capabilities)
--                                  ON CONFLICT(owner_id, contact_id) DO UPDATE
--    relation.go:66/112            SELECT contact_id, trust_level, capabilities
--    relation.go:100               UPDATE ... SET trust_level, updated_at=NOW()
--    internal/api/capability.go:49 SELECT trust_level（门控读路径）
--    capabilities 是 []string，pgx 默认编码为 text[]；**不能加 NOT NULL**：
--      relation.go 直接绑定 req.Capabilities，JSON 缺字段时是 nil → 显式 NULL 会绕过
--      缺省值并触发 NOT NULL 违约。
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trust_relations (
    owner_id      TEXT NOT NULL,
    contact_id    TEXT NOT NULL,
    trust_level   INT  NOT NULL DEFAULT 3,          -- 0=self 1=family 2=trusted 3=known 4=stranger 的 1-based 映射见 trustzone
    capabilities  TEXT[],
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, contact_id)
);
CREATE INDEX IF NOT EXISTS idx_trust_relations_contact ON trust_relations (contact_id);


-- -----------------------------------------------------------------------------
-- 6) blacklist —— 付费消息黑名单（internal/api/payment_ledger.go:168 写入，
--    internal/api/paid_flow.go:258 在扣费前强制拦截）
--    INSERT INTO blacklist(user_id, reason) ... ON CONFLICT (user_id) DO NOTHING
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS blacklist (
    user_id     TEXT PRIMARY KEY,
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- =============================================================================
-- 附：为什么**不**在这里建 payment_ledger_legacy_v09
-- =============================================================================
-- internal/api/payment_ledger.go 仍在对 `payment_ledger_legacy_v09` 做
-- INSERT/SELECT/UPDATE（:34 :45 :50 :67 :107 :121 :142 :147），internal/api/ops.go:34
-- 也在统计它。但这张表在仓库根 `migrations/20261023_migrate_payment_ledger_to_paid_message_ledger.sql`
-- 里被明确定义为**老表只读归档**：
--     ALTER TABLE payment_ledger RENAME TO payment_ledger_legacy_v09;   -- COMMIT 前
-- 而新的权威账本是 001_init.sql 已建的 `paid_message_ledger`（admin.go/privacy.go 的
-- 统计与导出都读它，且有生产测试 v176/v177 直接写它）。
--
-- 也就是说这里不是"漏了迁移"，而是**代码还在写一张已宣布退役的表**：
--   * 补建它 → 扣费写 legacy、统计读 paid_message_ledger，两个账本永远对不上（静默错误）；
--   * 不补   → /v1/ledger/* 在全新库上明确报 42P01（显式错误）。
-- 选择后者：显式错误优于静默错账。正确修法是让 payment_ledger.go 改写到
-- paid_message_ledger（或在 Wave F 并入 `internal/ledger` 的 DB 实现），属于代码改动，
-- 不在本迁移任务范围内。见 docs/notes-seq-audit.md §4 与回报中的 P1 条目。
-- =============================================================================
