-- v1.9.0：JWT 身份地基
-- users 增加明文 identifier（dev 登录 / 主体标识用），partial unique 保证同一标识映射同一用户。
-- 已有的 phone_encrypted 保留，后续接真实手机号加密登录。

ALTER TABLE users ADD COLUMN IF NOT EXISTS identifier TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_identifier
    ON users(identifier) WHERE identifier IS NOT NULL;
