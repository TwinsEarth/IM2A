package wechatapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"human-agent-im/internal/a2a"
	"human-agent-im/internal/agentdir"
	"human-agent-im/internal/agentquota"
	"human-agent-im/internal/wechatid"
)

// 可选依赖：v2.0.7 把 agent.create 接到真实业务链。
type Options struct {
	Quota    *agentquota.Service
	Dir      *agentdir.Service
	A2A      *a2a.Service
	WechatID *wechatid.Service
}

// WithOptions 注入真实依赖；不传则 agent.create 回 ACK 桩（向后兼容）。
func (s *Service) WithOptions(o Options) *Service {
	s.quota = o.Quota
	s.dir = o.Dir
	s.a2a = o.A2A
	s.wid = o.WechatID
	return s
}

func (s *Service) handleAgentCreate(ctx context.Context, wxid string, payload []byte) (any, error) {
	if s.quota == nil || s.dir == nil || s.a2a == nil || s.wid == nil {
		return map[string]any{"type": "ack", "echo_type": "agent.create", "status": "accepted"}, nil
	}
	var p struct {
		Role  string `json:"role"`
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.Role == "" {
		p.Role = "sub"
	}
	// 1. wxid → userID
	uid, _, err := s.wid.Identify(ctx, wxid)
	if err != nil {
		return nil, fmt.Errorf("identify: %w", err)
	}
	// 2. 额度门控
	if err := s.quota.Occupy(ctx, uid, p.Role); err != nil {
		return nil, err
	}
	// 3. 注册目录
	agentID := fmt.Sprintf("agent-%d-%s", uid, p.Role)
	a := &agentdir.Agent{
		AgentID:     agentID,
		OwnerUser:   fmt.Sprintf("%d", uid),
		DisplayName: p.Role + " agent",
		Tags:        []string{p.Role},
	}
	if err := s.dir.Upsert(ctx, a); err != nil {
		return nil, fmt.Errorf("dir upsert: %w", err)
	}
	// 4. A2A 上线消息：新 agent → 主 agent
	mainAgent := fmt.Sprintf("agent-%d-main", uid)
	_, _ = s.a2a.Send(ctx, &a2a.Envelope{
		MsgID:     fmt.Sprintf("online-%d-%d", uid, time.Now().UnixNano()),
		ConvID:    fmt.Sprintf("conv-%d-main", uid),
		FromAgent: agentID,
		ToAgent:   mainAgent,
		Type:      "agent.online",
		Payload:   map[string]any{"role": p.Role, "prompt": p.Prompt},
	})
	return map[string]any{
		"type":       "agent.create.result",
		"agent_id":   agentID,
		"owner_uid":  uid,
		"role":       p.Role,
	}, nil
}
