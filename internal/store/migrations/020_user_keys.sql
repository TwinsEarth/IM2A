-- v2.0.2：微信 ID 作为公钥身份，服务器保管加密私钥
-- 只有 wxid(公钥) 与服务器保管的私钥配对，才能解锁/调用该用户私有数据。
CREATE TABLE IF NOT EXISTS user_keys (
    wxid           TEXT PRIMARY KEY,          -- 公开身份(即"公钥标识")
    user_id        INTEGER NOT NULL,
    public_key     TEXT NOT NULL,             -- Ed25519 公钥(base64)
    private_enc    BYTEA NOT NULL,            -- 私钥经主密钥 AES-GCM 加密
    private_nonce  BYTEA NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
