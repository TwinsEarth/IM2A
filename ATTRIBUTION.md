# 第三方组件归属声明 / Third-Party Attribution

> 本仓库以**子插件**形式集成了第三方项目 **OpenClaw**。本文说明：用了什么、怎么用、依据什么许可、
> 许可证原文在哪里、以及本项目对上游的立场（无隶属、无背书、未修改上游源码）。
>
> This repository integrates the third-party project **OpenClaw** as a **sub-plugin**.
> This file states what is used, how, under which license, where the license text lives,
> and our stance toward upstream (no affiliation, no endorsement, no upstream source modification).

---

## 1. OpenClaw

| 项 | 值 |
|---|---|
| 名称 | OpenClaw |
| 上游仓库 | <https://github.com/openclaw/openclaw> |
| 上游主页 | <https://openclaw.ai> |
| 集成版本 | **2026.10.7**（部署时 `openclaw --version` 实测：`OpenClaw 2026.10.7 (157d8c2)`） |
| 分发方式 | npm 全局安装（`openclaw@2026.10.7`），作为**未修改的发行版二进制/CLI**使用 |
| 许可证 | **MIT** |
| 版权行 | `Copyright (c) 2026 OpenClaw Foundation` |
| 许可证原文 | [`THIRD_PARTY_LICENSES/OpenClaw-MIT.txt`](THIRD_PARTY_LICENSES/OpenClaw-MIT.txt)（逐字复制，未改动） |
| 集成形态 | 独立子插件 `internal/plugin/openclaw`（package `openclawplugin`），通过**子进程 CLI 调用** + **读取其账号文件契约**通信 |
| 是否复制上游源码 | **否**。本仓库未复制、未内联、未 vendor 任何 OpenClaw 源文件；适配层为独立实现 |

### 1.1 使用的具体能力

| # | 能力 | 具体用法（契约） | 在 IM2A 中的落点 |
|---|---|---|---|
| 1 | 微信个人号通道登录 | `openclaw channels login --channel openclaw-weixin`（子进程，从 stdout 提取二维码 URL） | `internal/openclaw/launcher.go`、`internal/plugin/openclaw` |
| 2 | 微信消息投递 | `openclaw message send --channel openclaw-weixin --account <id> --target <wxid> -m <text> --json` | `internal/openclaw/sender.go` |
| 3 | 账号注册表 | 账号文件 `$HOME/.openclaw/<channel>/accounts/<accountId>.json`，字段 `{"userId":"<wxid>"}` | `internal/openclaw/watcher.go`、`health.go`、`internal/relay/accounts.go` |
| 4 | 网关状态探测 | `openclaw gateway status --json`、`openclaw --version` | `internal/plugin/openclaw`（`GatewayStatus` / `Version`） |
| 5 | Agent 调用我方工具 | OpenClaw agent 收到微信消息后调用 MCP 工具 `im2a_route`，把消息交给 IM2A 分流 | `internal/api/mcp.go`、`internal/relay` |
| 6 | 运行时归属查询 | 上述信息经 `GET /v1/about/attribution` 与 `GET /v1/plugins` 暴露 | `internal/api/attribution.go`、`internal/api/plugin.go` |

### 1.2 为什么声明为"派生（Derived）"

IM2A 未复制 OpenClaw 源代码，但**为了与上游互操作，必须沿用其命令名、参数形状、JSON 字段名、账号文件结构与 MCP 工具名**。这些标识与契约来自上游，属于派生关系。我们选择**主动标注**派生关系——这比 MIT 许可证的最低要求更严格，目的是让下游用户与审计者能一眼看清依赖边界。

代码中凡涉及上游契约处，均以注释标注：`// 契约来源：OpenClaw <cli/path>`。

### 1.3 MIT 合规自检

MIT 的核心义务是：**在软件的副本或实质性部分中，保留版权声明与许可声明**。

