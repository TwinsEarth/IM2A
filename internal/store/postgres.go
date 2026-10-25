// PostgreSQL 实现（database/sql 标准库，具体驱动运行时注入）。
//
// 编译期零外部依赖：database/sql 是标准库，*sql.DB 不绑定具体驱动。
// 运行期服务主程序通过 blank import 注册驱动，例如：
//
//	import _ "github.com/jackc/pgx/v5/stdlib" // 或 github.com/lib/pq
//	store.Open("pgx", dsn)
//
// 未注册驱动时 Open 返回明确错误（资源闸门：本机离线无法拉取驱动，故自测走内存实现）。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/audit"
	"human-agent-im/internal/ledger"
	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/trustzone"
)

// driverRegistered 判断指定 database/sql 驱动是否已注册。
func driverRegistered(driver string) bool {
	for _, d := range sql.Drivers() {
		if d == driver {
			return true
		}
	}
	return false
}

// 连接池默认治理参数（v1.0.0 修复）。
// database/sql 默认 MaxOpenConns=0（无限制），高并发会打爆 PostgreSQL max_connections，
// 因此必须显式收敛；数值可在部署环境通过 PG_MAX_OPEN_CONNS 等环境变量覆盖（见 TunePoolFromEnv）。
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
)

// Open 打开数据库连接、配置连接池并做一次 Ping。driver 如 "pgx" / "postgres"。
func Open(driver, dsn string) (*sql.DB, error) {
	if !driverRegistered(driver) {
		return nil, fmt.Errorf("store: database driver %q is not registered; blank-import a postgres driver (pgx stdlib or lib/pq)", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	TunePool(db, defaultMaxOpenConns, defaultMaxIdleConns, defaultConnMaxLifetime, defaultConnMaxIdleTime)
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return db, nil
}

// TunePool 显式收敛 database/sql 连接池，避免无限连接压垮数据库。
func TunePool(db *sql.DB, maxOpen, maxIdle int, maxLifetime, maxIdleTime time.Duration) {
	if maxOpen > 0 {
		db.SetMaxOpenConns(maxOpen)
	}
	if maxIdle >= 0 {
		db.SetMaxIdleConns(maxIdle)
	}
	if maxLifetime > 0 {
		db.SetConnMaxLifetime(maxLifetime)
	}
	if maxIdleTime > 0 {
		db.SetConnMaxIdleTime(maxIdleTime)
	}
}

// NewPostgresStore 用已有 *sql.DB 构建聚合 Store。
func NewPostgresStore(db *sql.DB, dailyLedgerLimit int) *Store {
	if dailyLedgerLimit <= 0 {
		dailyLedgerLimit = 10
	}
	return &Store{
		Relation:    &pgRelation{db: db},
		Capability:  &pgCapability{db: db},
		Ledger:      &pgLedger{db: db, dailyLimit: dailyLedgerLimit},
		Approval:    &pgApproval{db: db},
		Negotiation: &pgNegotiation{db: db},
		Audit:       &pgAudit{db: db},
		Message:     NewPGMessage(db),
	}
}

// ============================================================================
// 关系链 / 信任档位
// ============================================================================

type pgRelation struct{ db *sql.DB }

func (r *pgRelation) GrantTrust(ctx context.Context, userID, targetID uint64, level trustzone.Level) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO trust_levels (user_id, target_id, level)
VALUES ($1,$2,$3)
ON CONFLICT (user_id, target_id) DO UPDATE SET level = EXCLUDED.level, granted_at = now()`,
		userID, targetID, int(level))
	return err
}

func (r *pgRelation) RevokeTrust(ctx context.Context, userID, targetID uint64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM trust_levels WHERE user_id=$1 AND target_id=$2`, userID, targetID)
	return err
}

func (r *pgRelation) GetTrust(ctx context.Context, userID, targetID uint64) (trustzone.Level, error) {
	var lv int
	err := r.db.QueryRowContext(ctx,
		`SELECT level FROM trust_levels WHERE user_id=$1 AND target_id=$2`, userID, targetID).Scan(&lv)
	if errors.Is(err, sql.ErrNoRows) {
		return trustzone.LevelStranger, ErrNotFound
	}
	if err != nil {
		return trustzone.LevelStranger, err
	}
	return trustzone.Level(lv), nil
}

func (r *pgRelation) ListTrust(ctx context.Context, userID uint64) (map[uint64]trustzone.Level, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT target_id, level FROM trust_levels WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]trustzone.Level{}
	for rows.Next() {
		var t, lv int
		if err := rows.Scan(&t, &lv); err != nil {
			return nil, err
		}
		out[uint64(t)] = trustzone.Level(lv)
	}
	return out, rows.Err()
}

