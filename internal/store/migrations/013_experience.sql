-- v1.8.2：双层经验库（MemoHarness）+ Propose-Commit-Audit 三分离（Harness-Search）。
-- 案例层写入源严格限定为"人类审批决策"；模式层须人类批准(approved_by_human=true)才生效。
CREATE TABLE IF NOT EXISTS case_experiences (
    id          BIGSERIAL PRIMARY KEY,
    user_id     TEXT NOT NULL,
    scenario    TEXT NOT NULL,
    decision    TEXT NOT NULL,          -- approve | reject | modify
    reason      TEXT NOT NULL DEFAULT '',
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_cases_user ON case_experiences(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS distilled_patterns (
    id                BIGSERIAL PRIMARY KEY,
    pattern_text      TEXT NOT NULL,
    source_case_ids   JSONB NOT NULL DEFAULT '[]'::jsonb,
    hit_count         BIGINT NOT NULL DEFAULT 0,
    approved_by_human BOOLEAN NOT NULL DEFAULT false,
    derived_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at       TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_patterns_effective ON distilled_patterns(approved_by_human, hit_count DESC);

-- Propose-Commit-Audit：提案→提交→审计最终裁决（审计 verdict 来自人类，非模型）
CREATE TABLE IF NOT EXISTS propose_audit (
    id             BIGSERIAL PRIMARY KEY,
    user_id        TEXT NOT NULL,
    phase          TEXT NOT NULL,        -- propose | commit | audit
    proposal       TEXT NOT NULL DEFAULT '',
    commit_verdict TEXT NOT NULL DEFAULT '',  -- 提交后人类批准/驳回
    audit_verdict  TEXT NOT NULL DEFAULT '',  -- 最终审计裁决（人类）
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pa_user ON propose_audit(user_id, created_at DESC);