- [x] **保留版权与许可声明**：`THIRD_PARTY_LICENSES/OpenClaw-MIT.txt` 逐字复制上游 `LICENSE`，含 `Copyright (c) 2026 OpenClaw Foundation`。
- [x] **在分发物中可发现**：仓库文件（本文件 + `NOTICE` + `README` 章节）、HTTP 端点（`/v1/about/attribution`）、运行时日志（启动横幅）四处可见。
- [x] **标注修改情况**：本项目**未修改 OpenClaw 源码**，仅调用其官方 npm 发行版；因此无需声明对上游的修改，但已声明集成方式。
- [x] **不暗示背书**：见 §3 免责声明。
- [x] **可验证**：`scripts/attribution-check.sh` 提供机器可执行的合规自检。

### 1.4 运行时可见性（部署后可直接验证）

```bash
# 1) 归属声明（JSON / 人读）
curl -sS http://127.0.0.1:18080/v1/about/attribution
curl -sS "http://127.0.0.1:18080/v1/about/attribution?format=text"

# 2) 插件清单与健康（含 attribution 字段）
curl -sS http://127.0.0.1:18080/v1/plugins

# 3) 启动日志横幅（systemd）
journalctl -u im2a-gateway -n 30 --no-pager | grep -i openclaw
```

启动横幅形如：

```
IM2A gateway <ver> — bundled plugin: OpenClaw (<ver>) MIT, Copyright (c) 2026 OpenClaw Foundation
  attribution: /v1/about/attribution | upstream: https://github.com/openclaw/openclaw
```

---

## 2. OpenClaw-Weixin（微信通道插件）

微信个人号通道**不是 OpenClaw 内置能力**，而是外部社区插件。本项目的部署流程会安装它，且微信相关能力依赖它，
因此一并公示（这是**严格于 MIT 最低要求**的主动披露：IM2A 不直接分发该包，但部署脚本会引入）。

| 项 | 值 |
|---|---|
| 名称 | OpenClaw-Weixin（`openclaw-weixin`） |
| 包来源 | npm `openclaw-weixin@3.3.1` |
| 上游仓库 | <https://github.com/NewFuture/openclaw-weixin> |
| 说明 | Community-maintained WeChat (微信 / Weixin) channel plugin for OpenClaw using the iLink bot API |
| 许可证 | **MIT** |
| 版权行 | `Copyright (c) 2026 Tencent` 与 `Copyright (c) 2026 Contributors to NewFuture/openclaw-weixin` |
| 许可证原文 | [`THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt`](THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt)（逐字复制） |
| 分发关系 | **传递依赖**：IM2A → OpenClaw（加载插件）→ openclaw-weixin。IM2A 不 vendor、不修改该包 |
| 安装命令 | `openclaw plugins install openclaw-weixin --accept-capabilities --acknowledge-install-policy-warning --force` |

**依赖链与免责**：本项目仅通过 OpenClaw 的插件机制加载该未修改发行版；其行为、可用性与合规性由上游负责。
微信平台的使用须遵守腾讯相关协议；本项目不提供任何绕过平台规则的能力。

---

## 3. DeepSeek-Bot（DS Bot）

与前两个上游**性质不同**：OpenClaw / openclaw-weixin 是**运行时集成**（调用未修改的发行版）；
DeepSeek-Bot 是**源码级移植**——本仓库把它的部分逻辑用 Go 重写，因此构成 Apache-2.0 意义上的
**衍生作品（derivative work）**，义务更重（见 §3.4）。

| 项 | 值 |
|---|---|
| 名称 | DeepSeek Bot（DS Bot），npm 包 `ds-bot` |
| 版本 | **0.2.1**（`v0.2_preview`） |
| 上游仓库 | <https://github.com/Fei2411/DeepSeek-Bot> |
| 许可证 | **Apache License 2.0** |
| 版权行 | `Copyright 2026 Fei2411 and the DS Bot contributors`（取自上游 `NOTICE`） |
| 许可证原文 | [`THIRD_PARTY_LICENSES/DeepSeek-Bot-LICENSE.txt`](THIRD_PARTY_LICENSES/DeepSeek-Bot-LICENSE.txt)（逐字复制） |
| **NOTICE 原文** | [`THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt`](THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt)（**逐字保留**，Apache-2.0 §4(d) 义务） |
| 关系 | **移植**：不含上游原始源码；仅将下列逻辑改写为 Go |
| 未移植 | `ds-bot/src/host/**`（深度耦合 DSH 注入点）、`client.js`（1.08 MB 构建产物）、`app/`（Electron 壳）、`cordis.patch.yml`（DSH bundle patch） |

