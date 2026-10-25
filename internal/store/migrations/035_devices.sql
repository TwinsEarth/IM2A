-- =============================================================================
-- 2026-10-25 P0 修复（task-11）：补 devices 表
-- =============================================================================
-- 背景（实测）：
--   `grep -i devices internal/store/migrations/*.sql` → 0 匹配（本文件之前）。
--   但生产代码在写它：
--     internal/api/multidevice.go:34  INSERT INTO devices(id, user_id, type, last_seen)
--                                     ON CONFLICT (id) DO UPDATE SET ...
--     internal/api/ops.go:35          SELECT COUNT(*) FROM devices
--     internal/privacy/privacy.go:40  {"identity", "devices", "user_id", TextID=true}
--     internal/privacy/privacy.go:71  同上（被遗忘权物理删除）
--     internal/api/cleanup_helpers_test.go:73  DELETE FROM devices WHERE id=$1
--   因为测试夹具自己 CREATE TABLE（multidevice_test.go:33），CI 绿而生产 SQL 一直失败：
--   多端注册（/v1/sync/*）与运维统计 im2a_devices_total 都会报 42P01。
--
-- 类型决策：
--   * devices.id 用 TEXT（设备 id 是客户端生成的字符串，如 ios-…，见 auth/device 用法）。
--   * devices.user_id 用 TEXT 而不是 BIGINT：internal/privacy/privacy.go:25/29 明确
--     把本表标为 TextID=true（用 strconv.FormatInt 绑定字符串），multidevice.go:34
--     绑定的也是 string；若建成 BIGINT，privacy 的导出/删除会因类型不匹配报错。
--   * 因此**不建** user_id → users(id) 外键：users.id 是 BIGINT，类型不一致时 PG 会
--     直接拒绝建 FK。跨类型引用改为应用层保证（见 notes-seq-audit.md 的后续建议）。
--   * type / last_seen 必须有缺省值：internal/privacy/privacy_real_test.go:67 只插
--     (id, user_id) 两列，依赖缺省填充。
-- =============================================================================

CREATE TABLE IF NOT EXISTS devices (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    type        TEXT NOT NULL DEFAULT '',          -- ios | android | web | desktop | watch …
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- privacy 导出/删除按 user_id 过滤；ops 统计与"某用户的设备列表"也走它。
CREATE INDEX IF NOT EXISTS idx_devices_user
    ON devices (user_id);

-- 运维：近 N 天活跃设备 / 僵尸设备清理。
CREATE INDEX IF NOT EXISTS idx_devices_last_seen
    ON devices (last_seen DESC);
