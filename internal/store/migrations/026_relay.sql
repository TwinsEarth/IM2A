-- v2.2.0：微信 → openclaw → IM2A relay 落库表
-- 每条经 openclaw 转发进来的微信消息都落一行，便于审计/重放/对账。
CREATE TABLE IF NOT EXISTS wechat_relay_messages (
    id          BIGSERIAL PRIMARY KEY,
    account_id  TEXT NOT NULL,              -- openclaw bot 账号（哪个 bot 收到）
    bot_wxid    TEXT NOT NULL,              -- bot 自己的微信 wxid（账号主人）
    from_wxid   TEXT NOT NULL,              -- 发消息的人
    text        TEXT NOT NULL,
    route       TEXT NOT NULL,              -- self_chat / peer_route
    reply       TEXT NOT NULL DEFAULT '',   -- 回给发送者的内容
    routed_to   TEXT NOT NULL DEFAULT '',   -- primary / fallback（实际命中的模型通道）
    peer_wxid   TEXT NOT NULL DEFAULT '',   -- peer_route 时的目标 wxid
    idem_key    TEXT NOT NULL DEFAULT '',   -- 幂等键（openclaw 工具调用可能重试）
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_wechat_relay_idem ON wechat_relay_messages(idem_key) WHERE idem_key <> '';
CREATE INDEX IF NOT EXISTS idx_wechat_relay_bot ON wechat_relay_messages(bot_wxid, created_at);
