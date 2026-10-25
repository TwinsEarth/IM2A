// Package broadcast 实现微信全量广播（v2.2.2）：
// 运营后台一键群发 → 遍历所有绑定用户的 bot wxid（排除官方发送号自身）→
// 经 openclaw 官方账号逐条投递，落库广播任务与投递明细，限流、并发、幂等。
package broadcast

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sender 由 openclaw 账号主动向微信发送（internal/openclaw.Sender 已满足）。
type Sender interface {
	Send(ctx context.Context, accountID, targetWxid, text string) (string, error)
}

// AccountLister 列出 openclaw accountId -> bot wxid（relay.AccountResolver 已满足）。
type AccountLister interface {
	AllAccounts() (map[string]string, error)
}

// ErrNoTargets 没有可投递的用户（只有官方发送号在线）。
var ErrNoTargets = errors.New("broadcast: no target users")

// Service 全量广播服务。
type Service struct {
	db       *sql.DB
	sender   Sender
	accounts AccountLister

	envAccount string
	workers    int
	pace       time.Duration
}

// New 构造广播服务。BROADCAST_ACCOUNT 指定官方发送 openclaw 账号，缺省自动选 admin 组。
func New(db *sql.DB, sender Sender, accounts AccountLister) *Service {
	return &Service{
		db:         db,
		sender:     sender,
		accounts:   accounts,
		envAccount: strings.TrimSpace(os.Getenv("BROADCAST_ACCOUNT")),
		workers:    4,
		pace:       1200 * time.Millisecond, // 微信风控：整体每 ≥1.2s 起一条
	}
}

// WithConcurrency 调整并发与节流（测试用）。
func (s *Service) WithConcurrency(workers int, pace time.Duration) *Service {
	if workers > 0 {
		s.workers = workers
	}
	if pace >= 0 {
		s.pace = pace
	}
	return s
}

// resolveSender 选定官方发送 account 与其 bot wxid。
func (s *Service) resolveSender(ctx context.Context, all map[string]string) (string, string, error) {
	if s.envAccount != "" {
		if w, ok := all[s.envAccount]; ok {
			return s.envAccount, w, nil
		}
	}
	// 优先 admin 组绑定的 wxid。
	var adminWxid string
	err := s.db.QueryRowContext(ctx,
		`SELECT b.wxid FROM wechat_bindings b
		   JOIN wechat_bot_groups g ON g.id = b.group_id
		  WHERE g.owner_user = 'admin' AND b.status = 'online'
		  ORDER BY b.bound_at LIMIT 1`).Scan(&adminWxid)
	if err == nil {
		for acc, w := range all {
			if w == adminWxid {
				return acc, w, nil
			}
		}
	}
	// 兜底：accountId 最小的在线账号（确定性）。
	accs := make([]string, 0, len(all))
	for a := range all {
		accs = append(accs, a)
	}
	if len(accs) == 0 {
		return "", "", errors.New("broadcast: no openclaw account online")
	}
	sort.Strings(accs)
	return accs[0], all[accs[0]], nil
}

