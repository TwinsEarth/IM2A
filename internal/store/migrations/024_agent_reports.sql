CREATE TABLE IF NOT EXISTS agent_reports (
  id          BIGSERIAL PRIMARY KEY,
  user_id     TEXT NOT NULL,
  report_date DATE NOT NULL DEFAULT current_date,
  summary     JSONB NOT NULL,
  status      TEXT NOT NULL DEFAULT 'draft',
  approved_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id, report_date)
);
