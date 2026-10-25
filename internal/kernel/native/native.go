// SPDX-License-Identifier: MIT
//
// Package native 是 **原生通道专用** 的内核域（Wave C）：
// 身份映射、presence 租约/聚合、会话/消息（C2/C3 后续）。
//
// 边界（D8=B 混合通道）：
//   - 人类 IM 消息/已读/presence/离线箱由腾讯云 IM PaaS 负责，本包**不承载**；
//   - 本包只服务 Agent 消息、事件推送、原生客户端；
//   - presence 真相在**内核内存**状态机（TTL 45s / 15s 刷新），
//     PG 的 n_presence 只是 30s 快照（观测/重启后最后已知状态），不参与读路径判定；
//   - offline 只能由内核按 TTL 推断，**拒绝**网关/客户端直接上报 offline（防伪造）。
package native
