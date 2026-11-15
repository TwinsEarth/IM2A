# @twinsearth/dsh-im2a-plugin

**智能体宇宙—IM2A** — 一个面向 Agent 开发&开放的即时连接网络。

一个包，一个 manifest，一套生命周期。内核负责生命周期、工具总线、宪法门控与
编排；九个能力模块挂在总线上；五个商用模块由 Relay 许可后加载，本版全部为
`planned`。

## 3.3.1 交付了什么

- 兼容 DSH `0.2.2-rc.3`：`engines.dsh` 自 `0.2.2-rc.2` 精确升级，未放宽为范围；
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
IM2A HTTP MCP  http://127.0.0.1:18082/mcp   (im2a-http-mcp, 30 tools)
```

行配置 `failOnStartupError: false`：桥未就绪时该行不可用，但不阻塞 Harness 启动。

## 验证

```text
node --test tests/v321-plugin-shell.test.mjs     8/8
node packages/dsh-im2a-plugin/migrations/generate-aliases.mjs   30 entries
```

display metadata：`locale/zh.json` / `locale/en.json` 的 `meta.title` 与
`meta.description`，图标 `icon.svg`，均通过 `package.json` 的 `exports` 导出。

## 不承诺

- 本版没有 Relay 许可握手，`license_implemented: false` 是事实声明；
- `identity` / `economy` / `governance` 为 `planned`，不会返回假结果；
- 回环明文仅用于本机；跨机部署必须 TLS。
