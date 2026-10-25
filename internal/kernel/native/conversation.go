// SPDX-License-Identifier: MIT
//
// C2（task-23）：原生会话模型（n_conversations / n_conversation_members）。
//
// 边界（D8=B）：只服务原生通道（Agent/事件/原生客户端）；人类 IM 会话由腾讯云 IM PaaS
// 负责，本包只允许 paas_conv_id 存引用，不双写正文。
//
// 不变量：
//   - 成员上限按 kind 强制（DIRECT 2 / GROUP 12 / TASK 8 / AGENT_NEGOTIATION 4），
//     校验在**同一事务 + 会话行 FOR UPDATE** 内完成（无 TOCTOU），超限即回滚；
//   - status 单向状态机 active → archived，归档后成员写操作拒绝（ErrArchived）；
//   - 非成员访问一律 ErrNotFound（404 fail-closed，不泄露存在性，延续 C1 L4 原则）；
//   - client_conv_id 幂等：UNIQUE(owner_principal, client_conv_id)（per-owner 唯一）。
package native

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConvKind 会话类型。
type ConvKind string

const (
	ConvDirect           ConvKind = "DIRECT"
	ConvGroup            ConvKind = "GROUP"
	ConvTask             ConvKind = "TASK"
	ConvAgentNegotiation ConvKind = "AGENT_NEGOTIATION"
)

// MaxMembers 返回该 kind 的成员上限（含 owner）。
func (k ConvKind) MaxMembers() int {
	switch k {
	case ConvDirect:
		return 2
	case ConvGroup:
		return 12
	case ConvTask:
		return 8
	case ConvAgentNegotiation:
		return 4
	default:
		return 0
	}
}

func (k ConvKind) valid() bool { return k.MaxMembers() > 0 }

// Role 成员角色。
type Role string

const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleMember   Role = "member"
	RoleObserver Role = "observer"
)

func (r Role) valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember, RoleObserver:
		return true
	}
	return false
}

// ConvStatus 会话状态（单向状态机）。
type ConvStatus string

const (
	ConvStatusActive   ConvStatus = "active"
	ConvStatusArchived ConvStatus = "archived"
)

// 具名错误码（回报里的「会话上限与错误码表」来源）。
const (
	CodeInvalidArgument   = "INVALID_ARGUMENT"       // 400
	CodeNotFound          = "CONVERSATION_NOT_FOUND" // 404（不存在/非成员，fail-closed）
	CodeMemberLimit       = "MEMBER_LIMIT_REACHED"   // 409
	CodeArchived          = "CONVERSATION_ARCHIVED"  // 410
	CodeRoleForbidden     = "ROLE_FORBIDDEN"         // 403
	CodeOwnerMustTransfer = "OWNER_MUST_TRANSFER"    // 409
	CodeConflict          = "CONVERSATION_CONFLICT"  // 409（并发幂等唯一索引兜底）
)

// StatusForCode 把具名错误码映射为 HTTP 状态码。
func StatusForCode(code string) int {
	switch code {
	case CodeInvalidArgument:
		return 400
	case CodeNotFound:
		return 404
	case CodeMemberLimit, CodeOwnerMustTransfer, CodeConflict:
		return 409
	case CodeArchived:
		return 410
	case CodeRoleForbidden:
		return 403
	case CodeMessageTooLarge:
		return 413
	default:
		return 500
	}
}

// Error 是 native 域的具名错误（HTTP 层据此映射 status + code，不泄漏 SQL 细节）。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf 提取 native 错误码（非 native 错误返回 ""）。
func CodeOf(err error) string {
	var ne *Error
	if errors.As(err, &ne) {
		return ne.Code
	}
	return ""
}

// Conversation 是 n_conversations 一行 + 成员。
type Conversation struct {
	ID             string
	Kind           ConvKind
	Channel        string
	OwnerPrincipal int64
	Title          string
	Status         ConvStatus
	MaxMembers     int
	ClientConvID   string
	PaasConvID     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
	Members        []Member
}

