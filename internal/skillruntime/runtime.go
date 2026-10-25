// Skill Runtime：注册表 + 权限门控代理 + 方法分发（§3.2 / §3.3）。
package skillruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"human-agent-im/internal/trustzone"
)

// CapabilityProvider 提供 Agent 能力声明（可由 store.CapabilityStore 适配，也可进程内注入）。
type CapabilityProvider interface {
	GetCapability(ctx context.Context, agentID string) (*trustzone.Capability, error)
}

// Registry 进程内 Skill 注册表。
type Registry struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{skills: map[string]Skill{}}
}

// Register 注册一个 Skill。
func (r *Registry) Register(s Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := s.Manifest()
	if _, ok := r.skills[m.SkillID]; ok {
		return fmt.Errorf("skillruntime: skill %s already registered", m.SkillID)
	}
	r.skills[m.SkillID] = s
	return nil
}

// MustRegister 注册，失败 panic（仅用于启动期注册内置 Skill）。
func (r *Registry) MustRegister(s Skill) {
	if err := r.Register(s); err != nil {
		panic(err)
	}
}

func (r *Registry) get(skillID string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[skillID]
	return s, ok
}

// findTool 按 "skillID.toolName" 或全局唯一工具名定位 Skill。
func (r *Registry) findTool(fullName string) (Skill, *Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.skills {
		for i := range s.Tools() {
			t := s.Tools()[i]
			m := s.Manifest()
			if t.Name == fullName || m.SkillID+"."+t.Name == fullName {
				return s, &t, true
			}
		}
	}
	return nil, nil, false
}

// Runtime Skill 运行时（含门控代理）。
type Runtime struct {
	registry *Registry
	caps     CapabilityProvider
}

// NewRuntime 创建运行时。caps 可为 nil（此时按 Skill 清单自身的 trust_zone_min 做基础门控）。
func NewRuntime(reg *Registry, caps CapabilityProvider) *Runtime {
	return &Runtime{registry: reg, caps: caps}
}

// Initialize 处理 initialize（无状态：仅回协议版本/能力）。
func (rt *Runtime) Initialize() InitializeResult {
	return InitializeResult{
		ProtocolVersion: MCPProtocolVersion,
		Capabilities:    map[string]any{"tools": map[string]any{"listChanged": false}},
		ServerInfo:      map[string]any{"name": "human-agent-im-skill-runtime", "version": "0.2"},
	}
}

// ListTools 聚合所有 Skill 的工具（tools/list）。
func (rt *Runtime) ListTools() []ToolListEntry {
	rt.registry.mu.RLock()
	defer rt.registry.mu.RUnlock()
	out := []ToolListEntry{}
	for _, s := range rt.registry.skills {
		m := s.Manifest()
		for _, t := range s.Tools() {
			out = append(out, ToolListEntry{
				Tool:            t,
				SkillID:         m.SkillID,
				ReadOnlyHint:    t.ReadOnly,
				DestructiveHint: t.Destructive,
			})
		}
	}
	return out
}