### 3.1 上游是什么（避免误读）

DS Bot 是 **DeepSeek Harness（DSH）的插件**：`ds-bot/index.js` 向 DSH 注入
`llm / tools / systemPrompt / sessionController / sessions / sessionTitle / workspaceRegistry / connection`；
每个 Bot 是 DSH 的一个 root Session，名册与群聊记录存在 `DSH_HOME` 下的一个 JSON 文件里。
因此它的 host 代码**无法在 Go 中直接复用**——这也是本仓库只移植"自包含部分"的原因。

### 3.2 移植了什么（修改声明，Apache-2.0 §4(b)）

| # | 上游来源 | 本仓库落点 | 修改内容 | 性质 |
|---|---|---|---|---|
| P1 | `ds-bot/web-search.js`（上游 `AGENTS.md` 明示该文件"独立于 Bot 团队代码"） | `internal/plugin/dsbot/search.go` | 重写为 Go：`net/http` + `encoding/json` 替代 JS 的 fetch/解析；去掉 DSH 工具注册与 `inject` 依赖；只保留 provider 抽象、请求构造、结果解析与格式化；解析逻辑用内嵌 fixture 离线测试 | **源码级移植（衍生作品）** |
| P2 | `ds-bot/src/host/text.js`、`delivery.js`、`constants.js` 与 `docs/plans/2026-10-16-long-messages.md` | `internal/plugin/dsbot/chunk.go` | 重写为纯函数 `Split(text, limit)`：按 rune/grapheme 边界切分、保护 URL 与超长词；上游"超长转附件、消息只留开头 1,000 字"的策略改为"切成多条消息"（IM2A 的微信个人号通道没有附件能力）；阈值常量逐字移植 | **源码级移植（衍生作品）** |
| P3 | `docs/vision.md` 与 `src/host/{roster,permissions}.js` 的**语义** | `internal/plugin/dsbot/roster.go` | **仅按设计与行为重写**为只读 JSON 规格（Main Bot 最高权限、可多 Main Bot、每 Bot 三种会话、角色与权限继承、群规则与上限）；不复制上游代码、不执行任何 Bot | **按设计重实现（非衍生）** |

> **法律定性说明**（值得单独说明，因为它决定了义务范围）：
> Apache-2.0 的衍生作品义务（保留声明、声明修改）针对**受版权保护的表达**。P1/P2 是对上游源文件的改写，
> 属衍生，已按 §3.4 履行义务；P3 只借鉴了**设计文档描述的接口与语义**，属于独立实现，
> 不构成衍生作品——但仍在本文中署名致谢，这是比法律要求更严格的做法。
>
> 每个移植文件头部均标注：`// 移植自 Fei2411/DeepSeek-Bot <path> (Apache-2.0)，已修改为 Go 实现：<说明>`。

### 3.3 NOTICE 的传递性（最容易漏的一点）

上游 `NOTICE` 自身又声明它**包含来自 DeepSeek Harness 的 MIT 代码**（`Copyright (c) 2026 DeepSeek`），
并逐项列出受影响文件。由于我们**逐字保留上游 NOTICE**，这份转手归属也被完整保留——
不需要（也不应该）把 DS Bot 的代码再拆开单独归属。

### 3.4 Apache-2.0 合规自检

