package model

import (
	"context"
	"sync/atomic"
)

// ChainProvider 是云端主/备双通道模型链（v2.2.0）：
// 默认走 primary（DeepSeek）；primary 调用失败（网络错误、5xx、429、空响应等）时
// 自动切到 fallback（Kimi/Moonshot）。两条通道都失败才返回错误。
//
// 与 HybridProvider（端侧↔云端）正交：本链只在"云端厂商之间"做容灾。
type ChainProvider struct {
	primary  Provider
	fallback Provider

	primaryOK   int64
	fallbackOK  int64
	primaryFail int64
	bothFail    int64
}

// NewChain 构造主/备模型链。fallback 传 nil 表示不做备通道（等价于只用 primary）。
func NewChain(primary, fallback Provider) *ChainProvider {
	return &ChainProvider{primary: primary, fallback: fallback}
}

// Name 实现 Provider。
func (c *ChainProvider) Name() string {
	name := "chain:" + safeName(c.primary)
	if c.fallback != nil {
		name += "|" + safeName(c.fallback)
	}
	return name
}

func safeName(p Provider) string {
	if p == nil {
		return "<nil>"
	}
	return p.Name()
}

// Chat 实现 Provider：先 primary，失败再 fallback。
func (c *ChainProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	resp, err := c.primary.Chat(ctx, req)
	if err == nil {
		atomic.AddInt64(&c.primaryOK, 1)
		resp.RoutedTo = "primary"
		return resp, nil
	}
	atomic.AddInt64(&c.primaryFail, 1)

	if c.fallback == nil {
		atomic.AddInt64(&c.bothFail, 1)
		return nil, err
	}

	resp2, err2 := c.fallback.Chat(ctx, req)
	if err2 != nil {
		atomic.AddInt64(&c.bothFail, 1)
		// 返回两条通道的错误，便于排障
		return nil, &ChainError{PrimaryErr: err, FallbackErr: err2}
	}
	atomic.AddInt64(&c.fallbackOK, 1)
	resp2.RoutedTo = "fallback"
	return resp2, nil
}

// Stats 返回 主成功 / 备成功 / 主失败 / 双失败 计数（用于监控与断言）。
func (c *ChainProvider) Stats() (primaryOK, fallbackOK, primaryFail, bothFail int64) {
	return atomic.LoadInt64(&c.primaryOK),
		atomic.LoadInt64(&c.fallbackOK),
		atomic.LoadInt64(&c.primaryFail),
		atomic.LoadInt64(&c.bothFail)
}

// ChainError 主备双通道均失败时返回。
type ChainError struct {
	PrimaryErr  error
	FallbackErr error
}

func (e *ChainError) Error() string {
	return "model chain: primary failed: " + e.PrimaryErr.Error() +
		"; fallback failed: " + e.FallbackErr.Error()
}
