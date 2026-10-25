package orchestration

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"human-agent-im/internal/memory"
)

// v2.3.17：L4 灰度接线钩子。
//
// 设计原则：默认 off，对生产零影响；L4_ENABLED=on 时才介入。
// 用法：inqueue worker 在调旧 handler 之前先调 L4Hook.MaybeHandle；
// 返回 handled=true 时按 reply 处理（reply 为空表示静默不回），
// 旧链路不再跑；handled=false 时交回旧链路（由完整 chat 模型回答）。
//
// v2.3.23：接入持久三层记忆。MaybeHandle 携带 wxid：
//   - 先把 wxid 解析为稳定记忆命名空间 user_id（l4_subjects，高位偏移，重启不丢）；
//   - 规划前注入该用户的跨助理协调摘要 + 最近办事轨迹 + 管家级偏好（recentSummary）；
//   - L4 接管后把本次任务按子 Agent 落一条情景记忆（episodes），供下一轮检索；
//   - 记忆全部走 memory.Store 接口（生产 PGStore，单测 MemoryStore），可持久化。

type L4Hook struct {
	Runner *Runner
	// TrustLevel 默认信任档（生产里按用户查表，先固定 3；v2.3.24 起读注册表真实档位）。
	TrustLevel int
	// Mem 持久记忆（可选）；nil 时编排照常，只是不记不忆。
	Mem memory.Store
	// Subj 外部渠道身份→记忆命名空间解析器（可选，Mem 非 nil 时应同时提供）。
	Subj memory.SubjectResolver
	// Trust 生产注册表信任档提供者（v2.3.24）；nil 时用固定 TrustLevel 默认档。
	Trust TrustProvider

	epSeq uint64 // 情景记忆 id 去重计数
}

// TrustProvider 从生产注册表读取该用户的真实信任档（并负责首消息自动开通）。
// 由 agentregistry.Store.TrustLevel 实现；返回 false/err 时钩子回退默认档。
type TrustProvider interface {
	TrustLevel(ctx context.Context, wxid string) (int, error)
}

// EnvBool 把 on/1/true/yes 视为开（大小写不敏感），与 main.go 的 "on" 约定对齐。
func EnvBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}

// NewL4Hook 从环境变量读开关。off 或 Runner=nil 时返回 nil（调用方 nil-safe）。
func NewL4Hook(r *Runner) *L4Hook {
	if !EnvBool(os.Getenv("L4_ENABLED")) || r == nil {
		return nil
	}
	return &L4Hook{Runner: r, TrustLevel: 3}
}

// WithMemory 给钩子装配持久记忆与身份解析器，返回自身以便链式调用。
func (h *L4Hook) WithMemory(mem memory.Store, subj memory.SubjectResolver) *L4Hook {
	if h == nil {
		return nil
	}
	h.Mem = mem
	h.Subj = subj
	return h
}

// WithTrust 接入生产注册表信任档提供者（v2.3.24，门控读真实信任档）。
func (h *L4Hook) WithTrust(tp TrustProvider) *L4Hook {
	if h == nil {
		return nil
	}
	h.Trust = tp
	return h
}

