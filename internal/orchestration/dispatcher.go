package orchestration

import (
	"context"
	"errors"
	"time"
)

// ToolExecutor 是 Dispatcher / Runner 真正执行工具调用的接口（v2.3.25 起接 skillruntime）。
// 未注入 executor 时，任务计划只记录、不执行，避免幻觉动作（"假已办"）。
// trustLevel 是经 PermissionGate 裁决使用的同一信任档，传给执行侧做二次门控，不采信模型自报。
type ToolExecutor interface {
	Execute(ctx context.Context, sub SubAgentID, step ToolStep, trustLevel int) (string, error)
}

// ErrToolNotWired 表示编排层识别了该工具，但尚无真实 Skill 接通（不能假装已办）。
// 适配层（skillbridge）对未接入的领域工具返回该错误；Runner 据实回告，不回"已安排好"。
var ErrToolNotWired = errors.New("tool not wired to a real skill yet")

// Dispatcher 接收 Commander 的 Plan，按依赖图并行/串行编排，并强制有界预算。
type Dispatcher struct {
	bounds Bounds
	exec   ToolExecutor // 可为 nil（骨架期）
	now    func() time.Time
}

func NewDispatcher(b Bounds, exec ToolExecutor) *Dispatcher {
	if b.MaxSteps <= 0 {
		b = DefaultBounds()
	}
	return &Dispatcher{bounds: b, exec: exec, now: time.Now}
}

// Outcome 是一次调度的结果。
type Outcome struct {
	OK       bool
	Circuit  bool     // 是否因预算/步数熔断
	Messages []string // 子任务结论（handoff 增量）
}

// Dispatch 执行 Plan。Phase A 骨架：只做合法性与有界性检查，不真正调工具。
func (d *Dispatcher) Dispatch(ctx context.Context, p Plan) Outcome {
	if d == nil {
		return Outcome{OK: false}
	}
	if p.Intent != IntentTask || len(p.Subtasks) == 0 {
		// 非任务直接交回快速通道
		return Outcome{OK: true}
	}
	if len(p.Subtasks) > d.bounds.MaxSubtasks {
		return Outcome{OK: false, Circuit: true}
	}
	deadline, cancel := context.WithTimeout(ctx, d.bounds.Deadline)
	defer cancel()

	steps := 0
	for _, st := range p.Subtasks {
		for range st.Steps {
			steps++
			if steps > d.bounds.MaxSteps {
				return Outcome{OK: false, Circuit: true}
			}
			select {
			case <-deadline.Done():
				return Outcome{OK: false, Circuit: true}
			default:
			}
			// Phase B 起：d.exec.Execute(...)。骨架期无 executor，仅记录计划被接受。
		}
	}
	return Outcome{OK: true}
}
