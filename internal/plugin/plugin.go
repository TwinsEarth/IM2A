// SPDX-License-Identifier: MIT

// Package plugin 是 IM2A 内嵌第三方插件的最小生命周期框架：
// 统一的能力声明（Capability）、上游归属（Attribution）、健康状态（Health）
// 与确定性启停顺序（Host）。纯标准库实现，不依赖任何业务包。
package plugin

import (
	"context"
	"time"
)

// Capability 描述插件对外宣告的一项集成能力。
// Kind 取值：channel | runtime | skill | audit | ui。
type Capability struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Version string   `json:"version"`
	Modes   []string `json:"modes,omitempty"`
}

// 能力类型。
const (
	KindChannel = "channel"
	KindRuntime = "runtime"
	KindSkill   = "skill"
	KindAudit   = "audit"
	KindUI      = "ui"
)

// Attribution 描述插件所含第三方的上游归属信息。
// 归属必须同时出现在启动日志、HTTP 端点、仓库文件与 README（四者缺一不算完成）。
type Attribution struct {
	Upstream     string   `json:"upstream"`
	UpstreamName string   `json:"upstreamName"`
	License      string   `json:"license"`   // SPDX id，例如 MIT
	Copyright    string   `json:"copyright"` // 上游 LICENSE 原文里的版权行
	NoticePath   string   `json:"noticePath"`
	Derived      bool     `json:"derived"`
	Usage        []string `json:"usage"`
}

// Health 是插件自报的健康状态。
// State 取值：ok | degraded | failed | disabled。
type Health struct {
	State     string    `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// 健康状态取值。
const (
	StateOK       = "ok"
	StateDegraded = "degraded"
	StateFailed   = "failed"
	StateDisabled = "disabled"
)

// Plugin 是内嵌插件必须实现的接口。
// 约定：实现方不得在被 Host 持有的锁内回调 Host，避免死锁。
type Plugin interface {
	ID() string
	Version() string
	Capabilities() []Capability
	Attribution() Attribution
	Start(context.Context) error
	Stop(context.Context) error
	Health(context.Context) Health
}

// Snapshot 是插件状态的序列化视图，供 /v1/plugins 等 HTTP 端点使用。
type Snapshot struct {
	ID           string       `json:"id"`
	Version      string       `json:"version"`
	State        string       `json:"state"`
	Detail       string       `json:"detail,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	Attribution  Attribution  `json:"attribution"`
}