// Call 执行 PreToolUse 门控 → 调用 Skill（tools/call）。
//
// 门控顺序（与 trustzone.Capability.Check 一致，代码层强制）：
//  1. Skill 白名单（agent capability.AllowedSkills）
//  2. 信任档位下限
//  3. 领域权限矩阵
//  4. 金额阈值
//  5. 预算
//
// DENIED → 不执行 Skill；REQUIRES_APPROVAL → 不执行，返回上抛（编排层生成审批单）。
func (rt *Runtime) Call(ctx context.Context, p CallParams) CallResult {
	skill, tool, ok := rt.registry.findTool(p.Name)
	if !ok {
		return CallResult{Status: "DENIED", Gate: &GateDecision{
			Decision: "DENY", Reason: "tool not found: " + p.Name, Tool: p.Name}}
	}
	manifest := skill.Manifest()

	// 解析调用上下文
	scope := tool.Scope
	if p.Gate.Scope != "" {
		scope = trustzone.Scope(p.Gate.Scope)
	}
	amount := p.Gate.AmountCents
	if amount == 0 && tool.AmountFromArgs != "" {
		amount = readAmountCents(p.Arguments, tool.AmountFromArgs)
	}
	trust := trustzone.Level(p.Gate.TrustLevel)

	// 组装门控能力：优先用 Agent 注册的 capability（含白名单/预算/阈值），
	// 否则用 Skill 清单声明的最低档位。
	cap := rt.resolveCapability(ctx, p.Gate.AgentID, manifest, tool)
	decision := cap.Check(trustzone.CallContext{
		SkillID:     manifest.SkillID,
		Scope:       scope,
		TrustLevel:  trust,
		AmountCents: amount,
	})
	gd := &GateDecision{Decision: decision.String(), Reason: decision.Reason,
		SkillID: manifest.SkillID, Tool: tool.Name}

	if !decision.Allowed && !decision.RequiresApproval {
		return CallResult{Status: "DENIED", Gate: gd}
	}
	if decision.RequiresApproval || manifest.ApprovalPolicy == ApprovalApprove ||
		manifest.ApprovalPolicy == ApprovalDual {
		return CallResult{Status: "REQUIRES_APPROVAL", Gate: gd}
	}

	text, structured, err := skill.Call(ctx, tool.Name, p.Arguments)
	if err != nil {
		return CallResult{Status: "DENIED", Gate: &GateDecision{
			Decision: "DENY", Reason: err.Error(), SkillID: manifest.SkillID, Tool: tool.Name}}
	}
	return CallResult{
		Status:     "OK",
		Content:    []ContentBlock{{Type: "text", Text: text}},
		Structured: structured,
		Gate:       gd,
	}
}

// resolveCapability 取得门控能力；缺失则回退到 Skill 清单下限（白名单包含自身）。
func (rt *Runtime) resolveCapability(ctx context.Context, agentID string, m Manifest, tool *Tool) trustzone.Capability {
	if rt.caps != nil && agentID != "" {
		if c, err := rt.caps.GetCapability(ctx, agentID); err == nil {
			return *c
		}
	}
	return trustzone.Capability{
		AgentID:       agentID,
		TrustLevelMin: m.TrustZoneMin,
		AllowedSkills: []string{m.SkillID},
	}
}

// Dispatch 解析并分发一条 JSON-RPC 请求。
func (rt *Runtime) Dispatch(ctx context.Context, raw []byte) RPCResponse {
	var req RPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return rpcError(nil, ErrCodeParse, "parse error: "+err.Error())
	}
	switch req.Method {
	case MethodInitialize:
		return rpcResult(req.ID, rt.Initialize())
	case MethodToolsList:
		return rpcResult(req.ID, ToolsListResult{Tools: rt.ListTools()})
	case MethodToolsCall:
		var p CallParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, ErrCodeInvalidParams, "invalid params: "+err.Error())
		}
		res := rt.Call(ctx, p)
		// 门控非 OK 映射为 JSON-RPC 错误码，便于客户端区分
		if res.Status == "DENIED" {
			return rpcError(req.ID, ErrCodeGateDenied, res.Gate.Reason, res)
		}
		if res.Status == "REQUIRES_APPROVAL" {
			return rpcError(req.ID, ErrCodeGateApproval, res.Gate.Reason, res)
		}
		return rpcResult(req.ID, res)
	default:
		return rpcError(req.ID, ErrCodeMethodNotFound, "method not found: "+req.Method)
	}
}

func rpcResult(id json.RawMessage, result any) RPCResponse {
	return RPCResponse{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

func rpcError(id json.RawMessage, code int, msg string, data ...any) RPCResponse {
	e := &RPCError{Code: code, Message: msg}
	if len(data) > 0 {
		e.Data = data[0]
	}
	return RPCResponse{JSONRPC: JSONRPCVersion, ID: id, Error: e}
}

// readAmountCents 从 arguments 读取金额（兼容数字/字符串）。
func readAmountCents(args map[string]any, key string) int64 {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		if x, err := strconv.ParseInt(n, 10, 64); err == nil {
			return x
		}
	}
	return 0
}