// Targets 返回待投递用户 wxid（排除发送方自身）及官方发送 account/wxid。
func (s *Service) Targets(ctx context.Context) (targets []string, senderAccount, senderWxid string, err error) {
	all, err := s.accounts.AllAccounts()
	if err != nil {
		return nil, "", "", fmt.Errorf("broadcast: list accounts: %w", err)
	}
	senderAccount, senderWxid, err = s.resolveSender(ctx, all)
	if err != nil {
		return nil, "", "", err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT wxid FROM wechat_bindings WHERE status = 'online' ORDER BY wxid`)
	if err != nil {
		return nil, "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, "", "", err
		}
		if w == senderWxid {
			continue // openclaw 不能给自己发
		}
		targets = append(targets, w)
	}
	return targets, senderAccount, senderWxid, rows.Err()
}

func newID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("bc_%d_%s", time.Now().Unix(), hex.EncodeToString(b))
}

// idemWindow 幂等键复用窗口：窗口内同键的重复请求只返回原任务，不重复群发。
const idemWindow = 10 * time.Minute

// FindByIdem 按幂等键查找窗口内已存在的广播任务，返回 (id,total,found)。
func (s *Service) FindByIdem(ctx context.Context, key string) (string, int, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", 0, false
	}
	var id string
	var total int
	cutoff := time.Now().Add(-idemWindow)
	err := s.db.QueryRowContext(ctx,
		`SELECT id,total FROM wechat_broadcasts
		  WHERE idempotency_key=$1 AND created_at > $2
		  ORDER BY created_at DESC LIMIT 1`, key, cutoff).Scan(&id, &total)
	if err != nil {
		return "", 0, false
	}
	return id, total, true
}

// Create 落一条广播任务与全部 pending 投递，并异步执行。返回任务 id 与目标数。
// idemKey 非空时做幂等：同键在窗口内重复请求（弱网重试/重复点击）只返回原任务，
// 绝不重复创建、重复群发。
func (s *Service) Create(ctx context.Context, text, createdBy, idemKey string) (string, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", 0, errors.New("broadcast: empty text")
	}
	if len([]rune(text)) > 4000 {
		return "", 0, errors.New("broadcast: text too long (max 4000)")
	}
	idemKey = strings.TrimSpace(idemKey)
	// 快速路径：窗口内已存在同键任务（最常见的"响应丢失后重试"场景），直接返回原任务。
	if id, total, ok := s.FindByIdem(ctx, idemKey); ok {
		return id, total, nil
	}
	targets, senderAccount, senderWxid, err := s.Targets(ctx)
	if err != nil {
		return "", 0, err
	}
	if len(targets) == 0 {
		return "", 0, ErrNoTargets
	}
	id := newID()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO wechat_broadcasts(id,text,sender_account,sender_wxid,status,total,created_by,idempotency_key)
		 VALUES($1,$2,$3,$4,'running',$5,$6,$7)
		 ON CONFLICT (idempotency_key) WHERE idempotency_key <> '' DO NOTHING`,
		id, text, senderAccount, senderWxid, len(targets), createdBy, idemKey)
	if err != nil {
		tx.Rollback()
		return "", 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// ON CONFLICT DO NOTHING 命中（同键任务已被并发请求抢先创建）。
		_ = tx.Rollback()
		if eid, total, found := s.FindByIdem(ctx, idemKey); found {
			return eid, total, nil
		}
		return "", 0, errors.New("broadcast: idempotent conflict, existing task not found")
	}
	for _, w := range targets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wechat_broadcast_deliveries(broadcast_id,to_wxid) VALUES($1,$2)
			 ON CONFLICT (broadcast_id,to_wxid) DO NOTHING`, id, w); err != nil {
			tx.Rollback()
			return "", 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", 0, err
	}
	go s.Run(context.Background(), id)
	return id, len(targets), nil
}

type job struct {
	id   int64
	wxid string
}

// Run 执行一条广播：worker 池 + 全局节流，逐条投递并更新明细，最后汇总状态。
// 只处理 pending 投递，重入安全（已 sent/failed 的不会重发）。
func (s *Service) Run(ctx context.Context, id string) {
	var text, senderAccount string
	err := s.db.QueryRowContext(ctx,
		`SELECT text, sender_account FROM wechat_broadcasts WHERE id=$1`, id).Scan(&text, &senderAccount)
	if err != nil {
		return
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE wechat_broadcasts SET status='running' WHERE id=$1 AND status='pending'`, id)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, to_wxid FROM wechat_broadcast_deliveries WHERE broadcast_id=$1 AND status='pending' ORDER BY id`, id)
	if err != nil {
		return
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.wxid); err == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()

	if len(jobs) == 0 {
		return
	}
	sem := make(chan struct{}, s.workers)
	ticker := time.NewTicker(s.pace)
	defer ticker.Stop()
	var wg sync.WaitGroup
	for _, j := range jobs {
		sem <- struct{}{}
		<-ticker.C // 全局节流
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			mid, err := s.sender.Send(ctx, senderAccount, j.wxid, text)
			if err != nil {
				_, _ = s.db.ExecContext(ctx,
					`UPDATE wechat_broadcast_deliveries SET status='failed', error=$2, updated_at=now() WHERE id=$1`,
					j.id, truncate(err.Error(), 500))
				return
			}
			_, _ = s.db.ExecContext(ctx,
				`UPDATE wechat_broadcast_deliveries SET status='sent', message_id=$2, updated_at=now() WHERE id=$1`,
				j.id, mid)
		}(j)
	}
	wg.Wait()

	var total, sent, failed int
	_ = s.db.QueryRowContext(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE status='sent'),
		        count(*) FILTER (WHERE status='failed')
		   FROM wechat_broadcast_deliveries WHERE broadcast_id=$1`, id).Scan(&total, &sent, &failed)
	status := "completed"
	switch {
	case sent == 0:
		status = "failed"
	case failed > 0:
		status = "partial"
	}
	_, _ = s.db.ExecContext(ctx,
		`UPDATE wechat_broadcasts SET status=$2, sent=$3, failed=$4, finished_at=now() WHERE id=$1`,
		id, status, sent, failed)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Get 返回广播任务与投递明细。
