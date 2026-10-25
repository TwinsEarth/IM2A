// Package conformance 是「关系档位门控协议 v1」的**一致性测试套件**（实现无关）。
//
// 用途：任何实现（本仓库的 trustzone、或第三方的门控实现）只要满足 `Implementer` 接口，
// 就能用 `spec/vectors/relation-gating.v1.json` 的公开向量验证是否符合协议。
//
// 协议见 `spec/relation-gating.md`。本包**零外部依赖**（只用标准库）。
//
// 设计要点：
//   - 向量是"输入 → 期望输出（allowed / requiresApproval / reasonCode）"；
//   - 任何一条不一致：`Run` 记录 failure，命令行工具据此 exit 1；
//   - 不比较人读消息文本（规范明确文本不属于协议），只比较三态 + reasonCode。
package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// 协议版本与向量文件版本。
const (
	ProtocolVersion = "relation-gating/v1"
	VectorsVersion  = 1
)

// ReasonCode 是协议定义的稳定拒绝/放行标识（跨实现比对用）。
type ReasonCode string

const (
	ReasonOK                     ReasonCode = "OK"
	ReasonSkillNotAllowed        ReasonCode = "SKILL_NOT_ALLOWED"
	ReasonTrustBelowMin          ReasonCode = "TRUST_BELOW_MIN"
	ReasonScopeForbidden         ReasonCode = "SCOPE_FORBIDDEN"
	ReasonScopeRequiresApproval  ReasonCode = "SCOPE_REQUIRES_APPROVAL"
	ReasonAmountAboveThreshold   ReasonCode = "AMOUNT_ABOVE_THRESHOLD"
	ReasonAmountAboveBudget      ReasonCode = "AMOUNT_ABOVE_BUDGET"
	// reasonUnknownImpl 只在 reference adapter 收到未知人读文本时出现（用于显式暴露实现漂移）。
	reasonUnknownImpl ReasonCode = "UNKNOWN_IMPLEMENTATION_REASON"
)

// AllReasonCodes 协议 v1 的全部 reasonCode（供校验/文档生成）。
var AllReasonCodes = []ReasonCode{
	ReasonOK, ReasonSkillNotAllowed, ReasonTrustBelowMin, ReasonScopeForbidden,
	ReasonScopeRequiresApproval, ReasonAmountAboveThreshold, ReasonAmountAboveBudget,
}

func (r ReasonCode) Valid() bool {
	for _, c := range AllReasonCodes {
		if r == c {
			return true
		}
	}
	return false
}

// Capability 是 Agent 的能力声明（输入的一半）。
type Capability struct {
	AgentID                    string   `json:"agentId,omitempty"`
	TrustLevelMin              int      `json:"trustLevelMin"`
	AllowedSkills              []string `json:"allowedSkills,omitempty"`
	BudgetCentsPerDay          int64    `json:"budgetCentsPerDay"`
	RequiresApprovalAboveCents int64    `json:"requiresApprovalAboveCents"`
}

// Call 是一次工具调用的上下文（输入的另一半）。
type Call struct {
	SkillID     string `json:"skillId"`
	Scope       string `json:"scope"`
	TrustLevel  int    `json:"trustLevel"`
	AmountCents int64  `json:"amountCents"`
	TargetUID   uint64 `json:"targetUid,omitempty"`
}

// Input 向量输入。
type Input struct {
	Capability Capability `json:"capability"`
	Call       Call       `json:"call"`
}

// Outcome 是协议规定的输出（三态 + reasonCode）。
type Outcome struct {
	Allowed          bool       `json:"allowed"`
	RequiresApproval bool       `json:"requiresApproval"`
	ReasonCode       ReasonCode `json:"reasonCode"`
}

// String 便于失败输出。
func (o Outcome) String() string {
	return fmt.Sprintf("{allowed=%v requiresApproval=%v reasonCode=%s}", o.Allowed, o.RequiresApproval, o.ReasonCode)
}

// Implementer 是被测/第三方实现的唯一接口：纯函数，输入→输出。
type Implementer interface {
	Decide(Input) Outcome
}

