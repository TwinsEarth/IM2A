-- v2.0.8：协商轮次补全"接收方 Agent"。
-- 原 negotiation_turns 只持久化 from_agent_id，LoadSession 把会话级 to_agent_id
-- 错误地套到每一轮，导致多轮 A2A 协商重载后无法正确判断"该谁发言"。
ALTER TABLE negotiation_turns
    ADD COLUMN IF NOT EXISTS to_agent_id VARCHAR(64) NOT NULL DEFAULT '';
