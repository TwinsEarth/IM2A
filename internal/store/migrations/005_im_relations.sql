-- v1.7.1 IM 关系链 / 五档信任档位（面向腾讯云字符串 UserID，档位值同 trustzone 0..4）
CREATE TABLE IF NOT EXISTS im_relations (
    owner_id    TEXT NOT NULL,
    peer_id     TEXT NOT NULL,
    trust_level INT  NOT NULL DEFAULT 3,
    status      TEXT NOT NULL DEFAULT 'friend',
    source      TEXT NOT NULL DEFAULT 'webhook',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, peer_id)
);