// Member 是 n_conversation_members 一行。
type Member struct {
	PrincipalID int64
	Role        Role
	JoinedAt    time.Time
	LeftAt      *time.Time
	Muted       bool
}

// CreateRequest 建会话。
type CreateRequest struct {
	OwnerUserID   int64
	Kind          ConvKind
	Title         string
	ClientConvID  string
	MemberUserIDs []int64 // 初始成员（DIRECT 恰好 1 个对端）
	PaasConvID    string
}

// ConversationStore 是 C2 领域层（PG 实现；所有写操作单事务）。
type ConversationStore struct {
	db         *sql.DB
	principals *PrincipalStore
}

// NewConversationStore 构造 C2 存储。
func NewConversationStore(db *sql.DB) *ConversationStore {
	return &ConversationStore{db: db, principals: NewPrincipalStore(db)}
}

// queryer 抽象 *sql.DB / *sql.Tx。
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// conversationID 生成对外会话 id（"c_"+16字节随机 hex，无外部依赖）。
func conversationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 概率可忽略；退回时间戳保证可区分（不会用于安全场景）。
		return fmt.Sprintf("c_%d", time.Now().UnixNano())
	}
	return "c_" + hex.EncodeToString(b[:])
}

// principalForUser 只查不建（读路径 fail-closed：没有 principal 视为非成员）。
func principalForUser(ctx context.Context, q queryer, userID int64) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`SELECT id FROM n_principal WHERE kind='human' AND user_id=$1`, userID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("native: select principal user=%d: %w", userID, err)
	}
	return id, true, nil
}

