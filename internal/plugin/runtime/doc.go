// SPDX-License-Identifier: MIT

// Package pluginruntime 实现 Wave D1 的插件运行时基础（不接线版本）：
// manifest 解析与校验、JSON canonical 化与 manifest_hash、Ed25519 签名校验、
// 目录 loader（版本扫描/签名/平台校验）、依赖图拓扑排序与启动顺序。
//
// 重要边界（写死，不要误读）：
//   - 本包当前**没有被任何生产代码 import**：PLUGIN_RUNTIME_V2 默认 0，D1 前半只交付纯库，
//     因此现有 /v1/plugins JSON、golden 基线、-selftest 行为逐字节不变（由 runtime 包的
//     TestRuntimePackageIsNotWired 守护）。
//   - K0 不提供安全边界：SecurityBoundary(K0)==false 是硬裁决，只有 K1/K2 才可能为 true；
//     这条不随 manifest 内容改变，也不允许用文档/输出措辞暗示 K0 有隔离。
//   - 本包不启动任何进程、不加载任何 Go 代码、不写任何文件；loader 只读。
package pluginruntime
