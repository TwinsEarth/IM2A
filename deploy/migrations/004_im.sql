-- v1.2 重做：腾讯云 IM 双通道物理隔离（幂等回调 + 群消息持久化）。

-- 1) Webhook 回调幂等：增加 MsgKey 唯一键，腾讯云重投同一回调只落一条。
ALTER TABLE im_webhook_events ADD COLUMN IF NOT EXISTS msg_key VARCHAR(128);
ALTER TABLE im_webhook_events ADD COLUMN IF NOT EXISTS sdk_app_id BIGINT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_im_webhook_msgkey
    ON im_webhook_events (msg_key)
    WHERE msg_key IS NOT NULL;

-- 2) 群消息持久化：群广播消息必须落库，离线成员可拉取，不能只在内存 WS 推一次。
CREATE TABLE IF NOT EXISTS im_group_messages (
    id         BIGSERIAL PRIMARY KEY,
    group_id   VARCHAR(64) NOT NULL,
    from_user  VARCHAR(64) NOT NULL,
    text       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_im_group_messages_group ON im_group_messages (group_id, id DESC);
