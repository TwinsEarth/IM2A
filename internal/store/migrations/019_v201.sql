-- v2.0.1：微信账户持久识别 + Agent 额度/VIP
CREATE TABLE IF NOT EXISTS wechat_identity (
    wxid TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    bound_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_plans (
    plan_key TEXT PRIMARY KEY,
    main_limit INTEGER NOT NULL DEFAULT 1,
    manager_limit INTEGER NOT NULL DEFAULT 10,
    sub_limit INTEGER NOT NULL DEFAULT 100,
    price_cents INTEGER NOT NULL DEFAULT 0
);

-- 用户确认档位：free / basic ¥99 / pro ¥299 / enterprise ¥999
INSERT INTO agent_plans(plan_key,main_limit,manager_limit,sub_limit,price_cents) VALUES
  ('free',1,2,10,0),
  ('basic',1,5,30,9900),
  ('pro',1,10,100,29900),
  ('enterprise',1,50,500,99900)
ON CONFLICT (plan_key) DO NOTHING;

CREATE TABLE IF NOT EXISTS agent_quota_usage (
    user_id INTEGER PRIMARY KEY,
    plan_key TEXT NOT NULL DEFAULT 'free' REFERENCES agent_plans(plan_key),
    main_used INTEGER NOT NULL DEFAULT 0,
    manager_used INTEGER NOT NULL DEFAULT 0,
    sub_used INTEGER NOT NULL DEFAULT 0
);