// loadConversation 读取会话行（不校验成员）。
func (s *ConversationStore) loadConversation(ctx context.Context, q queryer, id string) (*Conversation, error) {
	var c Conversation
	var archived sql.NullTime
	var clientConv, paasConv sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT id, kind, channel, owner_principal, title, status, max_members,
		        client_conv_id, paas_conv_id, created_at, updated_at, archived_at
		   FROM n_conversations WHERE id = $1`, id).
		Scan(&c.ID, &c.Kind, &c.Channel, &c.OwnerPrincipal, &c.Title, &c.Status, &c.MaxMembers,
			&clientConv, &paasConv, &c.CreatedAt, &c.UpdatedAt, &archived)
	if err == sql.ErrNoRows {
		return nil, newError(CodeNotFound, "conversation %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("native: load conversation %q: %w", id, err)
	}
	c.ClientConvID, c.PaasConvID = clientConv.String, paasConv.String
	if archived.Valid {
		c.ArchivedAt = &archived.Time
	}
	return &c, nil
}

// loadMembers 读取成员（仅 active：left_at IS NULL；退出历史由软删保留在表里）。
func (s *ConversationStore) loadMembers(ctx context.Context, q queryer, convID string) ([]Member, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT principal_id, role, joined_at, left_at, muted
		   FROM n_conversation_members
		  WHERE conversation_id = $1 AND left_at IS NULL
		  ORDER BY joined_at, principal_id`, convID)
	if err != nil {
		return nil, fmt.Errorf("native: load members %q: %w", convID, err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var left sql.NullTime
		if err := rows.Scan(&m.PrincipalID, &m.Role, &m.JoinedAt, &left, &m.Muted); err != nil {
			return nil, fmt.Errorf("native: scan member: %w", err)
		}
		if left.Valid {
			m.LeftAt = &left.Time
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// activeMember 返回 active 成员；不存在/已退出返回 (nil,false,nil)。
func (s *ConversationStore) activeMember(ctx context.Context, q queryer, convID string, principalID int64) (*Member, bool, error) {
	var m Member
	var left sql.NullTime
	err := q.QueryRowContext(ctx,
		`SELECT principal_id, role, joined_at, left_at, muted
		   FROM n_conversation_members
		  WHERE conversation_id=$1 AND principal_id=$2 AND left_at IS NULL`,
		convID, principalID).Scan(&m.PrincipalID, &m.Role, &m.JoinedAt, &left, &m.Muted)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("native: active member: %w", err)
	}
	if left.Valid {
		m.LeftAt = &left.Time
	}
	return &m, true, nil
}

// MemberView / ConversationView 是对外 JSON 形状（principal 映射回 user_id）。
type MemberView struct {
	UserID      int64      `json:"user_id"`
	PrincipalID int64      `json:"principal_id"`
	Role        string     `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
	LeftAt      *time.Time `json:"left_at,omitempty"`
}

type ConversationView struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Channel      string       `json:"channel"`
	Title        string       `json:"title"`
	Status       string       `json:"status"`
	OwnerUserID  int64        `json:"owner_user_id"`
	ClientConvID string       `json:"client_conv_id,omitempty"`
	PaasConvID   string       `json:"paas_conv_id,omitempty"`
	MaxMembers   int          `json:"max_members"`
	Members      []MemberView `json:"members"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	ArchivedAt   *time.Time   `json:"archived_at,omitempty"`
}

// buildView 把领域对象映射成对外形状（批量补 user_id；成员 ≤12，逐条查可接受）。
func (s *ConversationStore) buildView(ctx context.Context, c *Conversation) (*ConversationView, error) {
	v := &ConversationView{
		ID: c.ID, Kind: string(c.Kind), Channel: c.Channel, Title: c.Title,
		Status: string(c.Status), ClientConvID: c.ClientConvID, PaasConvID: c.PaasConvID,
		MaxMembers: c.MaxMembers, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, ArchivedAt: c.ArchivedAt,
	}
	if uid, ok, err := s.principals.UserIDOf(ctx, c.OwnerPrincipal); err != nil {
		return nil, err
	} else if ok {
		v.OwnerUserID = uid
	}
	v.Members = make([]MemberView, 0, len(c.Members))
	for _, m := range c.Members {
		mv := MemberView{PrincipalID: m.PrincipalID, Role: string(m.Role), JoinedAt: m.JoinedAt, LeftAt: m.LeftAt}
		if uid, ok, err := s.principals.UserIDOf(ctx, m.PrincipalID); err != nil {
			return nil, err
		} else if ok {
			mv.UserID = uid
		}
		v.Members = append(v.Members, mv)
	}
	return v, nil
}

// viewByID 读会话 + 成员并映射对外形状（调用方已做权限校验）。
func (s *ConversationStore) viewByID(ctx context.Context, q queryer, id string) (*ConversationView, error) {
	c, err := s.loadConversation(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if c.Members, err = s.loadMembers(ctx, q, id); err != nil {
		return nil, err
	}
	return s.buildView(ctx, c)
}

// Create 创建会话（幂等：同 owner + client_conv_id 返回已有会话，created=false）。
func (s *ConversationStore) Create(ctx context.Context, req CreateRequest) (*ConversationView, bool, error) {
	kind := req.Kind
	if kind == "" {
		kind = ConvDirect
	}
	if !kind.valid() {
		return nil, false, newError(CodeInvalidArgument, "invalid kind %q", req.Kind)
	}
	if req.OwnerUserID <= 0 {
		return nil, false, newError(CodeInvalidArgument, "owner user_id required")
	}
	members := dedupeUserIDs(req.MemberUserIDs, req.OwnerUserID)
	if kind == ConvDirect && len(members) != 1 {
		return nil, false, newError(CodeInvalidArgument, "DIRECT 需要恰好一个对端成员（got %d）", len(members))
	}
	max := kind.MaxMembers()
	if 1+len(members) > max {
		return nil, false, newError(CodeMemberLimit, "kind=%s 成员上限 %d（请求 1+%d）", kind, max, len(members))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("native: begin create conversation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ownerPID, err := resolveOrCreateUser(ctx, tx, req.OwnerUserID)
	if err != nil {
		return nil, false, err
	}
	// 幂等：同 owner + client_conv_id 直接返回已有会话。
	if req.ClientConvID != "" {
		var existID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM n_conversations WHERE owner_principal=$1 AND client_conv_id=$2`,
			ownerPID, req.ClientConvID).Scan(&existID)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return nil, false, fmt.Errorf("native: commit create(idempotent): %w", err)
			}
			v, err := s.viewByID(ctx, s.db, existID)
			return v, false, err
		}
		if err != sql.ErrNoRows {
			return nil, false, fmt.Errorf("native: idempotency lookup: %w", err)
		}
	}

	id := conversationID()
	var clientConv any
	if req.ClientConvID != "" {
		clientConv = req.ClientConvID
	}
	var paasConv any
	if req.PaasConvID != "" {
		paasConv = req.PaasConvID
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO n_conversations
		   (id, kind, channel, owner_principal, title, status, max_members, client_conv_id, paas_conv_id)
		 VALUES ($1,$2,'native',$3,$4,'active',$5,$6,$7)`,
		id, kind, ownerPID, req.Title, max, clientConv, paasConv)
	if err != nil {
		// 并发同 client_conv_id：唯一索引兜底后返回已有会话。
		if req.ClientConvID != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			var existID string
			err2 := s.db.QueryRowContext(ctx,
				`SELECT id FROM n_conversations WHERE owner_principal=$1 AND client_conv_id=$2`,
				ownerPID, req.ClientConvID).Scan(&existID)
			if err2 == nil {
				v, verr := s.viewByID(ctx, s.db, existID)
				return v, false, verr
			}
			return nil, false, newError(CodeConflict, "client_conv_id 冲突且无法回读")
		}
		return nil, false, fmt.Errorf("native: insert conversation: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO n_conversation_members (conversation_id, principal_id, role)
		 VALUES ($1,$2,'owner')`, id, ownerPID); err != nil {
		return nil, false, fmt.Errorf("native: insert owner member: %w", err)
	}
	for _, uid := range members {
		pid, err := resolveOrCreateUser(ctx, tx, uid)
		if err != nil {
			return nil, false, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO n_conversation_members (conversation_id, principal_id, role)
			 VALUES ($1,$2,'member')
			 ON CONFLICT (conversation_id, principal_id) DO UPDATE
			   SET left_at = NULL, role = 'member', joined_at = now()`, id, pid); err != nil {
			return nil, false, fmt.Errorf("native: insert member uid=%d: %w", uid, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("native: commit create conversation: %w", err)
	}
	v, err := s.viewByID(ctx, s.db, id)
	return v, true, err
}

// isUniqueViolation 判断 PG 唯一约束冲突（不引入 pgx 具体错误类型，按错误文本兜底）。
func isUniqueViolation(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate key") || strings.Contains(s, "unique constraint") || strings.Contains(s, "23505")
}

// Get 读取会话（非成员 → ErrNotFound fail-closed；归档会话仍可读）。
func (s *ConversationStore) Get(ctx context.Context, convID string, viewerUserID int64) (*ConversationView, error) {
	pid, ok, err := principalForUser(ctx, s.db, viewerUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	if _, isMember, err := s.activeMember(ctx, s.db, convID, pid); err != nil {
		return nil, err
	} else if !isMember {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	return s.viewByID(ctx, s.db, convID)
}

// List 列出 viewer 作为 active 成员参与的会话（归档的也列出，便于客户端展示）。
func (s *ConversationStore) List(ctx context.Context, viewerUserID int64) ([]*ConversationView, error) {
	pid, ok, err := principalForUser(ctx, s.db, viewerUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []*ConversationView{}, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id FROM n_conversations c
		   JOIN n_conversation_members m ON m.conversation_id = c.id
		  WHERE m.principal_id=$1 AND m.left_at IS NULL
		  ORDER BY c.updated_at DESC`, pid)
	if err != nil {
		return nil, fmt.Errorf("native: list conversations: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("native: scan conversation id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*ConversationView, 0, len(ids))
	for _, id := range ids {
		v, err := s.viewByID(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// lockConversation 在事务内锁会话行（成员操作串行化，避免 TOCTOU 超限）。
func (s *ConversationStore) lockConversation(ctx context.Context, tx *sql.Tx, convID string) (*Conversation, error) {
	var c Conversation
	c.ID = convID
	err := tx.QueryRowContext(ctx,
		`SELECT kind, status, max_members, owner_principal FROM n_conversations WHERE id=$1 FOR UPDATE`,
		convID).Scan(&c.Kind, &c.Status, &c.MaxMembers, &c.OwnerPrincipal)
	if err == sql.ErrNoRows {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	if err != nil {
		return nil, fmt.Errorf("native: lock conversation %q: %w", convID, err)
	}
	return &c, nil
}

// actorMember 在事务内读取 actor 的 active 成员身份；非成员 → ErrNotFound（不泄露存在性）。
func (s *ConversationStore) actorMember(ctx context.Context, tx *sql.Tx, convID string, actorUserID int64) (*Member, error) {
	pid, ok, err := principalForUser(ctx, tx, actorUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	m, isMember, err := s.activeMember(ctx, tx, convID, pid)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	return m, nil
}

// AddMember 邀请/加入成员（owner/admin 可操作；上限在会话行锁内强制）。
func (s *ConversationStore) AddMember(ctx context.Context, convID string, actorUserID, inviteeUserID int64, role Role) (*MemberView, error) {
	if role == "" {
		role = RoleMember
	}
	if role == RoleOwner {
		return nil, newError(CodeInvalidArgument, "owner 只能通过 transfer 产生")
	}
	if !role.valid() {
		return nil, newError(CodeInvalidArgument, "invalid role %q", role)
	}
	if inviteeUserID <= 0 {
		return nil, newError(CodeInvalidArgument, "user_id required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("native: begin add member: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := s.lockConversation(ctx, tx, convID)
	if err != nil {
		return nil, err
	}
	actor, err := s.actorMember(ctx, tx, convID, actorUserID)
	if err != nil {
		return nil, err
	}
	if actor.Role != RoleOwner && actor.Role != RoleAdmin {
		return nil, newError(CodeRoleForbidden, "仅 owner/admin 可邀请成员（role=%s）", actor.Role)
	}
	if conv.Status == ConvStatusArchived {
		return nil, newError(CodeArchived, "conversation %q 已归档，不可加成员", convID)
	}
	inviteePID, err := resolveOrCreateUser(ctx, tx, inviteeUserID)
	if err != nil {
		return nil, err
	}
	if _, isMember, err := s.activeMember(ctx, tx, convID, inviteePID); err != nil {
		return nil, err
	} else if isMember {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("native: commit add member(idempotent): %w", err)
		}
		return &MemberView{UserID: inviteeUserID, PrincipalID: inviteePID, Role: string(role)}, nil
	}
	var activeCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM n_conversation_members WHERE conversation_id=$1 AND left_at IS NULL`,
		convID).Scan(&activeCount); err != nil {
		return nil, fmt.Errorf("native: count members: %w", err)
	}
	if activeCount >= conv.MaxMembers {
		// 具名错误 → defer Rollback；调用方事务已回滚，成员数保持不变。
		return nil, newError(CodeMemberLimit, "kind=%s 成员上限 %d（当前 %d）", conv.Kind, conv.MaxMembers, activeCount)
	}
	var joined time.Time
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO n_conversation_members (conversation_id, principal_id, role)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (conversation_id, principal_id) DO UPDATE
		   SET left_at = NULL, role = EXCLUDED.role, joined_at = now()
		 RETURNING joined_at`, convID, inviteePID, role).Scan(&joined); err != nil {
		return nil, fmt.Errorf("native: insert member: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE n_conversations SET updated_at=now() WHERE id=$1`, convID); err != nil {
		return nil, fmt.Errorf("native: touch conversation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("native: commit add member: %w", err)
	}
	return &MemberView{UserID: inviteeUserID, PrincipalID: inviteePID, Role: string(role), JoinedAt: joined}, nil
}

// RemoveMember 移除成员（owner 可移除除自己外任何成员；admin 只能移除 member/observer）。
func (s *ConversationStore) RemoveMember(ctx context.Context, convID string, actorUserID, targetUserID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("native: begin remove member: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := s.lockConversation(ctx, tx, convID)
	if err != nil {
		return err
	}
	if conv.Status == ConvStatusArchived {
		return newError(CodeArchived, "conversation %q 已归档，不可移除成员", convID)
	}
	actor, err := s.actorMember(ctx, tx, convID, actorUserID)
	if err != nil {
		return err
	}
	targetPID, ok, err := principalForUser(ctx, tx, targetUserID)
	if err != nil {
		return err
	}
	if !ok {
		return newError(CodeNotFound, "member user %d not found", targetUserID)
	}
	target, isMember, err := s.activeMember(ctx, tx, convID, targetPID)
	if err != nil {
		return err
	}
	if !isMember {
		return newError(CodeNotFound, "member user %d not found", targetUserID)
	}
	if target.Role == RoleOwner {
		return newError(CodeOwnerMustTransfer, "不能移除 owner（先 transfer）")
	}
	if actor.Role != RoleOwner {
		if actor.Role != RoleAdmin || target.Role == RoleAdmin {
			return newError(CodeRoleForbidden, "仅 owner 可移除 admin；admin 只能移除 member/observer")
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE n_conversation_members SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2 AND left_at IS NULL`,
		convID, targetPID); err != nil {
		return fmt.Errorf("native: remove member: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE n_conversations SET updated_at=now() WHERE id=$1`, convID); err != nil {
		return fmt.Errorf("native: touch conversation: %w", err)
	}
	return tx.Commit()
}

// Leave 退出会话（owner 必须先 transfer，保证不留无主）。
func (s *ConversationStore) Leave(ctx context.Context, convID string, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("native: begin leave: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := s.lockConversation(ctx, tx, convID)
	if err != nil {
		return err
	}
	if conv.Status == ConvStatusArchived {
		return newError(CodeArchived, "conversation %q 已归档", convID)
	}
	actor, err := s.actorMember(ctx, tx, convID, userID)
	if err != nil {
		return err
	}
	if actor.Role == RoleOwner {
		return newError(CodeOwnerMustTransfer, "owner 退出前必须 transfer")
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE n_conversation_members SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2 AND left_at IS NULL`,
		convID, actor.PrincipalID); err != nil {
		return fmt.Errorf("native: leave conversation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE n_conversations SET updated_at=now() WHERE id=$1`, convID); err != nil {
		return fmt.Errorf("native: touch conversation: %w", err)
	}
	return tx.Commit()
}

// ChangeRole 变更成员角色（仅 owner 可操作；不能授予 owner，需用 TransferOwner）。
func (s *ConversationStore) ChangeRole(ctx context.Context, convID string, actorUserID, targetUserID int64, role Role) error {
	if role == RoleOwner {
		return newError(CodeInvalidArgument, "owner 只能通过 transfer 产生")
	}
	if !role.valid() {
		return newError(CodeInvalidArgument, "invalid role %q", role)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("native: begin change role: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := s.lockConversation(ctx, tx, convID)
	if err != nil {
		return err
	}
	if conv.Status == ConvStatusArchived {
		return newError(CodeArchived, "conversation %q 已归档", convID)
	}
	actor, err := s.actorMember(ctx, tx, convID, actorUserID)
	if err != nil {
		return err
	}
	if actor.Role != RoleOwner {
		return newError(CodeRoleForbidden, "仅 owner 可变更角色（role=%s）", actor.Role)
	}
	targetPID, ok, err := principalForUser(ctx, tx, targetUserID)
	if err != nil {
		return err
	}
	if !ok {
		return newError(CodeNotFound, "member user %d not found", targetUserID)
	}
	if targetPID == actor.PrincipalID {
		return newError(CodeInvalidArgument, "不能变更自己的角色（owner 请用 transfer）")
	}
	if _, isMember, err := s.activeMember(ctx, tx, convID, targetPID); err != nil {
		return err
	} else if !isMember {
		return newError(CodeNotFound, "member user %d not found", targetUserID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE n_conversation_members SET role=$3 WHERE conversation_id=$1 AND principal_id=$2 AND left_at IS NULL`,
		convID, targetPID, role); err != nil {
		return fmt.Errorf("native: change role: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE n_conversations SET updated_at=now() WHERE id=$1`, convID); err != nil {
		return fmt.Errorf("native: touch conversation: %w", err)
	}
	return tx.Commit()
}

// TransferOwner 移交 owner（旧 owner→admin，新 owner→owner；仅 owner 可操作）。
func (s *ConversationStore) TransferOwner(ctx context.Context, convID string, actorUserID, newOwnerUserID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("native: begin transfer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := s.lockConversation(ctx, tx, convID)
	if err != nil {
		return err
	}
	if conv.Status == ConvStatusArchived {
		return newError(CodeArchived, "conversation %q 已归档", convID)
	}
	actor, err := s.actorMember(ctx, tx, convID, actorUserID)
	if err != nil {
		return err
	}
	if actor.Role != RoleOwner {
		return newError(CodeRoleForbidden, "仅 owner 可移交")
	}
	newPID, ok, err := principalForUser(ctx, tx, newOwnerUserID)
	if err != nil {
		return err
	}
	if !ok {
		return newError(CodeNotFound, "user %d is not an active member", newOwnerUserID)
	}
	if newPID == actor.PrincipalID {
		return newError(CodeInvalidArgument, "不能移交给自己")
	}
	if _, isMember, err := s.activeMember(ctx, tx, convID, newPID); err != nil {
		return err
	} else if !isMember {
		return newError(CodeNotFound, "user %d is not an active member", newOwnerUserID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE n_conversations SET owner_principal=$2, updated_at=now() WHERE id=$1`, convID, newPID); err != nil {
		return fmt.Errorf("native: transfer conversation owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE n_conversation_members SET role='admin' WHERE conversation_id=$1 AND principal_id=$2`,
		convID, actor.PrincipalID); err != nil {
		return fmt.Errorf("native: demote old owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE n_conversation_members SET role='owner' WHERE conversation_id=$1 AND principal_id=$2`,
		convID, newPID); err != nil {
		return fmt.Errorf("native: promote new owner: %w", err)
	}
	return tx.Commit()
}

// Archive 归档会话（owner only；active→archived 单向，重复归档幂等成功）。
func (s *ConversationStore) Archive(ctx context.Context, convID string, actorUserID int64) (*ConversationView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("native: begin archive: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := s.lockConversation(ctx, tx, convID)
	if err != nil {
		return nil, err
	}
	actor, err := s.actorMember(ctx, tx, convID, actorUserID)
	if err != nil {
		return nil, err
	}
	if actor.Role != RoleOwner {
		return nil, newError(CodeRoleForbidden, "仅 owner 可归档")
	}
	if conv.Status != ConvStatusArchived {
		if _, err := tx.ExecContext(ctx,
			`UPDATE n_conversations SET status='archived', archived_at=now(), updated_at=now() WHERE id=$1`,
			convID); err != nil {
			return nil, fmt.Errorf("native: archive conversation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("native: commit archive: %w", err)
	}
	return s.viewByID(ctx, s.db, convID)
}

// dedupeUserIDs 去重并去掉 owner 自己。
func dedupeUserIDs(ids []int64, owner int64) []int64 {
	seen := map[int64]bool{owner: true}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
