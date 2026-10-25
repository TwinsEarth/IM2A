// SPDX-License-Identifier: MIT
//
// topic → userID 映射（B2B-DESIGN §4.1/§5）：内核事件仍以 topic 形式广播
// （internal/stream.Hub.Broadcast(topic, any)），而 UDS 投递只认 userID；
// 这层映射属于内核，网关侧没有 topic 语义。
//
// 已覆盖的 topic 形状（Wave A/B 实测调用点）：
//   - approval:<userID>   （internal/api/approval*.go）
//   - trust:<userID>      （internal/api/relation.go）
//   - user:<userID>       （客户端约定，兼容保留）
//   - subagent:<name>     —— 无 userID 语义，需由上层解析器决定（见 Resolver）
//   - negotiation:<sid>   —— 同上
package connapi

import (
	"strconv"
	"strings"
)

// TopicPrefix 是内核事件 topic 的前缀。
type TopicPrefix string

const (
	TopicApproval    TopicPrefix = "approval:"
	TopicTrust       TopicPrefix = "trust:"
	TopicUser        TopicPrefix = "user:"
	TopicSubagent    TopicPrefix = "subagent:"
	TopicNegotiation TopicPrefix = "negotiation:"
)

// Resolver 可选的 topic 解析器：当默认规则解析不出 userID（如 subagent/negotiation），
// 由装配方注入基于 store 的解析（session → 参与用户）。返回 false 表示无法投递到用户。
type Resolver interface {
	ResolveUserID(topic string) (int64, bool)
}

// TopicUserID 按固定前缀解析 topic 里的 userID；无法解析返回 (0,false)。
// 只做纯函数解析，不访问 DB；subagent/negotiation 返回 false，交给 Resolver。
func TopicUserID(topic string) (int64, bool) {
	t := strings.TrimSpace(topic)
	for _, p := range []TopicPrefix{TopicApproval, TopicTrust, TopicUser} {
		if strings.HasPrefix(t, string(p)) {
			id, err := strconv.ParseInt(t[len(p):], 10, 64)
			if err != nil || id <= 0 {
				return 0, false
			}
			return id, true
		}
	}
	return 0, false
}

// ResolveTopicUserID 先走固定前缀，再走可选 Resolver；都失败返回 (0,false)。
// nil Resolver 安全。
func ResolveTopicUserID(topic string, r Resolver) (int64, bool) {
	if id, ok := TopicUserID(topic); ok {
		return id, true
	}
	if r != nil {
		return r.ResolveUserID(topic)
	}
	return 0, false
}