// ImplementerFunc 让函数直接满足接口（第三方最省事的接入方式）。
type ImplementerFunc func(Input) Outcome

func (f ImplementerFunc) Decide(in Input) Outcome { return f(in) }

// Vector 一条公开测试向量。
type Vector struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Input       Input   `json:"input"`
	Expect      Outcome `json:"expect"`
}

// VectorFile 向量文件（版本化，`relation-gating.v1.json`）。
type VectorFile struct {
	Version int      `json:"version"`
	Spec    string   `json:"spec"`
	Vectors []Vector `json:"vectors"`
}

// LoadVectors 读取并校验向量文件（版本、唯一 ID、reasonCode 合法性、三态一致性）。
func LoadVectors(path string) (*VectorFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read vectors %s: %w", path, err)
	}
	var vf VectorFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&vf); err != nil {
		return nil, fmt.Errorf("parse vectors %s: %w", path, err)
	}
	if vf.Version != VectorsVersion {
		return nil, fmt.Errorf("vectors version=%d, want %d", vf.Version, VectorsVersion)
	}
	if len(vf.Vectors) == 0 {
		return nil, fmt.Errorf("vectors 为空")
	}
	seen := map[string]bool{}
	for i, v := range vf.Vectors {
		if v.ID == "" {
			return nil, fmt.Errorf("vector[%d] 缺 id", i)
		}
		if seen[v.ID] {
			return nil, fmt.Errorf("vector id 重复: %s", v.ID)
		}
		seen[v.ID] = true
		if !v.Expect.ReasonCode.Valid() {
			return nil, fmt.Errorf("vector %s: 非法 reasonCode %q", v.ID, v.Expect.ReasonCode)
		}
		if err := checkTriState(v.Expect); err != nil {
			return nil, fmt.Errorf("vector %s: %w", v.ID, err)
		}
	}
	return &vf, nil
}

// checkTriState 校验 §2.1 的三态约束（向量自身必须自洽）。
func checkTriState(o Outcome) error {
	switch o.ReasonCode {
	case ReasonOK:
		if !o.Allowed || o.RequiresApproval {
			return fmt.Errorf("OK 必须 allowed=true, requiresApproval=false（实际 %v）", o)
		}
	case ReasonScopeRequiresApproval, ReasonAmountAboveThreshold:
		if o.Allowed || !o.RequiresApproval {
			return fmt.Errorf("%s 必须 allowed=false, requiresApproval=true（实际 %v）", o.ReasonCode, o)
		}
	default:
		if o.Allowed || o.RequiresApproval {
			return fmt.Errorf("%s 必须 allowed=false, requiresApproval=false（实际 %v）", o.ReasonCode, o)
		}
	}
	return nil
}

// Failure 一条不一致。
type Failure struct {
	ID       string  `json:"id"`
	Category string  `json:"category"`
	Want     Outcome `json:"want"`
	Got      Outcome `json:"got"`
}

// Report 运行结果。
type Report struct {
	Spec     string    `json:"spec"`
	Total    int       `json:"total"`
	Passed   int       `json:"passed"`
	Failed   int       `json:"failed"`
	Failures []Failure `json:"failures"`
	ByCategory map[string]int `json:"byCategory"`
}

// Run 对全部向量执行实现并汇总；任何不一致都会出现在 Failures 中。
func Run(impl Implementer, vf *VectorFile) Report {
	rep := Report{Spec: vf.Spec, Total: len(vf.Vectors), ByCategory: map[string]int{}}
	for _, v := range vf.Vectors {
		rep.ByCategory[v.Category]++
		got := impl.Decide(v.Input)
		if got == v.Expect {
			rep.Passed++
			continue
		}
		rep.Failures = append(rep.Failures, Failure{ID: v.ID, Category: v.Category, Want: v.Expect, Got: got})
	}
	rep.Failed = len(rep.Failures)
	sort.Slice(rep.Failures, func(i, j int) bool { return rep.Failures[i].ID < rep.Failures[j].ID })
	return rep
}

// JSON 机器可读输出。
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
