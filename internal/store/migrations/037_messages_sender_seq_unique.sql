-- =============================================================================
-- 037_messages_sender_seq_unique.sql（v2.3.27 / task-12）
-- 目的：给 messages 的 (sender_id, seq) 加唯一约束，把"重启归零导致同会话撞号"
--       从**静默重复**变成**显式失败**（配合 internal/msgseq 的启动播种）。
-- 适用范围（v3 决策 D8=B）：本表是**本地消息镜像**（人类消息的权威投递与续传在腾讯云
--       IM PaaS，不在本表）；该约束服务本地消息与 Agent 消息的有序/幂等。
-- =============================================================================
--
-- 为什么不能无脑 CREATE UNIQUE INDEX：
--   历史上 msgseq 是纯内存计数器（进程重启从 0 开始），而 messages.seq 一直落库，
--   所以生产库里**极可能已经存在重复的 (sender_id, seq)**。直接建唯一索引会让
--   Migrate 整体失败（migrate.go:65 不吞错）→ 服务再也起不来。
--   这正是 004_im.sql 缺表导致"全新库起不来"的同一类事故，不能再犯第二次。
--   → 本迁移先检测，有重复则只 WARN 并跳过；清理后下次启动自动补建（幂等收敛）。
--
-- 【只读检测】先在生产库手动跑这几条确认现状（不改任何数据）：
--   -- 1. 有多少组重复
--   SELECT count(*) FROM (
--     SELECT 1 FROM messages GROUP BY sender_id, seq HAVING count(*) > 1
--   ) d;
--   -- 2. 重复最严重的 sender / seq（前 20）
--   SELECT sender_id, seq, count(*) AS dup
--     FROM messages GROUP BY sender_id, seq HAVING count(*) > 1
--    ORDER BY dup DESC, sender_id LIMIT 20;
--   -- 3. 受影响的 sender 数
--   SELECT count(DISTINCT sender_id) FROM (
--     SELECT sender_id FROM messages GROUP BY sender_id, seq HAVING count(*) > 1
--   ) d;
--
-- 【清理 SQL】给人工确认后执行，**本迁移不会自动跑**（删消息是不可逆操作）：
--   规则建议：同一 (sender_id, seq) 保留 id 最小的一条（最早写入的那条就是当时真正的
--   首次分配，后来者都是重启后重复发号）。先备份：
--     CREATE TABLE messages_dup_backup_20261025 AS
--       SELECT a.* FROM messages a
--        JOIN (SELECT sender_id, seq FROM messages GROUP BY sender_id, seq HAVING count(*) > 1) d
--          ON a.sender_id = d.sender_id AND a.seq = d.seq;
--   再删重复：
--     DELETE FROM messages a
--      USING messages b
--      WHERE a.sender_id = b.sender_id AND a.seq = b.seq AND a.id > b.id;
--   清理完重启服务，本迁移会自动建出 uq_messages_sender_seq（idempotency 收敛）。
--
-- 注意：CREATE INDEX（非 CONCURRENTLY）会短暂锁表写；本表是消息镜像表、当前量级小，
--       可接受。若将来数据量很大，改由运维在低峰执行 CONCURRENTLY 并预先建好索引，
--       本迁移的 IF NOT EXISTS 就会自动跳过。
-- =============================================================================

DO $$
DECLARE
    dup_groups bigint;
BEGIN
    SELECT count(*) INTO dup_groups
      FROM (SELECT 1 FROM messages GROUP BY sender_id, seq HAVING count(*) > 1) d;

    IF dup_groups > 0 THEN
        RAISE WARNING 'messages 存在 % 组重复 (sender_id,seq)：跳过唯一索引 uq_messages_sender_seq。清理后重启会自动补建（见本迁移注释）。', dup_groups;
    ELSE
        CREATE UNIQUE INDEX IF NOT EXISTS uq_messages_sender_seq
            ON messages (sender_id, seq);
        RAISE NOTICE 'uq_messages_sender_seq 已就绪（messages 无重复）。';
    END IF;
END $$;
