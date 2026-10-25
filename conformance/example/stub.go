// Package example 演示第三方如何接入关系门控一致性套件（10 行）。
//
// 你只需要实现 conformance.Implementer 的 `Decide(Input) Outcome`，
// 然后把它交给 conformance.Run 或自己的 main 即可；协议见 spec/relation-gating.md。
package example

import (
	"strings"

	"human-agent-im/conformance"
)

// Stub 是第三方实现的占位示例：**故意只实现一个最小子集**（L0 全放行，其余一律 forbidden），
// 用来演示接口形状；它不会通过公开向量（这是预期的——本示例只保证"能编译、能接入"）。
//
// 换成一个真实实现时，把 Decide 内部替换为你自己的门控逻辑即可（矩阵/顺序见规范 §3/§4）。
type Stub struct{}

var _ conformance.Implementer = Stub{}

// Decide 示例实现（10 行以内）。
func (Stub) Decide(in conformance.Input) conformance.Outcome {
	if strings.TrimSpace(in.Call.Scope) == "" { // 非法输入直接拒绝
		return conformance.Outcome{ReasonCode: conformance.ReasonScopeForbidden}
	}
	if in.Call.TrustLevel == 0 { // 只演示 L0 全放行
		return conformance.Outcome{Allowed: true, ReasonCode: conformance.ReasonOK}
	}
	return conformance.Outcome{ReasonCode: conformance.ReasonScopeForbidden}
}
