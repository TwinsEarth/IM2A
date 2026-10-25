// SPDX-License-Identifier: MIT
//
// Package golden 是网关的黑盒行为快照 harness：以子进程启动真实 gateway，
// 遍历固定路由表，把每条路由的 status / content-type / JSON 键路径与类型
// 归一化成可 diff 的观察值，存到 testdata/golden.json，用作重构前后的行为守门。
//
// 归一化原则（任务要求）：只记稳定信息，不记时间/ID/地址/随机值。
// 因此它同时也能证明「路由注册表化」重构没有改变对外可观察行为。
package golden
