-- v2.2.1：peer_route 消息经 openclaw 出站 API 真实推送到对方微信的状态
ALTER TABLE wechat_relay_messages
    ADD COLUMN IF NOT EXISTS peer_delivered BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS peer_message_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_wechat_relay_peer ON wechat_relay_messages(peer_wxid, peer_delivered)
    WHERE peer_wxid <> '';
