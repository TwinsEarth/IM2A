// Package experience 实现双层经验库与 Propose-Commit-Audit 三分离。
// 借鉴 MemoHarness：逐案例诊断 + 蒸馏全局模式；Harness-Search：提案/提交/审计责任分离。
// 铁律：案例写入只接受人类审批决策；模式必须经人类批准才生效；审计最终裁决归人类。
package experience

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type Case struct {
	ID       int64          `json:"id"`
	UserID   string         `json:"userId"`
	Scenario string         `json:"scenario"`
	Decision string         `json:"decision"` // approve|reject|modify
	Reason   string         `json:"reason"`
	Payload  map[string]any `json:"payload"`
}

type Pattern struct {
	ID              int64   `json:"id"`
	PatternText     string  `json:"patternText"`
	SourceCaseIDs   []int64 `json:"sourceCaseIds"`
	HitCount        int64   `json:"hitCount"`
	ApprovedByHuman bool    `json:"approvedByHuman"`
}

type AuditRecord struct {
	ID            int64  `json:"id"`
	UserID        string `json:"userId"`
	Phase         string `json:"phase"`
	Proposal      string `json:"proposal"`
	CommitVerdict string `json:"commitVerdict"`
	AuditVerdict  string `json:"auditVerdict"`
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// RecordCase 唯一写入口：记录人类审批决策（不接受模型自诊断）。
func (s *Service) RecordCase(ctx context.Context, c *Case) (int64, error) {
	if c.UserID == "" || c.Scenario == "" || (c.Decision != "approve" && c.Decision != "reject" && c.Decision != "modify") {
		return 0, errors.New("experience: valid userId/scenario/decision(approve|reject|modify) required")
	}
	pj, _ := json.Marshal(c.Payload)
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO case_experiences(user_id,scenario,decision,reason,payload)
		 VALUES($1,$2,$3,$4,$5) RETURNING id`,
		c.UserID, c.Scenario, c.Decision, c.Reason, pj).Scan(&id)
	return id, err
}

func (s *Service) ListCases(ctx context.Context, userID string) ([]*Case, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,user_id,scenario,decision,reason,payload FROM case_experiences WHERE user_id=$1 ORDER BY id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Case, 0)
	for rows.Next() {
		c := &Case{}
		var pj []byte
		if err := rows.Scan(&c.ID, &c.UserID, &c.Scenario, &c.Decision, &c.Reason, &pj); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(pj, &c.Payload)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ProposePattern 蒸馏出候选模式（提案态，默认未批准）。
func (s *Service) ProposePattern(ctx context.Context, text string, caseIDs []int64) (int64, error) {
	if text == "" {
		return 0, errors.New("experience: empty pattern")
	}
	cj, _ := json.Marshal(caseIDs)
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO distilled_patterns(pattern_text,source_case_ids) VALUES($1,$2) RETURNING id`, text, cj).Scan(&id)
	return id, err
}

// ApprovePattern 人类批准模式（只此一处把 approved_by_human 置真）。
func (s *Service) ApprovePattern(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE distilled_patterns SET approved_by_human=true, approved_at=now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("experience: pattern not found")
	}
	return nil
}

// EffectivePatterns 只召回人类已批准的模式（未批准提案不进入决策）。
func (s *Service) EffectivePatterns(ctx context.Context) ([]*Pattern, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,pattern_text,source_case_ids,hit_count,approved_by_human FROM distilled_patterns
		 WHERE approved_by_human=true ORDER BY hit_count DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Pattern, 0)
	for rows.Next() {
		p := &Pattern{}
		var cj []byte
		if err := rows.Scan(&p.ID, &p.PatternText, &cj, &p.HitCount, &p.ApprovedByHuman); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cj, &p.SourceCaseIDs)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Propose 记录提案；CommitVerdict 为人类提交裁决；AuditVerdict 为最终人类审计裁决。
func (s *Service) Propose(ctx context.Context, userID, proposal string) (int64, error) {
	if userID == "" || proposal == "" {
		return 0, errors.New("experience: userId/proposal required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO propose_audit(user_id,phase,proposal) VALUES($1,'propose',$2) RETURNING id`, userID, proposal).Scan(&id)
	return id, err
}

// FinalAudit 人类最终审计裁决（责任分离：不允许模型 verdict 覆盖）。
func (s *Service) FinalAudit(ctx context.Context, id int64, commitVerdict, auditVerdict string) error {
	if auditVerdict != "approve" && auditVerdict != "reject" {
		return errors.New("experience: auditVerdict must be approve|reject (human)")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE propose_audit SET phase='audit', commit_verdict=$2, audit_verdict=$3 WHERE id=$1`, id, commitVerdict, auditVerdict)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("experience: record not found")
	}
	return nil
}

func (s *Service) GetAudit(ctx context.Context, id int64) (*AuditRecord, error) {
	r := &AuditRecord{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id,user_id,phase,proposal,commit_verdict,audit_verdict FROM propose_audit WHERE id=$1`, id).
		Scan(&r.ID, &r.UserID, &r.Phase, &r.Proposal, &r.CommitVerdict, &r.AuditVerdict)
	return r, err
}
