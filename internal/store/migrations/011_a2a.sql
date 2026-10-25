-- v1.8.0 A2A envelopes
CREATE TABLE IF NOT EXISTS a2a_envelopes (
    msg_id TEXT PRIMARY KEY, conv_id TEXT NOT NULL, from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL, msg_type TEXT NOT NULL DEFAULT 'proposal',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb, delivered BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), delivered_at TIMESTAMPTZ);
CREATE INDEX IF NOT EXISTS idx_a2a_inbox ON a2a_envelopes(to_agent, delivered, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_a2a_conv  ON a2a_envelopes(conv_id, created_at);
