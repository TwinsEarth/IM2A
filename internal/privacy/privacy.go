// Package privacy 实现数据主体权利（DSR，v1.8.6）：个人数据可携带（导出）与被遗忘权（删除/匿名化）。
//
// 对齐《个人信息保护法》第 44–47 条（查阅、复制、更正、删除）与等保 2.0。
// 删除策略：个人派生数据（记忆/画像/关系/设备/凭证）物理删除；账户匿名化（不物理删除，
// 保留外发一致性）；审计/账本/外发消息法定保留（等保审计留存 + 财税凭证）。
package privacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

type source struct {
	Category string
	Table    string
	Col      string
	TextID   bool // 归属列是 TEXT（腾讯 IM ID）还是 BIGINT（内部用户 ID）
}

// arg 为归属列绑定正确类型的参数：TEXT 列传字符串，BIGINT 列传 int64。
func (s source) arg(uid int64) any {
	if s.TextID {
		return strconv.FormatInt(uid, 10)
	}
	return uid
}

// exportSources 个人可携带数据（导出）。表名/列名来自代码常量白名单，用户值走 $1 参数。
var exportSources = []source{
	{"identity", "users", "id", false},
	{"identity", "account_identities", "user_id", false},
	{"identity", "devices", "user_id", true},
	{"relationship", "im_relations", "owner_id", true},
	{"relationship", "trust_relations", "owner_id", true},
	{"relationship", "trust_levels", "user_id", false},
	{"relationship", "trust_level_events", "user_id", false},
	{"agent", "agent_directory", "owner_user", true},
	{"agent", "agent_capabilities", "owner_id", false},
	{"memory", "working_memory", "user_id", false},
	{"memory", "episodes", "user_id", false},
	{"memory", "facts", "user_id", false},
	{"memory", "case_experiences", "user_id", true},
	{"memory", "improvements", "user_id", false},
	{"memory", "propose_audit", "user_id", true},
	{"memory", "threshold_patches", "user_id", false},
	{"approval", "approval_tickets", "user_id", false},
	{"approval", "offline_approvals", "user_id", true},
	{"approval", "ticket_state", "user_id", true},
	{"im", "im_groups", "owner_id", true},
	{"im", "im_group_members", "user_id", true},
	{"im", "im_read_receipts", "owner_id", true},
	{"im", "im_push_credentials", "user_id", true},
	{"im", "im_push_tasks", "user_id", true},
	{"im", "blacklist", "user_id", true},
	{"support", "support_tickets", "user_id", false},
}

// erasureSources 被遗忘权物理删除的个人派生数据。
// 不含 users（匿名化）、audit_events/security_events（等保审计留存）、
// paid_message_ledger（财税凭证）、messages/im_messages（涉及对方的外发消息）。
var erasureSources = []source{
	{"", "account_identities", "user_id", false},
	{"", "devices", "user_id", true},
	{"", "im_relations", "owner_id", true},
	{"", "trust_relations", "owner_id", true},
	{"", "trust_levels", "user_id", false},
	{"", "trust_level_events", "user_id", false},
	{"", "agent_directory", "owner_user", true},
	{"", "agent_capabilities", "owner_id", false},
	{"", "working_memory", "user_id", false},
	{"", "episodes", "user_id", false},
	{"", "facts", "user_id", false},
	{"", "case_experiences", "user_id", true},
	{"", "improvements", "user_id", false},
	{"", "propose_audit", "user_id", true},
	{"", "threshold_patches", "user_id", false},
	{"", "approval_tickets", "user_id", false},
	{"", "offline_approvals", "user_id", true},
	{"", "ticket_state", "user_id", true},
	{"", "im_groups", "owner_id", true},
	{"", "im_group_members", "user_id", true},
	{"", "im_read_receipts", "owner_id", true},
	{"", "im_push_credentials", "user_id", true},
	{"", "im_push_tasks", "user_id", true},
	{"", "blacklist", "user_id", true},
}

const tsFmt = `YYYY-MM-DD"T"HH24:MI:SSZ`

func newRequestID(kind string) string {
	return fmt.Sprintf("%s-%s-%s", kind, time.Now().UTC().Format("20060102"),
		strconv.FormatInt(time.Now().UnixNano(), 10))
}

