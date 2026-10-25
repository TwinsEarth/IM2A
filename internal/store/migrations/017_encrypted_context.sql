-- v1.9.2：端到端加密 Context。服务端只存 nonce + GCM 密文，不存密钥。
CREATE TABLE IF NOT EXISTS encrypted_context (
    id           bigserial PRIMARY KEY,
    user_id      bigint      NOT NULL,
    realm        text        NOT NULL,
    kid          text        NOT NULL DEFAULT 'client-aes256-gcm',
    nonce        bytea       NOT NULL,
    ciphertext   bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_encrypted_context_user ON encrypted_context(user_id, realm);
