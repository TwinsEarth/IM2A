// Package inqueue 是 v2.3.1 A1 的 PG 持久化入站队列。
//
// 设计对齐 L4 v2 §3.2：
//   - webhook 只验签/规范化/写 inbound_events，立即 ACK（P99 < 1s）。
//   - 后台 Worker 用 SELECT ... FOR UPDATE SKIP LOCKED 抢占 queued 事件，
//     崩溃后 status=queued 的事件不会丢，重启自动重投。
//   - idem_key 唯一：渠道 msgId 重复直接走 ON CONFLICT DO NOTHING，不重复处理。
package inqueue

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Event 是一条入站消息（webhookRequest 的落库形态）。
type Event struct {
	ID            int64
	Account       string
	FromWxid      string
	Text          string
	NegoSessionID string
	NegoIntent    string
}

// Enqueue 写入一条入站事件。duplicate=true 表示幂等键已存在，未重复入队。
func Enqueue(ctx context.Context, db *sql.DB, idemKey, account, fromWxid, text, negoSession, negoIntent string) (id int64, duplicate bool, err error) {
	if db == nil {
		return 0, false, nil
	}
	err = db.QueryRowContext(ctx, `
		INSERT INTO inbound_events (idem_key, account, from_wxid, text, nego_session_id, nego_intent)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (idem_key) DO NOTHING
		RETURNING id
	`, idemKey, account, fromWxid, text, negoSession, negoIntent).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, false, nil
}

// Handler 是 Worker 拿到一条事件后真正处理它的函数（由 relay 注入）。
type Handler func(ctx context.Context, ev Event) error

// Worker 是后台消费循环。
type Worker struct {
	db      *sql.DB
	handler Handler
	poll    time.Duration
}

func NewWorker(db *sql.DB, h Handler) *Worker {
	return &Worker{db: db, handler: h, poll: 500 * time.Millisecond}
}

// Start 启动后台消费循环（阻塞 ctx 取消）。
func (w *Worker) Start(ctx context.Context) {
	if w.db == nil || w.handler == nil {
		return
	}
	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()

	var ev Event
	var idemKey string
	err = tx.QueryRowContext(ctx, `
		SELECT id, idem_key, account, from_wxid, text, nego_session_id, nego_intent
		FROM inbound_events
		WHERE status='queued'
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&ev.ID, &idemKey, &ev.Account, &ev.FromWxid, &ev.Text, &ev.NegoSessionID, &ev.NegoIntent)
	if err != nil {
		return // 没活
	}
	// 占 processing，事务提交后别的 worker 不会再拿到同一条
	if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET status='processing' WHERE id=$1`, ev.ID); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}

	// 真正处理在事务外，避免长事务占连接
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.handler(hctx, ev); err != nil {
		log.Printf("[inqueue] event %d failed: %v", ev.ID, err)
		_, _ = w.db.ExecContext(ctx,
			`UPDATE inbound_events SET status='error', error=$2, processed_at=now() WHERE id=$1`, ev.ID, err.Error())
		return
	}
	_, _ = w.db.ExecContext(ctx,
		`UPDATE inbound_events SET status='done', processed_at=now() WHERE id=$1`, ev.ID)
}
