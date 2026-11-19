# @twinsearth/dsh-im2a-plugin

[![CI](https://github.com/TwinsEarth/dsh-im2a-plugin/actions/workflows/ci.yml/badge.svg)](https://github.com/TwinsEarth/dsh-im2a-plugin/actions/workflows/ci.yml)
[![npm](https://img.shields.io/npm/v/@twinsearth/dsh-im2a-plugin)](https://www.npmjs.com/package/@twinsearth/dsh-im2a-plugin)
[![license](https://img.shields.io/github/license/TwinsEarth/dsh-im2a-plugin)](LICENSE)

**智能体宇宙—IM2A** — 一个面向 Agent 开发&开放的即时连接网络。

一个包，一个 manifest，一套生命周期。内核负责生命周期、工具总线、宪法门控与
编排；九个能力模块挂在总线上；五个商用模块由 Relay 许可后加载，本版全部为
`planned`。

## 3.4.0 交付了什么

- **修复**：`kernel/topology-metrics.mjs` 曾 import `../../im2a-http-mcp/skill-claim.mjs`，
  该路径只存在于仓库、不在发布包内，导致 3.3.1 装好后该模块 `ERR_MODULE_NOT_FOUND`。
  判定逻辑（补丁证据 + 独立验证者）已内联为本地函数，模块现可从 tarball 加载；
- `scripts/check-dist.mjs`：把 tarball 解到临时目录、按「消费者视角」逐个 import 内核模块，
  CI 每次推送都跑；
- **工具面归位**：`plugin.json.runtime.tools` 声明桥真正服务的 32 个工具；
  `registry.publicTools()` 返回的才是可调用面，`declaredTools()` 保留 39 个设计名，
  `toolGap()` 显式报告差异（交集仅 `im2a_kernel_status`）；
- 兼容 DSH `0.2.2-rc.5`：`engines.dsh` 自 `0.2.2-rc.3` 精确升级，未放宽为范围；
- `plugin.json`：单包 manifest，内核 + 9 模块 + 5 个商用占位；
- `kernel/` 六个运行时模块：`model-registry`、`meter`、`ui-artifact`、`did-portability`、
  `topology-metrics`、`scope-governance`，已在 manifest 注册并挂到对应能力模块；
- MCP 工具面 32：新增 `governance_manage`（只提议/查状态，裁决不进 MCP）与
  `im2a_kernel_status`（只读内核状态）；
- `migrations/3.2.1-tool-aliases.json`：31 条旧名 → 新名，带真实 schema hash；
- `migrations/generate-aliases.mjs`：从运行中的 bridge 重新生成别名表。

尚未交付（3.3.x 后续）：9 个内核元工具中除 `im2a_kernel_status` 外的其它入口、
模块 handler 接线、Relay 许可握手。

## 安装

```text
plugin_manager install_bundle(@twinsearth/dsh-im2a-plugin@3.3.1)
```

单一 bundle 只插入一条 `im2a-mcp` 行；不要再叠加第二个 bundle 覆盖该行——
分层覆盖正是此前启动顺序问题的来源。

## 运行前提

```text
IM2A HTTP MCP  http://127.0.0.1:18082/mcp   (im2a-http-mcp, 32 tools)
```

行配置 `failOnStartupError: false`：桥未就绪时该行不可用，但不阻塞 Harness 启动。

## 验证

CI 在每次推送 `main` 时运行（见 `.github/workflows/ci.yml`），本地等价命令：

```text
node scripts/check-package.mjs
```

它校验：两份清单的版本与 `engines.dsh` 一致、`LICENSE` 为 MIT、`kernel.runtime_modules`
声明的文件全部存在、别名表的工具名与 schema hash 合法、随包文件齐全。随后用
`npm pack --dry-run` 核对 tarball 内容。

display metadata：`locale/zh.json` / `locale/en.json` 的 `meta.title` 与
`meta.description`，图标 `icon.svg`，均通过 `package.json` 的 `exports` 导出。

## 不承诺

- 本版没有 Relay 许可握手，`license_implemented: false` 是事实声明；
- `identity` / `economy` / `governance` / `observability` 为 `partial`：内核模块已实现，
  但验证档位、经济工具面、裁决入口与遥测仍未交付，不会返回假结果；
- 回环明文仅用于本机；跨机部署必须 TLS。
