// Package orchestration 是 L4 Agent 编排层的 Go 原生实现（Commander + Dispatcher）。
//
// 设计原则（对齐 L4 v2 设计文档 §3）：
//   - Commander（管家）只做决策：激活判断、意图识别、任务拆解、子 Agent 路由；不直接调工具、不直接生成外发内容。
//   - 非法/超时/低置信一律安全降级到 chat（快速通道），绝不硬拆任务。
//   - Dispatcher 有界循环：maxSteps/deadline/预算封顶，超限即熔断，防烧 token、防死循环。
//   - 生产开关 L4_ENABLED=on 才启用；默认 off，回退到 v2.2.x 的同步 self_chat 路径。
package orchestration

import "time"

// SubAgentID 是子 Agent 的稳定标识（注册表主键）。
type SubAgentID string

const (
	AgentMaster      SubAgentID = "master"      // 总管家（协调）
	AgentSchedule    SubAgentID = "schedule"    // 日程提醒
	AgentProcurement SubAgentID = "procurement" // 采购
	AgentLegal       SubAgentID = "legal"       // 法务
	AgentParenting   SubAgentID = "parenting"   // 亲子
	AgentProperty    SubAgentID = "property"    // 物业
)

// Intent 是 Commander 对一条消息的意图判定。
type Intent string

const (
	IntentChat      Intent = "chat"       // 闲聊/问答/澄清 → 快速通道
	IntentTask      Intent = "task"       // 办事（需拆子任务）
	IntentRelay     Intent = "relay"      // 发给另一个用户（peer）
	IntentNegoReply Intent = "nego_reply" // 旧协商单的回复
	IntentSystem    Intent = "system"     // 系统指令（审批卡回复等）
)

// Activation 是否响应。
type Activation string

const (
	ActivationRespond Activation = "respond"
	ActivationIgnore  Activation = "ignore"
)

// ToolStep 是子任务里的一个工具调用提案。
type ToolStep struct {
	Tool                 string         `json:"tool"`
	Args                 map[string]any `json:"args,omitempty"`
	Risk                 string         `json:"risk,omitempty"` // none|low|high
	RequiresConfirmation bool           `json:"requires_confirmation"`
}

// SubTask 是 Commander 拆出的一个子任务。
type SubTask struct {
	SubAgent SubAgentID `json:"sub_agent"`
	Goal     string     `json:"goal"`
	Deps     []int      `json:"deps,omitempty"` // 依赖的子任务下标
	Steps    []ToolStep `json:"steps,omitempty"`
}

// Plan 是 Commander 的结构化输出（JSON Schema，对齐设计 §6）。
type Plan struct {
	Activation      Activation `json:"activation"`
	Intent          Intent     `json:"intent"`
	Confidence      float64    `json:"confidence"`
	NeedClarify     bool       `json:"need_clarify"`
	ClarifyQuestion string     `json:"clarify_question,omitempty"`
	Subtasks        []SubTask  `json:"subtasks,omitempty"`
	ReplyHint       string     `json:"reply_hint,omitempty"`
}

// SafeChatPlan 返回一个安全降级的闲聊 Plan（任何失败路径都用它）。
func SafeChatPlan(replyHint string) Plan {
	return Plan{
		Activation: ActivationRespond,
		Intent:     IntentChat,
		Confidence: 0.0,
		ReplyHint:  replyHint,
	}
}

// Bounds 是 Dispatcher 的有界循环预算。
type Bounds struct {
	MaxSteps    int           // 最大工具调用步数
	MaxSubtasks int           // 最大并行子任务数
	Deadline    time.Duration // 全链路 deadline
}

// DefaultBounds 是生产默认预算。
func DefaultBounds() Bounds {
	return Bounds{MaxSteps: 8, MaxSubtasks: 4, Deadline: 30 * time.Second}
}
