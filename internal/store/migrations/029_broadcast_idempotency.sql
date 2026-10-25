-- v2.2.3 广播创建幂等：弱网/移动网络下浏览器 POST 响应可能丢失（服务端已落库并群发，
-- 但浏览器报 Failed to fetch），用户重试会重复群发。引入幂等键：
-- 前端每次"群发"生成一个键，重试/重连只命中同一条任务，绝不重复创建。
ALTER TABLE wechat_broadcasts ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';

-- 仅对非空幂等键建唯一部分索引（空键保持旧行为，可重复创建）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_bc_idem_key
    ON wechat_broadcasts(idempotency_key)
    WHERE idempotency_key <> '';
