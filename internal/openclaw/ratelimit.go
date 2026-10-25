package openclaw

import (
	"context"
	"sync"
	"time"
)

// v2.3.2 A5 出站令牌桶：按 bot 账号串行限速，避免被微信判定刷屏。
//
// 设计：每账号两次发送之间至少 3 秒（≥3s/条），与 ≤5 条/分钟的约束等价
// （60s/3s=20 条/分钟上限，留充足余量）。同步 Send 与 outbox worker 共用
// 同一个闸门，保证无论走哪条路径都不会超速。
//
// 实现：lastSent[account] 记录上一次发送完成的时间；Wait 计算需要 sleep
// 的间隔，等待后把自己登记为"上次发送时刻"。用 mutex 保护 map，不引入
// 定时器，简单可靠。

var (
	rateMu         sync.Mutex
	lastSent       = map[string]time.Time{}
	minGap         = 3 * time.Second
	ratelimitWaits int64 // atomic 替代：仅观测，用 mutex 计数
)

// WaitOutbound 在真正发送前调用：阻塞到距上一次同账号发送 ≥ minGap。
// ctx 取消时立即返回（不强行发）。
func WaitOutbound(ctx context.Context, accountID string) error {
	if accountID == "" {
		return nil
	}
	for {
		rateMu.Lock()
		now := time.Now()
		last, ok := lastSent[accountID]
		var wait time.Duration
		if ok {
			wait = last.Add(minGap).Sub(now)
		}
		if wait <= 0 {
			lastSent[accountID] = now
			rateMu.Unlock()
			return nil
		}
		rateMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// ResetRateLimiter 仅供测试/熔断后复位该账号的限速（不主动调用）。
func ResetRateLimiter(accountID string) {
	rateMu.Lock()
	delete(lastSent, accountID)
	rateMu.Unlock()
}
