package orchestration

import (
	"context"
	"fmt"
	"strings"

	"human-agent-im/internal/memory"
)

// v2.3.10 C3：子 Agent 启动时的记忆检索与注入。
//
// 问题：现有 memory 包有三层（working/facts/episodes），但子 Agent 启动时
// 不会自动把该用户的偏好/历史喂进 prompt，等于"有记忆但不用"。
// 本文件提供 BuildSubagentContext：
//   - 按 userText 关键词检索该 Scope 下最相关的 facts（偏好/常办事）
//   - 拉最近 5 条 episodes（最近做过的事）
//   - 拼成一段简短的系统补充（≤600 字），不泄露其他用户/其他子 Agent 的数据

// MemoryReader 只列子 Agent 注入需要的读接口（由 memory.MemoryStore 实现）。
type MemoryReader interface {
	SearchFacts(ctx context.Context, sc memory.Scope, keyword string) ([]*memory.Fact, error)
	ListFacts(ctx context.Context, sc memory.Scope) ([]*memory.Fact, error)
	ListEpisodes(ctx context.Context, sc memory.Scope, q memory.EpisodeQuery) ([]*memory.Episode, error)
}

// BuildSubagentContext 把子 Agent 启动要用的记忆拼成一段文本。
// 找不到记忆时返回空串（不污染 prompt）。
func BuildSubagentContext(ctx context.Context, store MemoryReader, sc memory.Scope, userText string) string {
	if store == nil {
		return ""
	}
	var b strings.Builder

	// 1. 关键词命中的事实（偏好、常办事）
	if kw := strings.TrimSpace(userText); kw != "" {
		if facts, err := store.SearchFacts(ctx, sc, kw); err == nil {
			for _, f := range facts {
				if f == nil || f.Confidence < 0.5 {
					continue
				}
				fmt.Fprintf(&b, "你记得：%s = %s（把握 %.0f%%）\n", f.Key, f.Value, f.Confidence*100)
			}
		}
	}
	// 2. 兜底：如果关键词没命中，列最多 3 条高置信事实
	if b.Len() == 0 {
		if facts, err := store.ListFacts(ctx, sc); err == nil {
			n := 0
			for _, f := range facts {
				if f == nil || f.Confidence < 0.7 {
					continue
				}
				fmt.Fprintf(&b, "你记得：%s = %s\n", f.Key, f.Value)
				n++
				if n >= 3 {
					break
				}
			}
		}
	}

	// 3. 最近 5 条 episode
	if eps, err := store.ListEpisodes(ctx, sc, memory.EpisodeQuery{Limit: 5}); err == nil {
		for _, e := range eps {
			if e == nil {
				continue
			}
			fmt.Fprintf(&b, "最近办过：%s\n", e.Summary)
		}
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	// 硬上限：注入上下文不超过 600 字，避免挤占 prompt
	if r := []rune(out); len(r) > 600 {
		out = strings.TrimRight(string(r[:600]), "\n") + "…"
	}
	return "【你的记忆】\n" + out
}
