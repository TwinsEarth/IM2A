-- v1.7.10 内容审核：自动规则 + 人工审核队列
CREATE TABLE IF NOT EXISTS content_rules (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  rule_type   SMALLINT NOT NULL,          -- 1=关键词包含 2=手机号 3=URL 4=自定义正则
  pattern     TEXT NOT NULL DEFAULT '',
  action      SMALLINT NOT NULL DEFAULT 1,-- 1=直接拦截 2=转人工复审
  enabled     SMALLINT NOT NULL DEFAULT 1,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS content_moderation_queue (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ref            TEXT NOT NULL DEFAULT '',          -- message_id / 来源标识
  source         TEXT NOT NULL DEFAULT 'msg',
  content        TEXT NOT NULL DEFAULT '',
  auto_decision  SMALLINT NOT NULL DEFAULT 0,       -- 0=待审 1=自动放行 2=自动拦截
  hit_rules      JSONB NOT NULL DEFAULT '[]'::jsonb,
  manual_status  SMALLINT NOT NULL DEFAULT 0,       -- 0=未审 1=放行 2=拦截 3=误报
  reviewer       TEXT NOT NULL DEFAULT '',
  review_note    TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  reviewed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_cmq_status ON content_moderation_queue(manual_status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_cmq_auto ON content_moderation_queue(auto_decision);
CREATE INDEX IF NOT EXISTS idx_cmq_ref ON content_moderation_queue(ref);

INSERT INTO content_rules(code,rule_type,pattern,action,enabled) VALUES
  ('phone',2,'',1,1),
  ('url',3,'',1,1),
  ('kw_fraud',1,'诈骗',1,1),
  ('kw_remit',1,'汇款到',1,1),
  ('kw_prize',1,'中奖',1,1),
  ('kw_brush',1,'兼职刷单',1,1),
  ('kw_gamble',1,'赌博',1,1),
  ('kw_invoice_review',1,'代开发票',2,1)
ON CONFLICT (code) DO NOTHING;
