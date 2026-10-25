-- v1.7.5：IM 消息本地镜像（入站 recv / 出站 sent），消息历史与未读查询。
CREATE TABLE IF NOT EXISTS im_messages (
    msg_key     TEXT PRIMARY KEY,
    from_user   TEXT NOT NULL,
    to_user     TEXT NOT NULL,
    msg_type    TEXT NOT NULL DEFAULT 'TIMTextElem',
    dir         TEXT NOT NULL,            -- recv | sent
    text        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_im_messages_peer ON im_messages(from_user, to_user, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_im_messages_dir_recv ON im_messages(to_user, dir, created_at DESC);