// ============================================================================
// Agent 能力声明
// ============================================================================

type pgCapability struct{ db *sql.DB }

func (c *pgCapability) UpsertCapability(ctx context.Context, cap trustzone.Capability) error {
	owner := interface{}(nil)
	// 预留：Capability 暂未携带 OwnerID（owner_id 列可空）。
	_, err := c.db.ExecContext(ctx, `
INSERT INTO agent_capabilities
  (agent_id, owner_id, kind, trust_level_min, allowed_skills,
   budget_cents_per_day, requires_approval_above_cents, enabled)
VALUES ($1,$2,'sub',$3,$4,$5,$6,TRUE)
ON CONFLICT (agent_id) DO UPDATE SET
  trust_level_min = EXCLUDED.trust_level_min,
  allowed_skills = EXCLUDED.allowed_skills,
  budget_cents_per_day = EXCLUDED.budget_cents_per_day,
  requires_approval_above_cents = EXCLUDED.requires_approval_above_cents,
  enabled = TRUE, updated_at = now()`,
		cap.AgentID, owner, int(cap.TrustLevelMin), cap.AllowedSkills,
		cap.BudgetCentsPerDay, cap.RequiresApprovalAboveCents)
	return err
}

func (c *pgCapability) GetCapability(ctx context.Context, agentID string) (*trustzone.Capability, error) {
	var cap trustzone.Capability
	var min int
	err := c.db.QueryRowContext(ctx, `
SELECT agent_id, trust_level_min, allowed_skills,
       budget_cents_per_day, requires_approval_above_cents
FROM agent_capabilities WHERE agent_id=$1 AND enabled=TRUE`, agentID).
		Scan(&cap.AgentID, &min, &cap.AllowedSkills,
			&cap.BudgetCentsPerDay, &cap.RequiresApprovalAboveCents)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	cap.TrustLevelMin = trustzone.Level(min)
	return &cap, nil
}

func (c *pgCapability) ListCapabilities(ctx context.Context) ([]trustzone.Capability, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT agent_id, trust_level_min, allowed_skills,
       budget_cents_per_day, requires_approval_above_cents
FROM agent_capabilities WHERE enabled=TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trustzone.Capability
	for rows.Next() {
		var cap trustzone.Capability
		var min int
		if err := rows.Scan(&cap.AgentID, &min, &cap.AllowedSkills,
			&cap.BudgetCentsPerDay, &cap.RequiresApprovalAboveCents); err != nil {
			return nil, err
		}
		cap.TrustLevelMin = trustzone.Level(min)
		out = append(out, cap)
	}
	return out, rows.Err()
}

// ============================================================================
// 付费消息账本（不可变 + 幂等 + 单日上限）
// ============================================================================

type pgLedger struct {
	db         *sql.DB
	dailyLimit int
}