// MaybeHandle 走编排管线。wxid 为微信发送方身份（用于记忆命名空间）。
// handled=false（Passthrough）：闲聊/转发/协商/系统/安全降级，交回旧链路，由完整 chat 模型回答。
// handled=true 且 reply 非空：把 reply 发回微信。
// handled=true 且 reply 为空：静默（如群聊未 @），不发也不回旧链路。
func (h *L4Hook) MaybeHandle(ctx context.Context, wxid, userText string) (reply string, handled bool) {
	if h == nil || h.Runner == nil {
		return "", false
	}

	// 1) 解析身份 + 加载记忆摘要（失败不阻断编排，仅降级为无记忆）。
	var userID uint64
	recentSummary := ""
	if h.Mem != nil && h.Subj != nil && strings.TrimSpace(wxid) != "" {
		uid, err := h.Subj.SubjectID(ctx, wxid)
		if err != nil {
			log.Printf("[l4] subject resolve failed wxid=%s: %v", wxid, err)
		} else {
			userID = uid
			recentSummary = h.buildSummary(ctx, userID)
		}
	}

	// 2) 解析真实信任档（v2.3.24，生产注册表；失败回退默认档，不阻断）。
	trust := h.TrustLevel
	if h.Trust != nil && strings.TrimSpace(wxid) != "" {
		if t, err := h.Trust.TrustLevel(ctx, wxid); err == nil {
			trust = t
		} else {
			log.Printf("[l4] trust resolve failed wxid=%s: %v (fallback %d)", wxid, err, trust)
		}
	}

	// 3) 跑编排。
	res := h.Runner.Run(ctx, userText, recentSummary, trust)
	if res.Passthrough {
		return "", false
	}

	// 4) L4 接管（澄清/办事/静默）→ 落情景记忆，供下一轮检索。
	if h.Mem != nil && userID != 0 {
		h.recordEpisode(ctx, userID, userText, res)
	}
	return res.Reply, true
}

// buildSummary 给 Commander 拼一段用户记忆（协调摘要 + 跨助理最近轨迹 + 管家级偏好）。
func (h *L4Hook) buildSummary(ctx context.Context, userID uint64) string {
	if h.Mem == nil {
		return ""
	}
	var b strings.Builder

	cm := NewCommanderMemory(h.Mem)
	if c := strings.TrimSpace(cm.GetCoordination(ctx, userID)); c != "" {
		fmt.Fprintf(&b, "【跨助理协调】%s\n", c)
	}
	if d := strings.TrimSpace(cm.UserDigest(ctx, userID, knownSubagents)); d != "" {
		fmt.Fprintf(&b, "【最近各助理办过的事】\n%s\n", d)
	}
	// 管家级稳定偏好（高置信事实，最多 3 条）。
	if facts, err := h.Mem.ListFacts(ctx, memory.Scope{UserID: userID, AgentID: CommanderAgentID}); err == nil {
		n := 0
		for _, f := range facts {
			if f == nil || f.Confidence < 0.7 {
				continue
			}
			fmt.Fprintf(&b, "【用户偏好】%s = %s\n", f.Key, f.Value)
			n++
			if n >= 3 {
				break
			}
		}
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	if r := []rune(out); len(r) > 800 {
		out = strings.TrimRight(string(r[:800]), "\n") + "…"
	}
	return out
}

// recordEpisode 把本次 L4 接管结果按子 Agent 落情景记忆。
func (h *L4Hook) recordEpisode(ctx context.Context, userID uint64, userText string, res RunnerResult) {
	outcome := "handled"
	switch {
	case res.NeedApprove:
		outcome = "pending"
	case !res.Allowed:
		outcome = "rejected"
	}
	summary := strings.TrimSpace(userText)
	if r := []rune(summary); len(r) > 120 {
		summary = strings.TrimRight(string(r[:120]), "\n") + "…"
	}
	agents := []string{CommanderAgentID}
	if len(res.Plan.Subtasks) > 0 {
		seen := map[string]bool{CommanderAgentID: true}
		for _, st := range res.Plan.Subtasks {
			if !seen[string(st.SubAgent)] {
				seen[string(st.SubAgent)] = true
				agents = append(agents, string(st.SubAgent))
			}
		}
	}
	for _, aid := range agents {
		e := &memory.Episode{
			ID:      fmt.Sprintf("ep_l4_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&h.epSeq, 1)),
			Kind:    "l4",
			Summary: summary,
			Result:  outcome,
			Tags:    []string{string(res.Plan.Intent)},
		}
		if err := h.Mem.AppendEpisode(ctx, memory.Scope{UserID: userID, AgentID: aid}, e); err != nil {
			log.Printf("[l4] append episode failed agent=%s: %v", aid, err)
		}
	}
}

// knownSubagents 是 Commander 已知的五个领域子 Agent（用于跨助理轨迹摘要）。
var knownSubagents = []string{
	string(AgentSchedule),
	string(AgentProcurement),
	string(AgentLegal),
	string(AgentParenting),
	string(AgentProperty),
}
