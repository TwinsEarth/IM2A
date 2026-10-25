package inqueue

import (
	"context"
	"log"
)

// v2.3.18：L4 中间件。
//
// 在旧 handler 之前先问 L4Hook：能接管就直接把 ReplyFunc 发回微信，
// 旧 handler 不再跑；不能接管（hook=nil 或 handled=false）就走旧链路。
//
// 用最小接口解耦，避免 inqueue 直接 import orchestration。
//
// v2.3.23：MaybeHandle 增加 wxid 参数，供 L4 记忆层做按用户命名空间的
// 记忆检索与落库（重启不丢、不串用户）。

// L4Interceptor 是 orchestration.L4Hook 的最小切面。
type L4Interceptor interface {
	MaybeHandle(ctx context.Context, wxid, text string) (reply string, handled bool)
}

// ReplyFunc 把 L4 生成的回复投递回微信（生产里由 relay 注入 openclaw 出站）。
type ReplyFunc func(ctx context.Context, ev Event, reply string) error

// WithL4 把 h 包一层 L4 前置拦截。interceptor 为 nil 时直接返回 h，零开销。
func WithL4(h Handler, interceptor L4Interceptor, reply ReplyFunc) Handler {
	if h == nil || interceptor == nil {
		return h
	}
	return func(ctx context.Context, ev Event) error {
		replyText, handled := interceptor.MaybeHandle(ctx, ev.FromWxid, ev.Text)
		if handled {
			if reply != nil && replyText != "" {
				if err := reply(ctx, ev, replyText); err != nil {
					log.Printf("[inqueue] L4 reply failed for event %d: %v", ev.ID, err)
					return err
				}
			}
			return nil
		}
		return h(ctx, ev)
	}
}
