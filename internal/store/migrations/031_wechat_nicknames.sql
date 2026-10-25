-- v2.2.6 用户自定义昵称 + Agent 自定义昵称（微信对话里改名）
ALTER TABLE wechat_identity ADD COLUMN IF NOT EXISTS user_name  TEXT;
ALTER TABLE wechat_identity ADD COLUMN IF NOT EXISTS agent_name TEXT;
