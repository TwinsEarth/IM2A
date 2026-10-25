package orchestration

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
)

// v2.3.16 D4：端到端编排 Runner（composition root）。
//
// 前面 v2.3.0–v2.3.15 把所有零件都造好了（Commander / PermissionGate /
// ExitGuard / ProceduralMemory / DistillStats / CommanderMemory），
// 但没有一根管子把它们串起来。这个 Runner 就是那根管子：
//
//	userText
//	   → Commander.Plan（意图路由）
//	   → 闲聊/转发/协商/系统 等非"办事"意图：Passthrough=true，交回旧链路（完整 chat 模型）
//	   → 需要澄清：直接回澄清问题
//	   → 办事（task）：遍历 Subtasks，对每个 ToolStep 调 PermissionGate.Check
//	      · 全部 Allow → 合成给用户的回复，过 ExitGuard
//	      · 任意 NeedsApproval → 上抛审批（不直接发）
//	      · 任意 Deny → 友好拒绝
//	   → DistillStats 记录裁决结果
//
// 关键（v2.3.21 审查修复）：Runner 只接管"办事"（task）与"澄清"，
// 纯闲聊绝不由 256 token 的 planner 回答——那样会让 Bot 变笨；
// 闲聊一律 Passthrough 回旧链路，由完整 chat 模型作答。
//
// 不依赖 PG / HTTP / 微信，可在单测里完整跑通。

type Runner struct {
	Commander *Commander
	Gate      *PermissionGate
	Guard     *ExitGuard
	Stats     *DistillStats
	// Exec 真实工具执行器（v2.3.25）。nil 时只规划不执行（保留旧行为，便于单测/骨架期）。
	Exec ToolExecutor
}

// WithExecutor 注入真实 Skill 执行器（消除"假已办"）。返回自身便于链式装配。
func (r *Runner) WithExecutor(exec ToolExecutor) *Runner {
	if r == nil {
		return nil
	}
	r.Exec = exec
	return r
}

func NewRunner(cmd *Commander, gate *PermissionGate, guard *ExitGuard, stats *DistillStats) *Runner {
	if guard == nil {
		guard = NewExitGuard()
	}
	if stats == nil {
		stats = NewDistillStats()
	}
	return &Runner{Commander: cmd, Gate: gate, Guard: guard, Stats: stats}
}

// RunnerResult 一次编排的结果。
type RunnerResult struct {
	Reply       string // 可以直接发微信的文案（Passthrough 时为空）
	Allowed     bool   // 办事是否全部自动放行
	NeedApprove bool   // 是否需要上抛审批
	Passthrough bool   // 不由 L4 接管，交回旧链路（闲聊/转发/协商/系统/安全降级）
	Reason      string
	Plan        Plan
	Executed    bool     // v2.3.25：是否真正调用了 Skill 执行
	ExecResults []string // 各工具真实返回文本
}

// Run 跑一次编排。trustLevel 是当前用户对该用户的信任档（0–5）。
func (r *Runner) Run(ctx context.Context, userText, recentSummary string, trustLevel int) RunnerResult {
	if r == nil || r.Commander == nil {
		// 未初始化时不吞消息，交回旧链路，避免 Bot 变哑巴
		return RunnerResult{Passthrough: true, Reason: "runner nil"}
	}
	plan := r.Commander.Plan(ctx, userText, recentSummary)
	log.Printf("[l4] plan intent=%s conf=%.2f activation=%s subtasks=%d need_clarify=%q text=%q",
		plan.Intent, plan.Confidence, plan.Activation, len(plan.Subtasks), plan.ClarifyQuestion, truncate(userText, 30))

	// 激活策略：群聊未 @ 等 ignore 场景 → 静默（handled=true 且 reply 为空，中间件不发也不回旧链路）
	if plan.Activation == ActivationIgnore {
		return RunnerResult{Plan: plan, Allowed: true, Reply: ""}
	}

	// 1) 需要澄清：直接回澄清问题（L4 接管）
	if plan.NeedClarify && plan.ClarifyQuestion != "" {
		res := RunnerResult{Plan: plan, Allowed: true}
		res.Reply = r.Guard.Sanitize(plan.ClarifyQuestion)
		return res
	}

	// 2) 非"办事"意图（闲聊/转发/协商/系统）一律透传回旧链路，由完整 chat 模型/中继处理。
	//    包括 Commander 低置信/超时/解析失败的安全降级（SafeChatPlan intent=chat）。
	if plan.Intent != IntentTask || len(plan.Subtasks) == 0 {
		log.Printf("[l4] passthrough intent=%s reason=not-task", plan.Intent)
		return RunnerResult{Plan: plan, Passthrough: true}
	}

	// 3) 办事：逐工具门控 → 门控放行后真正执行（v2.3.25）。
	res := RunnerResult{Plan: plan, Allowed: true}
	for _, st := range plan.Subtasks {
		for _, step := range st.Steps {
			dec := r.Gate.Check(st.SubAgent, step, trustLevel)
			if !dec.Allow {
				res.Allowed = false
				res.Reason = dec.Reason
				res.Reply = r.Guard.Sanitize(fmt.Sprintf("这件事我还不能直接办：%s", dec.Reason))
				r.Stats.Record("rejected")
				return res
			}
			if dec.NeedsApproval {
				res.NeedApprove = true
				res.Allowed = false
				res.Reason = dec.Reason
				res.Reply = r.Guard.Sanitize(fmt.Sprintf("需要你批准：%s", dec.Reason))
				r.Stats.Record("pending")
				return res
			}
			// 门控放行 → 真正调 Skill（无执行器时只规划，维持旧行为并明确标注未执行）。
			if r.Exec != nil {
				out, err := r.Exec.Execute(ctx, st.SubAgent, step, trustLevel)
				if err != nil {
					// 未接通的能力：诚实告知，绝不回"已安排好"。
					if errors.Is(err, ErrToolNotWired) {
						res.Allowed = false
						res.Reason = err.Error()
						res.Reply = r.Guard.Sanitize("这项能力我还没接通，暂时没法替你直接办，等对接好了再帮你。")
						r.Stats.Record("not_wired")
						return res
					}
					res.Allowed = false
					res.Reason = err.Error()
					res.Reply = r.Guard.Sanitize("刚才没办成：" + err.Error())
					r.Stats.Record("failed")
					return res
				}
				if strings.TrimSpace(out) != "" {
					res.Executed = true
					res.ExecResults = append(res.ExecResults, out)
				}
			}
		}
	}
	// 全部放行：有真实执行结果就据实回，否则回计划提示（无执行器时的回退）。
	reply := strings.Join(res.ExecResults, "\n")
	if strings.TrimSpace(reply) == "" {
		reply = plan.ReplyHint
		if reply == "" {
			reply = "已为你安排好。"
		}
	}
	res.Reply = r.Guard.Sanitize(reply)
	r.Stats.Record("approved")
	return res
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