func (s *Service) Get(ctx context.Context, id string) (map[string]any, error) {
	var (
		text, senderAccount, senderWxid, status, createdBy, idemKey string
		total, sent, failed                                        int
		createdAt                                                  time.Time
		finishedAt                                                 sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT text,sender_account,sender_wxid,status,total,sent,failed,created_by,created_at,finished_at,idempotency_key
		   FROM wechat_broadcasts WHERE id=$1`, id).
		Scan(&text, &senderAccount, &senderWxid, &status, &total, &sent, &failed, &createdBy, &createdAt, &finishedAt, &idemKey)
	if err != nil {
		return nil, err
	}
	bc := map[string]any{
		"id": id, "text": text, "sender_account": senderAccount, "sender_wxid": senderWxid,
		"status": status, "total": total, "sent": sent, "failed": failed,
		"created_by": createdBy, "created_at": createdAt, "finished_at": nil,
		"idempotency_key": idemKey,
	}
	if finishedAt.Valid {
		bc["finished_at"] = finishedAt.Time
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT to_wxid,status,message_id,error,updated_at FROM wechat_broadcast_deliveries
		  WHERE broadcast_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []map[string]any
	for rows.Next() {
		var wxid, st, mid, derr string
		var updated time.Time
		if err := rows.Scan(&wxid, &st, &mid, &derr, &updated); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, map[string]any{
			"to_wxid": wxid, "status": st, "message_id": mid, "error": derr, "updated_at": updated,
		})
	}
	bc["deliveries"] = deliveries
	return bc, nil
}

// List 返回最近广播任务（不含明细）。
func (s *Service) List(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,status,total,sent,failed,created_by,created_at,finished_at,idempotency_key
		   FROM wechat_broadcasts ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, status, createdBy, idemKey string
		var total, sent, failed int
		var createdAt time.Time
		var finishedAt sql.NullTime
		if err := rows.Scan(&id, &status, &total, &sent, &failed, &createdBy, &createdAt, &finishedAt, &idemKey); err != nil {
			return nil, err
		}
		row := map[string]any{
			"id": id, "status": status, "total": total, "sent": sent, "failed": failed,
			"created_by": createdBy, "created_at": createdAt, "finished_at": nil,
			"idempotency_key": idemKey,
		}
		if finishedAt.Valid {
			row["finished_at"] = finishedAt.Time
		}
		out = append(out, row)
	}
	return out, nil
}