- [x] **附许可证副本**：`DeepSeek-Bot-LICENSE.txt`（Apache-2.0 全文，逐字）。
- [x] **保留 NOTICE**（§4(d)）：`DeepSeek-Bot-NOTICE.txt` 逐字保留；将来若本仓库附带 NOTICE 分发，必须包含其内容。
- [x] **声明修改**（§4(b)）：见 §3.2 表格 + 每个移植文件的头部注释。
- [x] **不暗示背书**（§6）：见 §5 免责与立场。
- [x] **专利条款**（§3）：Apache-2.0 授予明示专利许可；若使用者就本项目或上游提起专利诉讼，该许可依 §3 终止。本仓库不额外授予任何专利许可。
- [x] **商标**（§6）：不使用 "DeepSeek"、"DS Bot" 作为本项目名称，也不暗示关联。

### 3.5 运行时可见性

```bash
curl -sS http://127.0.0.1:18080/v1/about/attribution | grep -A2 DeepSeek
curl -sS http://127.0.0.1:18080/v1/plugins | grep dsbot
```

---

## 4. 本仓库（IM2A）自身许可

- 项目自述为 **MIT**（见 `README.md` 徽章与 `npm/package.json` 的 `"license": "MIT"`）。
- 本仓库根目录已补充 `LICENSE` 文件以落实该声明（此前仅有徽章声明、缺少许可证文件）。
- 版权人字符串采用 `IM2A contributors`；**该字符串应由项目权利人最终确认**。

---

## 5. 免责与立场

- 本项目与 **OpenClaw Foundation** 无隶属、无赞助、无官方背书关系。
- "OpenClaw" 为其权利人所有的名称/标识，本项目仅在**指明来源**的必要范围内使用，不用于暗示背书。
- OpenClaw 的许可证为 MIT，其分发不附带任何担保（见许可证原文末段）。
- 本项目的 OpenClaw 集成能力**默认关闭/降级**：上游缺失时插件进入 `degraded`，不影响 IM2A 其余功能。

---

## 6. English summary

This repository integrates **OpenClaw** (<https://github.com/openclaw/openclaw>, version **2026.10.7**,
MIT licensed, `Copyright (c) 2026 OpenClaw Foundation`) as an isolated **sub-plugin**
(`internal/plugin/openclaw`, package `openclawplugin`), and its deployment installs the external
**`openclaw-weixin@3.3.1`** channel plugin (<https://github.com/NewFuture/openclaw-weixin>, MIT,
`Copyright (c) 2026 Tencent` and `Copyright (c) 2026 Contributors to NewFuture/openclaw-weixin`)
as a **transitive dependency** loaded by OpenClaw.

It also **ports a small part of DeepSeek-Bot** (<https://github.com/Fei2411/DeepSeek-Bot>, version **0.2.1**,
**Apache-2.0**, `Copyright 2026 Fei2411 and the DS Bot contributors`) into Go
(`internal/plugin/dsbot`): the standalone web-search provider, long-message splitting, and a read-only
bot-roster spec. **No upstream source files are vendored**; the port is an independent Go rewrite and the
modifications are declared in §3.2 and in each ported file's header. The upstream **NOTICE is preserved
verbatim** in [`THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt`](THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt)
(Apache-2.0 §4(d)), which in turn carries its own MIT attributions to DeepSeek Harness.

- **No OpenClaw source code is copied or vendored.** The adapter is an independent implementation that
  invokes the unmodified upstream CLI as a child process and reads its documented account-file format.
- **Command names, CLI arguments, JSON field names, account-file schema and MCP tool name are derived from
  upstream contracts** for interoperability; this derivation is declared explicitly (stricter than MIT requires).
- The upstream **MIT license text is reproduced verbatim** in
  [`THIRD_PARTY_LICENSES/OpenClaw-MIT.txt`](THIRD_PARTY_LICENSES/OpenClaw-MIT.txt).
- Attribution is surfaced in four places: this file, `NOTICE`, `README.md`, and at runtime via
  `GET /v1/about/attribution`, `GET /v1/plugins` and the gateway startup banner.
- There is **no affiliation with or endorsement by** the OpenClaw Foundation.
- Verify with: `bash scripts/attribution-check.sh`.
