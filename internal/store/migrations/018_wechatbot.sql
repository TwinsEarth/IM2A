-- v2.0.0：微信 Bot 组绑定
-- 一个微信号(wxid)只能绑定一个 Bot 组；组内最多 3 个端(iOS/Android/Windows)。
CREATE TABLE IF NOT EXISTS wechat_bot_groups (
    id TEXT PRIMARY KEY,
    owner_user TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wechat_bindings (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES wechat_bot_groups(id) ON DELETE CASCADE,
    wxid TEXT NOT NULL,
    device_type TEXT NOT NULL CHECK (device_type IN ('ios','android','windows')),
    status TEXT NOT NULL DEFAULT 'online',
    bound_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_online_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 一个微信只能绑一个组
CREATE UNIQUE INDEX IF NOT EXISTS uq_wechat_bindings_wxid ON wechat_bindings(wxid);
-- 组内每个端最多一个
CREATE UNIQUE INDEX IF NOT EXISTS uq_wechat_binding_group_device ON wechat_bindings(group_id, device_type);
