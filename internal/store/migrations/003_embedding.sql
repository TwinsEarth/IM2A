-- v1.0.2 修复：语义记忆向量预留列。
-- 当前环境 PG16 无法安装 pgvector，故用 JSONB 存放 float32 向量数组，
-- 应用层可真实读写；未来装上 pgvector 后可平滑迁移为 vector(n) 并建索引。
-- 幂等：可重复执行。
ALTER TABLE facts ADD COLUMN IF NOT EXISTS embedding JSONB;
