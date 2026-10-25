-- v2.3.23：L4 微信渠道身份映射。
--
-- 记忆三层表（working_memory/episodes/facts）的 user_id 是 bigint 命名空间，
-- 而微信侧的身份是 wxid（字符串）。本表把 wxid 幂等映射为稳定的序列 id；
-- 应用层在返回给记忆层时统一加上 memory.L4SubjectOffset(=1_000_000_000)，
-- 使微信渠道的记忆命名空间与真实 users.id（小整数）彻底隔开，杜绝串用户记忆。
--
-- 幂等：可在启动时重复执行。
CREATE TABLE IF NOT EXISTS l4_subjects (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wxid       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_l4_subjects_wxid ON l4_subjects (wxid);