// txInsert 在事务内执行 Reserve（占位 + 日限检查），避免并发下日限穿透。
func (l *pgLedger) Reserve(messageID string, senderID, receiverID uint64, mode ledger.Mode, amountCents int64) (*ledger.Entry, error) {
	ctx := context.Background()
	err := withTx(ctx, l.db, func(tx *sql.Tx) error {
		var today int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM paid_message_ledger
			 WHERE sender_id=$1 AND created_at::date = current_date`, senderID).
			Scan(&today); err != nil {
			return err
		}
		if today >= l.dailyLimit {
			return fmt.Errorf("%w: sender=%d limit=%d", ledger.ErrDailyLimitExceeded, senderID, l.dailyLimit)
		}
		// ON CONFLICT DO NOTHING：重复占位不二次插入
		res, err := tx.ExecContext(ctx, `
INSERT INTO paid_message_ledger
  (message_id, sender_id, receiver_id, billing_mode, amount_cents, status)
VALUES ($1::uuid,$2,$3,$4,$5,0)
ON CONFLICT (message_id) DO NOTHING`,
			messageID, senderID, receiverID, int(mode), amountCents)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %s", ledger.ErrAlreadyProcessed, messageID)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO paid_message_events (message_id, from_status, to_status, note)
			 VALUES ($1::uuid,0,0,'reserved')`, messageID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return l.Get(messageID)
}

func (l *pgLedger) Charge(messageID, approvalCardID string) (*ledger.Entry, error) {
	ctx := context.Background()
	err := withTx(ctx, l.db, func(tx *sql.Tx) error {
		var fromStatus int
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM paid_message_ledger WHERE message_id=$1::uuid`, messageID).Scan(&fromStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return ledger.ErrNotFound
		}
		if err != nil {
			return err
		}
		if fromStatus == int(ledger.StatusCharged) {
			return fmt.Errorf("%w: %s already charged", ledger.ErrAlreadyProcessed, messageID)
		}
		if fromStatus != int(ledger.StatusPending) {
			return fmt.Errorf("%w: status=%d -> CHARGED", ledger.ErrInvalidTransition, fromStatus)
		}
		_, err = tx.ExecContext(ctx, `
UPDATE paid_message_ledger
SET status=1, charged_at=now(),
    approval_card_id = CASE WHEN $2 <> '' THEN $2 ELSE approval_card_id END
WHERE message_id=$1::uuid AND status=0`, messageID, approvalCardID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO paid_message_events (message_id, from_status, to_status, note)
			 VALUES ($1::uuid,0,1,'charged')`, messageID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return l.Get(messageID)
}

func (l *pgLedger) Refund(messageID, reason string) (*ledger.Entry, error) {
	ctx := context.Background()
	err := withTx(ctx, l.db, func(tx *sql.Tx) error {
		var fromStatus int
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM paid_message_ledger WHERE message_id=$1::uuid`, messageID).Scan(&fromStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return ledger.ErrNotFound
		}
		if err != nil {
			return err
		}
		if fromStatus != int(ledger.StatusCharged) {
			return fmt.Errorf("%w: status=%d -> REFUNDED", ledger.ErrInvalidTransition, fromStatus)
		}
		_, err = tx.ExecContext(ctx, `
UPDATE paid_message_ledger
SET status=2, refunded_at=now(), refund_reason=$2
WHERE message_id=$1::uuid AND status=1`, messageID, reason)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO paid_message_events (message_id, from_status, to_status, note)
			 VALUES ($1::uuid,1,2,$2)`, messageID, reason)
		return err
	})
	if err != nil {
		return nil, err
	}
	return l.Get(messageID)
}

func (l *pgLedger) Get(messageID string) (*ledger.Entry, error) {
	var e ledger.Entry
	var billingMode, status int
	var chargedAt sql.NullTime
	var card sql.NullString
	var msgID string
	err := l.db.QueryRowContext(context.Background(), `
SELECT message_id::text, sender_id, receiver_id, billing_mode, amount_cents,
       status, charged_at, approval_card_id
FROM paid_message_ledger WHERE message_id=$1::uuid`, messageID).
		Scan(&msgID, &e.SenderID, &e.ReceiverID, &billingMode, &e.AmountCents,
			&status, &chargedAt, &card)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ledger.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.MessageID = msgID
	e.Mode = ledger.Mode(billingMode)
	e.Status = ledger.Status(status)
	if chargedAt.Valid {
		e.ChargedAt = chargedAt.Time
	}
	if card.Valid {
		e.ApprovalCardID = card.String
	}
	return &e, nil
}

func (l *pgLedger) DailyUsage(senderID uint64) (sent, limit int) {
	_ = l.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM paid_message_ledger
		 WHERE sender_id=$1 AND created_at::date = current_date`, senderID).Scan(&sent)
	return sent, l.dailyLimit
}

// ============================================================================
// 审批单（事件溯源：状态列索引 + 事件追加；加载时重建聚合）
// ============================================================================

type pgApproval struct{ db *sql.DB }

func (a *pgApproval) Save(t *approval.Ticket) error {
	ctx := context.Background()
	return withTx(ctx, a.db, func(tx *sql.Tx) error {
		payload, err := json.Marshal(t.Payload)
		if err != nil {
			return err
		}
		var deadline interface{}
		if !t.Deadline.IsZero() {
			deadline = t.Deadline
		}
		var resolved interface{}
		if !t.ResolvedAt.IsZero() {
			resolved = t.ResolvedAt
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO approval_tickets
  (id, user_id, source_agent_id, ticket_type, state, payload, escalation, deadline, created_at, resolved_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (id) DO UPDATE SET
  state = EXCLUDED.state, payload = EXCLUDED.payload,
  escalation = EXCLUDED.escalation, deadline = EXCLUDED.deadline,
  resolved_at = EXCLUDED.resolved_at`,
			t.ID, t.UserID, t.SourceAgentID, string(t.Type), string(t.State),
			string(payload), t.Escalation, deadline, t.CreatedAt, resolved)
		if err != nil {
			return err
		}
		// 仅追加增量事件（按已落库条数）
		var existing int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM approval_events WHERE ticket_id=$1`, t.ID).Scan(&existing); err != nil {
			return err
		}
		for i := existing; i < len(t.Events); i++ {
			ev := t.Events[i]
			_, err = tx.ExecContext(ctx, `
INSERT INTO approval_events (ticket_id, event_type, by_who, note, created_at)
VALUES ($1,$2,$3,$4,$5)`,
				t.ID, string(ev.Type), ev.By, ev.Note, ev.At)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *pgApproval) Load(id string) (*approval.Ticket, error) {
	t, err := a.loadTicket(id)
	if err != nil {
		return nil, err
	}
	events, err := a.loadEvents(id)
	if err != nil {
		return nil, err
	}
	t.Events = events
	return t, nil
}

func (a *pgApproval) ListPending(userID uint64) ([]*approval.Ticket, error) {
	rows, err := a.db.QueryContext(context.Background(),
		`SELECT id FROM approval_tickets WHERE user_id=$1 AND state NOT IN ('COMPLETED','FAILED','REJECTED')
		 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*approval.Ticket
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, err := a.Load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (a *pgApproval) All() ([]*approval.Ticket, error) {
	rows, err := a.db.QueryContext(context.Background(), `SELECT id FROM approval_tickets ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*approval.Ticket
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, err := a.Load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (a *pgApproval) loadTicket(id string) (*approval.Ticket, error) {
	t := &approval.Ticket{}
	var typ, state, payloadStr string
	var deadline, resolved sql.NullTime
	var src sql.NullString
	err := a.db.QueryRowContext(context.Background(), `
SELECT id, user_id, source_agent_id, ticket_type, state, payload, escalation, deadline, created_at, resolved_at
FROM approval_tickets WHERE id=$1`, id).
		Scan(&t.ID, &t.UserID, &src, &typ, &state, &payloadStr, &t.Escalation,
			&deadline, &t.CreatedAt, &resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, approval.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.SourceAgentID = src.String
	t.Type = approval.TicketType(typ)
	t.State = approval.State(state)
	if payloadStr != "" {
		_ = json.Unmarshal([]byte(payloadStr), &t.Payload)
	}
	if deadline.Valid {
		t.Deadline = deadline.Time
	}
	if resolved.Valid {
		t.ResolvedAt = resolved.Time
	}
	return t, nil
}

func (a *pgApproval) loadEvents(id string) ([]approval.Event, error) {
	rows, err := a.db.QueryContext(context.Background(), `
SELECT event_type, by_who, note, created_at
FROM approval_events WHERE ticket_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []approval.Event
	for rows.Next() {
		var ev approval.Event
		var typ, by string
		var note sql.NullString
		if err := rows.Scan(&typ, &by, &note, &ev.At); err != nil {
			return nil, err
		}
		ev.Type = approval.EventType(typ)
		ev.By = by
		if note.Valid {
			ev.Note = note.String
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ============================================================================
// 协商会话（Turns 落表，加载时重建）
// ============================================================================

type pgNegotiation struct{ db *sql.DB }

// normalizeArtifactRefs 把「本轮没有 artifacts」统一成空数组，而不是 SQL NULL。
//
// 背景（v2.3.28 真库首验缺陷 2）：negotiation_turns.artifacts 在 DDL 里是
// `TEXT[] NOT NULL DEFAULT '{}'`（internal/store/migrations/001_init.sql:191），
// 即「没有 artifacts」的**合法表示是空数组**（已知的空集合），不是未知的 NULL。
// 而 negotiation.Turn.ArtifactRefs 是可选字段：调用方省略时是 nil slice，
// database/sql 会把它绑定成 NULL；显式 `VALUES(...,$7)` 又绕过列 DEFAULT，
// 于是真库报 23502。此处统一归一，保留 NOT NULL 这条不变量（改代码，不改 DDL）。
func normalizeArtifactRefs(refs []string) []string {
	if refs == nil {
		return []string{}
	}
	return refs
}

func (n *pgNegotiation) SaveSession(ctx context.Context, s *negotiation.Session) error {
	return withTx(ctx, n.db, func(tx *sql.Tx) error {
		summary, err := json.Marshal(s.Summary)
		if err != nil {
			return err
		}
		var deadline, approvalCard interface{}
		if !s.Deadline.IsZero() {
			deadline = s.Deadline
		}
		if s.ApprovalTicketID != "" {
			approvalCard = s.ApprovalTicketID
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO negotiation_sessions
  (id, state, from_agent_id, to_agent_id, max_rounds, round, token_used,
   token_budget, summary, approval_ticket_id, deadline, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
ON CONFLICT (id) DO UPDATE SET
  state=EXCLUDED.state, round=EXCLUDED.round, token_used=EXCLUDED.token_used,
  summary=EXCLUDED.summary, approval_ticket_id=EXCLUDED.approval_ticket_id,
  deadline=EXCLUDED.deadline, updated_at=now()`,
			s.ID, string(s.State), firstAgent(s), lastAgent(s), s.MaxRounds, s.Round,
			int64(s.TokenUsed), int64(s.TokenBudget), string(summary),
			approvalCard, deadline, s.CreatedAt)
		if err != nil {
			return err
		}
		// 追加新增 turns
		var maxSeq int
		_ = tx.QueryRowContext(ctx,
			`SELECT COALESCE(max(seq),0) FROM negotiation_turns WHERE session_id=$1`, s.ID).Scan(&maxSeq)
		for i := maxSeq; i < len(s.Turns); i++ {
			t := s.Turns[i]
			payload, err := json.Marshal(t.Payload)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
INSERT INTO negotiation_turns (session_id, seq, from_agent_id, to_agent_id, intent, payload, artifacts, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,now())
ON CONFLICT (session_id, seq) DO NOTHING`,
				s.ID, i+1, t.FromAgentID, t.ToAgentID, string(t.Intent), string(payload), normalizeArtifactRefs(t.ArtifactRefs))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (n *pgNegotiation) LoadSession(ctx context.Context, id string) (*negotiation.Session, error) {
	s := &negotiation.Session{}
	var state, summaryStr string
	var fromA, toA string
	var tokenUsed, tokenBudget int64
	var approvalCard sql.NullString
	var deadlineT sql.NullTime
	err := n.db.QueryRowContext(ctx, `
SELECT id, state, from_agent_id, to_agent_id, max_rounds, round, token_used,
       token_budget, summary, approval_ticket_id, deadline, created_at
FROM negotiation_sessions WHERE id=$1`, id).
		Scan(&s.ID, &state, &fromA, &toA, &s.MaxRounds, &s.Round, &tokenUsed,
			&tokenBudget, &summaryStr, &approvalCard, &deadlineT, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.State = negotiation.State(state)
	s.TokenUsed = uint64(tokenUsed)
	s.TokenBudget = uint64(tokenBudget)
	if summaryStr != "" {
		_ = json.Unmarshal([]byte(summaryStr), &s.Summary)
	}
	if approvalCard.Valid {
		s.ApprovalTicketID = approvalCard.String
	}
	if deadlineT.Valid {
		s.Deadline = deadlineT.Time
	}
	rows, err := n.db.QueryContext(ctx, `
SELECT from_agent_id, to_agent_id, intent, payload, artifacts
FROM negotiation_turns WHERE session_id=$1 ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t negotiation.Turn
		var intent, payloadStr, turnTo string
		if err := rows.Scan(&t.FromAgentID, &turnTo, &intent, &payloadStr, &t.ArtifactRefs); err != nil {
			return nil, err
		}
		t.SessionID = s.ID
		t.Intent = negotiation.Intent(intent)
		// 兼容迁移 030 之前的历史轮次：无逐轮接收方时回退到会话级 to_agent_id。
		t.ToAgentID = turnTo
		if t.ToAgentID == "" {
			t.ToAgentID = toA
		}
		_ = json.Unmarshal([]byte(payloadStr), &t.Payload)
		s.Turns = append(s.Turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func (n *pgNegotiation) ListSessions(ctx context.Context, limit int) ([]*negotiation.Session, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := n.db.QueryContext(ctx,
		`SELECT id FROM negotiation_sessions ORDER BY COALESCE(updated_at, created_at) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*negotiation.Session, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		s, err := n.LoadSession(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func firstAgent(s *negotiation.Session) string {
	if len(s.Turns) > 0 {
		return s.Turns[0].FromAgentID
	}
	return ""
}

func lastAgent(s *negotiation.Session) string {
	if len(s.Turns) > 0 {
		return s.Turns[0].ToAgentID
	}
	return ""
}

// ============================================================================
// 审计（append-only sink）
// ============================================================================

type pgAudit struct{ db *sql.DB }

func (a *pgAudit) Write(e audit.Entry) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(context.Background(), `
INSERT INTO audit_events (occurred_at, trace_id, message_id, agent_id, user_id, action, decision, detail)
VALUES ($1,NULL,$2,$3,$4,$5,$6,$7)`,
		e.At, nullableStr(e.MessageID),
		nullableStr(e.AgentID), nullableUint64(e.UserID),
		string(e.Action), nullableStr(e.Decision), string(detail))
	return err
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableUint64(u uint64) interface{} {
	if u == 0 {
		return nil
	}
	return u
}

// withTx 在事务内执行 fn，err 非 nil 时回滚。
func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
