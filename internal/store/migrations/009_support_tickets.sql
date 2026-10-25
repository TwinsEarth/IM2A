-- v1.7.8 客服工单：用户反馈 → 工单 → 处理闭环
CREATE TABLE IF NOT EXISTS support_tickets (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_no TEXT UNIQUE NOT NULL,
  user_id BIGINT,
  contact TEXT,
  category TEXT NOT NULL DEFAULT 'other',
  subject TEXT NOT NULL,
  content TEXT NOT NULL,
  priority SMALLINT NOT NULL DEFAULT 1,
  status SMALLINT NOT NULL DEFAULT 1,
  assignee TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_support_status ON support_tickets(status);
CREATE INDEX IF NOT EXISTS idx_support_user ON support_tickets(user_id);
CREATE INDEX IF NOT EXISTS idx_support_created ON support_tickets(created_at);

CREATE TABLE IF NOT EXISTS support_ticket_replies (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id BIGINT NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
  author_type SMALLINT NOT NULL,
  author TEXT,
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_support_replies_ticket ON support_ticket_replies(ticket_id);
