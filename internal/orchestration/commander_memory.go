package orchestration

import (
	"context"
	"fmt"
	"strings"

	"human-agent-im/internal/memory"
)

// v2.3.13 C2：三层记忆增强（Commander 协调摘要 + 跨子 Agent 用户 digest）。
//
// v1.9.4 已经把 working/facts/episodes 三层物理层建好（pgvector + 衰减 + 强化）。
// 但 L4 v2 §2.7 还有一条硬约束："总 Agent 只持有跨子 Agent 的协调摘要，
// 不直接读取子 Agent 原始记忆向量"。这版把这条落到代码里：
//
//   - CommanderMemory.SetCoordination/GetCoordination：
//     跨子 Agent 的协调摘要写在专用命名空间 AgentID="__commander__"，
//     子 Agent 永远读不到。
//   - CommanderMemory.UserDigest：
//     给 Commander 一份"最近各子 Agent 干过啥"的短摘要（每条 Agent 最多 2 条），
//     用于路由决策，而不是直接拉子 Agent 的 facts/episodes 原始表。

const CommanderAgentID = "__commander__"

// CommanderMemory 总 Agent 专用记忆视图。
type CommanderMemory struct {
	store memory.Store
}

func NewCommanderMemory(store memory.Store) *CommanderMemory {
	return &CommanderMemory{store: store}
}

func (c *CommanderMemory) commanderScope(userID uint64) memory.Scope {
	return memory.Scope{UserID: userID, AgentID: CommanderAgentID}
}

// SetCoordination 写一份跨子 Agent 协调摘要（≤1000 字，短）。
func (c *CommanderMemory) SetCoordination(ctx context.Context, userID uint64, summary string) error {
	if c == nil || c.store == nil {
		return nil
	}
	if r := []rune(summary); len(r) > 1000 {
		summary = strings.TrimRight(string(r[:1000]), "\n") + "…"
	}
	return c.store.PutWorking(ctx, c.commanderScope(userID), "coordination", summary)
}

// GetCoordination 读协调摘要（没写过返回空串）。
func (c *CommanderMemory) GetCoordination(ctx context.Context, userID uint64) string {
	if c == nil || c.store == nil {
		return ""
	}
	v, err := c.store.GetWorking(ctx, c.commanderScope(userID), "coordination")
	if err != nil || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// UserDigest 跨子 Agent 拉一份最近活动摘要。
//   - agentIDs 是 Commander 已知的子 Agent 列表；
//   - 每个 Agent 最多取最近 2 条 episode；
//   - 输出 ≤600 字，纯文本，供 Commander 路由时拼进 prompt。
func (c *CommanderMemory) UserDigest(ctx context.Context, userID uint64, agentIDs []string) string {
	if c == nil || c.store == nil {
		return ""
	}
	var b strings.Builder
	for _, aid := range agentIDs {
		if aid == CommanderAgentID {
			continue
		}
		sc := memory.Scope{UserID: userID, AgentID: aid}
		eps, err := c.store.ListEpisodes(ctx, sc, memory.EpisodeQuery{Limit: 2})
		if err != nil || len(eps) == 0 {
			continue
		}
		parts := make([]string, 0, len(eps))
		for _, e := range eps {
			if e == nil || strings.TrimSpace(e.Summary) == "" {
				continue
			}
			parts = append(parts, e.Summary)
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "[%s] %s\n", aid, strings.Join(parts, "；"))
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	if r := []rune(out); len(r) > 600 {
		out = strings.TrimRight(string(r[:600]), "\n") + "…"
	}
	return out
}