// Export 聚合该用户全部个人数据（可携带权），落 DSR 记录，返回聚合结果。
func (s *Service) Export(ctx context.Context, uid int64, reason string) (map[string]any, int64, error) {
	data, err := s.collect(ctx, uid)
	if err != nil {
		return nil, 0, err
	}
	raw, _ := json.Marshal(data)
	var id int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO data_subject_requests(request_id,user_id,kind,status,result,reason,completed_at)
		 VALUES ($1,$2,'export','completed',$3,$4,now()) RETURNING id`,
		newRequestID("EXP"), uid, string(raw), reason).Scan(&id)
	if err != nil {
		return nil, 0, err
	}
	return data, id, nil
}

func (s *Service) collect(ctx context.Context, uid int64) (map[string]any, error) {
	out := map[string]any{}
	for _, src := range exportSources {
		var cnt int
		if err := s.db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", src.Table, src.Col),
			src.arg(uid)).Scan(&cnt); err != nil {
			return nil, fmt.Errorf("count %s: %w", src.Table, err)
		}
		var raw []byte
		q := fmt.Sprintf(
			"SELECT COALESCE(json_agg(row_to_json(x)),'[]'::json) FROM (SELECT * FROM %s WHERE %s = $1 LIMIT 500) x",
			src.Table, src.Col)
		if err := s.db.QueryRowContext(ctx, q, src.arg(uid)).Scan(&raw); err != nil {
			return nil, fmt.Errorf("rows %s: %w", src.Table, err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		cat, _ := out[src.Category].(map[string]any)
		if cat == nil {
			cat = map[string]any{}
			out[src.Category] = cat
		}
		cat[src.Table] = map[string]any{"count": cnt, "rows": rows}
	}
	// 付费账本：本人作为发送方或接收方均属于其个人数据。
	var lcnt int
	if err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM paid_message_ledger WHERE sender_id=$1 OR receiver_id=$1",
		uid).Scan(&lcnt); err != nil {
		return nil, err
	}
	var lraw []byte
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(json_agg(row_to_json(x)),'[]'::json) FROM (SELECT * FROM paid_message_ledger WHERE sender_id=$1 OR receiver_id=$1 LIMIT 500) x",
		uid).Scan(&lraw); err != nil {
		return nil, err
	}
	var lrows []map[string]any
	_ = json.Unmarshal(lraw, &lrows)
	out["payment"] = map[string]any{"paid_message_ledger": map[string]any{"count": lcnt, "rows": lrows}}
	return out, nil
}

// Erase 被遗忘权：事务内物理删除个人派生数据 + 账户匿名化；审计/账本法定保留。
// 返回每张表删除/匿名化行数与 DSR 记录 ID。
func (s *Service) Erase(ctx context.Context, uid int64, reason string) (map[string]int64, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	report := map[string]int64{}
	for _, src := range erasureSources {
		extra := ""
		if src.Table == "trust_levels" {
			extra = " OR target_id = $1" // 双向解除信任关系
		}
		q := fmt.Sprintf("DELETE FROM %s WHERE %s = $1%s", src.Table, src.Col, extra)
		res, err := tx.Exec(q, src.arg(uid))
		if err != nil {
			return nil, 0, fmt.Errorf("erase %s: %w", src.Table, err)
		}
		n, _ := res.RowsAffected()
		report[src.Table] = n
	}
	// 工单解除个人关联，保留工单内容（供客服处理）。
	res, err := tx.Exec("UPDATE support_tickets SET user_id=NULL WHERE user_id=$1", uid)
	if err != nil {
		return nil, 0, err
	}
	n, _ := res.RowsAffected()
	report["support_tickets(unlinked)"] = n
	// 账户匿名化（不物理删除，保证外发/账本引用一致）。
	res, err = tx.Exec("UPDATE users SET phone_encrypted=NULL, status=2, updated_at=now() WHERE id=$1", uid)
	if err != nil {
		return nil, 0, err
	}
	n, _ = res.RowsAffected()
	report["users(anonymized)"] = n

	raw, _ := json.Marshal(report)
	var id int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO data_subject_requests(request_id,user_id,kind,status,result,reason,completed_at)
		 VALUES ($1,$2,'delete','completed',$3,$4,now()) RETURNING id`,
		newRequestID("DEL"), uid, string(raw), reason).Scan(&id); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return report, id, nil
}

// Request DSR 记录（对外）。
type Request struct {
	ID          int64           `json:"id"`
	RequestID   string          `json:"request_id"`
	UserID      int64           `json:"user_id"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason"`
	CreatedAt   string          `json:"created_at"`
	CompletedAt string          `json:"completed_at"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// List 查询某用户的 DSR 记录。
func (s *Service) List(ctx context.Context, uid int64, limit int) ([]Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,request_id,user_id,kind,status,COALESCE(reason,''),
		        to_char(created_at,$3),COALESCE(to_char(completed_at,$3),''),result
		 FROM data_subject_requests WHERE user_id=$1 ORDER BY id DESC LIMIT $2`,
		uid, limit, tsFmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.ID, &r.RequestID, &r.UserID, &r.Kind, &r.Status, &r.Reason,
			&r.CreatedAt, &r.CompletedAt, &r.Result); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
