package approval

import (
	"context"
	"log"
	"time"
)

// v2.3.8 B4：审批超时升级 worker + 执行幂等收尾。
//
// 现有 Engine.CheckTimeouts 已经按 deadline 扫全部非终态 ticket，
// 30min 推手表、2h 推短信；但没有 worker 周期性调用，也没有通知出口。
// 本文件补：
//   - EscalationSink 接口（解耦：生产里接 openclaw/微信推送）
//   - Worker ticker，周期性 CheckTimeouts 并把升级事件推到 sink
//   - 执行幂等：BeginExecutingWithHash / Complete / Fail 重复调用不报错（Transition 已幂等）

// EscalationSink 通知出口。
type EscalationSink interface {
	// Notify 把超时升级事件投递给用户。channel = watch_push / sms。
	Notify(ctx context.Context, userID uint64, ticket *Ticket, channel string) error
}

// LogSink 默认实现：只打日志，不真推送（生产替换成微信/短信）。
type LogSink struct{}

func (LogSink) Notify(_ context.Context, userID uint64, t *Ticket, channel string) error {
	log.Printf("[approval] escalation user=%d ticket=%s channel=%s state=%s",
		userID, t.ID, channel, t.State)
	return nil
}

// Worker 周期扫超时 ticket。
type Worker struct {
	engine   *Engine
	sink     EscalationSink
	interval time.Duration
	stop     chan struct{}
}

func NewWorker(e *Engine, sink EscalationSink, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if sink == nil {
		sink = LogSink{}
	}
	return &Worker{engine: e, sink: sink, interval: interval, stop: make(chan struct{})}
}

// Start 启动 ticker，ctx 取消或 Stop() 时退出。非阻塞。
func (w *Worker) Start(ctx context.Context) {
	go w.loop(ctx)
}

func (w *Worker) loop(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case now := <-t.C:
			w.Tick(ctx, now)
		}
	}
}

// Tick 跑一次 CheckTimeouts 并把升级事件投递到 sink（可单测直接调）。
func (w *Worker) Tick(ctx context.Context, now time.Time) error {
	escalated, err := w.engine.CheckTimeouts(now)
	if err != nil {
		return err
	}
	for _, tk := range escalated {
		// channel 由 Escalation 档位推断：2 = sms，1 = watch_push。
		ch := "watch_push"
		if tk.Escalation >= 2 {
			ch = "sms"
		}
		_ = w.sink.Notify(ctx, tk.UserID, tk, ch)
	}
	return nil
}

func (w *Worker) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

// 幂等执行包装：重复 Complete / Fail 不返回错误。
// 这是 B4 的"执行幂等"——网络重试、worker 重复投递不会重复扣费/重复执行。
func CompleteIdempotent(e *Engine, id, by string) error {
	tk, err := e.Get(id)
	if err != nil {
		return err
	}
	if tk.State == StateCompleted {
		return nil
	}
	_, err = e.Complete(id, by)
	if err != nil && err == ErrInvalidTransition {
		// 已经在终态，幂等吞掉
		return nil
	}
	return err
}

func FailIdempotent(e *Engine, id, by, reason string) error {
	tk, err := e.Get(id)
	if err != nil {
		return err
	}
	if tk.State == StateFailed || tk.State == StateRejected {
		return nil
	}
	_, err = e.Fail(id, by, reason)
	if err != nil && err == ErrInvalidTransition {
		return nil
	}
	return err
}
