// SPDX-License-Identifier: MIT
//
// 插件 HTTP 端点（契约来源：PLUGIN-CONTRACT.md §4）：通过 Deps 注入的插件宿主暴露只读的
// GET /v1/plugins 与 GET /v1/plugins/{id}，并提供出站发送器的 nil-safe 统一取用点（契约 §6）。
package api

import (
	"net/http"

	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
)

// PluginList 处理 GET /v1/plugins：返回全部已注册插件的只读快照。
// 匿名可读（归属公示 / 用户自查），只暴露契约 §1 的 Snapshot 字段。
func (d *Deps) PluginList(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Plugins == nil {
		writeErr(w, http.StatusServiceUnavailable, "plugin host not initialized")
		return
	}
	snaps := d.Plugins.Snapshots()
	if snaps == nil {
		snaps = []plugin.Snapshot{}
	}
	for i := range snaps {
		snaps[i] = normalizePluginSnapshot(snaps[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": snaps})
}

// PluginGet 处理 GET /v1/plugins/{id}：未注册插件返回 404，不泄漏内部错误细节。
func (d *Deps) PluginGet(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Plugins == nil {
		writeErr(w, http.StatusServiceUnavailable, "plugin host not initialized")
		return
	}
	id := r.PathValue("id")
	p, ok := d.Plugins.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	for _, s := range d.Plugins.Snapshots() {
		if s.ID == id {
			writeJSON(w, http.StatusOK, normalizePluginSnapshot(s))
			return
		}
	}
	// 宿主尚未生成快照（例如注册后未启动）：从插件门面读取真实状态，不伪造。
	h := p.Health(r.Context())
	writeJSON(w, http.StatusOK, normalizePluginSnapshot(plugin.Snapshot{
		ID:           p.ID(),
		Version:      p.Version(),
		State:        h.State,
		Detail:       h.Detail,
		Capabilities: p.Capabilities(),
		Attribution:  p.Attribution(),
	}))
}

// normalizePluginSnapshot 保证 JSON 数组字段是 [] 而不是 null（契约 §1 结构稳定）。
func normalizePluginSnapshot(s plugin.Snapshot) plugin.Snapshot {
	if s.Capabilities == nil {
		s.Capabilities = []plugin.Capability{}
	}
	return s
}

// resolveOpenClawSender 取 OpenClaw 插件单例发送器（契约 §6：全仓出站统一）。
// provider 为 nil 时回退进程内默认构造，保证未接线场景的行为与旧代码完全一致；
// 两种路径下失败都返回 openclaw 原有错误类型（ErrSendFailed / ErrTokenExpired / ...）。
func resolveOpenClawSender(provider func() *openclaw.Sender) *openclaw.Sender {
	if provider != nil {
		if s := provider(); s != nil {
			return s
		}
	}
	return openclaw.NewSender()
}
