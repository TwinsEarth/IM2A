# P0 Report — v2.3.45 Platform Scaffold (A2A correction)

日期：2026-10-29  
状态：**P0-A2 骨架与 A2A profile 修正完成；未 tag、未 Release、未 npm publish**

## 已完成

| 项 | 路径 | 结果 |
|---|---|---|
| 版本矩阵 | `version-matrix.json` | 平台 2.3.45 / Go 2.3.26 |
| monorepo | `package.json`、`pnpm-workspace.yaml`、`apps/`、`packages/` | 骨架 |
| release manifest schema | `release-manifest.schema.json` | 9 gates，非 draft 有证据约束 |
| release manifest draft | `release-manifest.json` | status=draft |
| A2A 规范钉版 | `packages/protocol/versions.json`、`vendor/a2a/SOURCE.json` | A2A 1.0 / v1.0.1，pinned commit + proto blob |
| A2A 适配 profile | `packages/protocol/schemas/a2a-profile.schema.json` | 按 v1.0.1 AgentCard 形状修正 |
| A2A fixtures | `packages/protocol/fixtures/` | 正例 + 负例 |
| MCP profile | `packages/protocol/schemas/mcp-profile.schema.json` | 2026-07-28 / stdio / HTTPS streamable-http |
| Agent Plugins profile | `packages/protocol/schemas/agent-plugin.schema.json` | plugin/skills/mcp/targets |
| session-ref | `packages/protocol/schemas/session-ref.schema.json` | `dsh-session:` 不透明引用 |
| adapter 契约 | `packages/harness-adapters/src/contract.ts` | detect/plan/apply/verify/rollback |
| 12 适配器清单 | `packages/harness-adapters/adapters.json` | 12 个，T0/T1/T2 |
| 生成器骨架 | `packages/agent-plugins/generator` | 12 targets，源哈希 |
| Skills | `skills/` + `packages/agent-plugins/source/skills/` | 4 个 SKILL.md |
| 验证 | `scripts/schema-validator.mjs`、`scripts/validate.mjs`、`tests/p0.test.mjs` | PASS + 负控 |

## P0-1：DSH 宿主插件 + insert patch（2026-10-30）

已新增：

```text
packages/im2a-dsh-plugin/
  package.json
  cordis.patch.yml
  locale/en.json
  locale/zh.json
  icon.svg
  README.md
```

钉版：

```text
@twinsearth/im2a-dsh-plugin 0.1.0
engines.dsh = 0.2.1-rc.3
```

关键修正：

- `cordis.patch.yml` 使用 `- insert:`，不再写裸 `- id:`
- sandbox adapter 的 `entryBlock()` 同步改为 `- insert:`
- 新增校验：host plugin 名称、固定 engines、patch insert、serverName、mcp-client
- 5 个 DSH 沙箱测试仍通过
- `validate.mjs` 返回 `dshHostPlugin=@twinsearth/im2a-dsh-plugin@0.1.0`

仍存在：

```text
adapter 仍直接写 sandbox patch，尚未改为安装 host bundle
adapter verify 的 JSON-RPC/isError 判定尚未修正
host plugin 尚未 npm pack/安装到沙箱（本机无 npm CLI）
live profile 未写入
```

---

## P0-2：host bundle 安装 + 真实 MCP 调用（2026-10-30）

沙箱 DSH_HOME：

```text
E:\GPT\im2a-v2.3.45\.tmp\dsh-home-p02b
profile: im2a-p02
```

实际安装：

```text
dsh plugin --profile im2a-p02 add file:.../packages/im2a-dsh-plugin
→ pnpm added @twinsearth/im2a-dsh-plugin
→ profile bundles 包含该插件
→ dsh --profile im2a-p02 --dump-config
   只出现一条 id: im2a-mcp / serverName: im2a
```

随后执行真实 MCP：

```text
initialize      -> serverInfo im2a-harness-bridge 0.1.0
tools/list      -> im2a_harness_list + 5 个工具
tools/call      -> im2a_harness_list
→ 返回 33 个 harness / selected 20
```

注意：这条真实调用是用 PowerShell 直接驱动 `im2a-mcp.exe` 完成的，**不是 adapter.verify() 的通过证据**。adapter 的旧字符串判定仍有假阳性，P0-3 必须修。

安装/卸载：

```text
adapter.installHost()   -> PASS（沙箱）
adapter.uninstallHost() -> PASS（im2a-mcp 行消失）
live profile            -> 0 修改
```

仍未完成：

```text
adapter.verify() 分阶段 JSON-RPC + isError 判定
本地 18080 隧道不可用；本次真实调用临时使用公网 18080
host plugin 尚未通过 npm pack 的正式 tarball 安装
没有把 adapter.apply() 改为 installHost() 路径
```

---

## P0-3：verify 分阶段 + host apply（2026-10-30）

已完成：

```text
packages/harness-adapters/dsh/verify.mjs
  initialize(id=1)
  notifications/initialized
  tools/list(id=2)
  tools/call(id=3)
  按 id 关联响应
  检查 error / isError
  401/404/5xx -> failed
  dial/refused -> blocked
```

测试：

```text
verify.test.mjs: 4/4 pass
- classify auth/route/gateway/network
- tools/call isError with tools/list containing tool -> FAIL
- gateway unreachable -> BLOCKED
- only real harness list -> PASS
```

host apply：

```text
adapter.planHost() -> mode=host
adapter.apply({mode:host}) -> 安装 @twinsearth/im2a-dsh-plugin
dsh --dump-config -> 一条 im2a-mcp / serverName im2a
```

正式 tgz：

```text
.tmp/artifacts/twinsearth-im2a-dsh-plugin-0.1.0.tgz
2,011 bytes
包含 package/package.json、cordis.patch.yml、locale、icon、README
dsh plugin --profile im2a-p03 add file:...tgz -> PASS
```

真实 MCP：

```text
initialize -> serverInfo
tools/list -> im2a_harness_list
tools/call -> 33 harnesses
```

边界：

```text
真实调用由 PowerShell 直接驱动 im2a-mcp.exe，
使用临时公网 18080；不是 adapter.verify() PASS。

adapter.verify() 在本沙箱返回：
  status=blocked, code=SPAWN_BLOCKED (spawn EPERM)
原因：DSH 沙箱禁止 Node 子进程管道；需在 CI/宿主环境复跑。
```

仍未完成：

```text
本地 18080 SSH 隧道未恢复
adapter.verify 的 CI/宿主环境证据未取
live profile 未写入
正式 npm publish / tag / Release 未执行
```

---

## P0-4：本地隧道 + adapter.verify（2026-10-30）

已恢复：

```text
127.0.0.1:18080 -> Vultr gateway
/v1/plugins -> 200
```

adapter.verify 真实结果：

```json
{
  "ok": true,
  "status": "passed",
  "code": null,
  "harnessCount": 33,
  "transport": "file-fd",
  "checks": [
    {"name": "profile-mcp-installed", "ok": true, "detail": "bundle=@twinsearth/im2a-dsh-plugin"},
    {"name": "initialize", "ok": true, "detail": "im2a-harness-bridge"},
    {"name": "tools/list", "ok": true, "detail": "im2a_harness_list"},
    {"name": "tools/call", "ok": true, "detail": "harnesses=33"}
  ]
}
```

说明：

```text
本机 DSH 沙箱仍禁止 Node 管道 spawn（EPERM）
adapter 自动回退到文件描述符 stdio（不是伪造结果）
请求仍全部走真实 im2a-mcp.exe + 本地 18080 + Bearer token
```

测试：

```text
verify.test.mjs: 4/4 pass
dsh-adapter.test.mjs: 5/5 pass
adapter.verify 真实本地：PASS (file-fd)
```

仍未完成：

```text
交互式 pipe 验证（非沙箱 CI）未跑
DSH 侧 Tool.listTools 中出现 mcp__im2a__im2a_harness_list 未验证
live profile 未安装
npm pack / tag / Release 未执行
```

---

## P0-5：DSH 会话内工具可见性（2026-10-30）

在临时 DSH_HOME 启动沙箱 Web：

```text
profile: im2a-p03
port: 18181
```

结果：

```text
dsh web: started
1 entry did not activate
im2a-mcp (@deepseek-ai/dsh-mcp-client):
  initial connection or tool synchronization failed
  Error: spawn EPERM
```

因此：

```text
--dump-config 能看到 im2a-mcp 行
DSH 运行时无法 spawn im2a-mcp.exe
mcp__im2a__im2a_harness_list 未注册
DSH 侧工具可见性：BLOCKED
```

原因不是配置或 token，而是本沙箱禁止子进程管道：

```text
@modelcontextprotocol/client StdioClientTransport
→ cross-spawn
→ spawn EPERM
```

与 adapter 的 `file-fd` 回退无关；DSH 的 MCP client 不支持该传输。

沙箱 DSH 已停止；live profile 未修改。

结论：

```text
P0-5 DSH-mediated tool call：BLOCKED（环境限制）
需要非沙箱宿主/CI 或允许 child_process 的 DSH 运行时
不得在当前环境标记 verified
```

---

## P0-6：Streamable HTTP MCP（2026-10-30）

新增：

```text
packages/im2a-http-mcp/server.mjs
packages/im2a-dsh-plugin/cordis.http.patch.yml
```

HTTP MCP：

```text
POST /mcp
GET  /mcp   (SSE keep-alive)
GET  /healthz
tools: im2a_harness_list, im2a_presence
```

直接调用：

```text
initialize -> im2a-http-mcp 0.1.0
tools/list -> 2 tools
tools/call im2a_harness_list -> 33 harnesses
```

DSH 侧证据：

```text
profile im2a-p03 + --patch cordis.http.patch.yml
dsh web: running

HTTP MCP server 收到 DSH 请求：
  server/discover
  initialize
  notifications/initialized
  tools/list

无 spawn EPERM
```

说明：

```text
DSH 已通过 HTTP 完成 MCP handshake 与 tools/list。
这证明工具在 MCP 层可见，且不再依赖 stdio 子进程。

仍未完成：
  DSH 侧真实 tools/call（需要 DSH 模型/工具触发）
  server/discover 目前返回 -32601；DSH 仍继续 initialize，但不能据此宣称完整
  2026-07-28 conformance
  远程部署仍需 HTTPS + 鉴权
```

沙箱 DSH 与 HTTP server 已停止；live profile 未修改。

---

## P0-7：MCP 2026-07-28 与 DSH tools/list（2026-10-30）

`server/discover` 已按官方 2026-07-28 形状实现：

```json
{
  "resultType": "complete",
  "supportedVersions": ["2026-07-28"],
  "capabilities": { "tools": {} },
  "_meta": { "io.modelcontextprotocol/serverInfo": { "name": "im2a-http-mcp", "version": "0.1.0" } },
  "ttlMs": 3600000,
  "cacheScope": "public"
}
```

DSH 实际请求：

```text
server/discover (id=server-discover-probe-1)
tools/list (id=0)
```

第一次失败：

```text
Invalid result for tools/list: missing required resultType
```

修正后第二次失败：

```text
tools/list missing ttlMs / cacheScope
```

补齐 `resultType: complete`、`ttlMs`、`cacheScope` 后：

```text
DSH 启动无 "entry did not activate"
HTTP MCP server 收到 discover + tools/list
```

因此：

```text
DSH -> HTTP MCP handshake：PASS
DSH -> tools/list：PASS
DSH 侧真实 tools/call：UNVERIFIED（需要模型/工具触发）
直接 HTTP tools/call：PASS，33 harnesses
```

仍未完成：

```text
DSH 侧实际调用 mcp__im2a__im2a_harness_list
server/discover 以外工具的 modern result 完整 schema
HTTPS/remote auth
live profile
```

---

## P0-9：adapter host 路径收口 + 密钥轮换（2026-10-30）

公开 adapter 路径：

```text
plan() -> planHost()      不再默认写 cordis.patch.yml
apply({plan:host}) -> dsh plugin add
rollback(applied:host) -> dsh plugin remove
planPatch()/rollbackPatch() 仅保留为内部兼容测试
```

host install/uninstall 加固：

```text
.im2a-install.lock 单写者锁
planDigest 校验
package.json / pnpm-lock.yaml / cordis.patch.yml 前后 sha256
.bak/im2a-host-<timestamp>-<hash>/ 备份
失败时 restoreSnapshot
Windows rename/CLI 失败分类
```

验证：

```text
dsh-adapter.test.mjs: 6/6 pass
host plan/apply/rollback: PASS
apply -> 3 个文件 afterSha
rollback -> host-uninstall，im2a-mcp 行消失
```

SSH 轮换：

```text
new fingerprint: SHA256:kVFZeBHFELyCKV/uhXhGgXQWSbCO8smIGHlP+tImdJY
old key: removed from Vultr authorized_keys
old key login: permission denied
18080 tunnel: new key, gateway 200
```

仍未完成：

```text
DSH 侧精确 Tool.listTools 名称
DSH 发起的真实 tools/call
live profile 安装
release-manifest DSH evidence
```

---

## P0-10：CI evidence 输出 + observed 提升（2026-10-30）

新增：

```text
packages/harness-adapters/dsh/verify-cli.mjs
P0-EVIDENCE.json
P0-STATUS.json
```

中途出现过一次失败证据：

```text
ok=false, status=failed, code=TIMEOUT
Vultr 18080 不可达
```

gateway restart 后恢复；本地隧道重建，最终 PASS：

```json
{
  "ok": true,
  "status": "passed",
  "harnessCount": 33,
  "transport": "pipe",
  "checks": [
    "profile-mcp-installed",
    "initialize: im2a-harness-bridge",
    "tools/list: im2a_harness_list",
    "tools/call: harnesses=33"
  ]
}
```

evidence：

```text
P0-EVIDENCE.json
checkedAt=2026-10-29T23:39:15.010Z
mcpBinSha256=63c3eade...
credential: process env only (not recorded)
```

DSH-mediated 证据：

```text
probe: im2a-dsh-probe
tool: mcp__im2a__im2a_harness_list
DSH version: 0.2.1-rc.3
transport: streamable-http
result: ok=true, isError=false
harnesses: 33
```

状态更新：

```text
dsh: planned -> observed -> verified
verify.mode=dsh-toolruntime
verify.lastResult=passed
verify.evidence=P0-DSH-TOOL-CALL.json
scope=sandbox DSH ToolRuntime; live profile not installed
```

边界：

```text
verified 仅限沙箱 DSH ToolRuntime
live profile 未安装
其余 11 个 adapter 未验证
release-manifest Compat 仍为 blocked（剩余 adapter）
```

---

## 专家复核后修正

1. **A2A profile 不再是 0.3 形状**
   - 删除 `agentCard.url` 必填
   - 改为 `supportedInterfaces[]`
   - 补齐 `version` / `defaultInputModes` / `defaultOutputModes` / `skills[].tags`
   - `securitySchemes`/`extensions` 放回 AgentCard/ capabilities 语义
2. **校验器不再自愈**
   - `validate.mjs` 只读，不再调用 `generate()`
   - 缺失/过期 dist 直接 `BLOCKED`
   - 新增“篡改源文件后必须失败”的负控
3. **Schema 真正执行**
   - 6 份本地 schema 对 fixture/source 执行
   - A2A 负例必须被拒绝
4. **MCP 钉版**
   - 2026-07-28 改为 `pinned`
   - `source/mcp.json`、`plugin.json.mcp`、schema 三者统一
5. **九方门禁**
   - manifest 非 draft 必须有 commit/components/artifacts
   - gate evidence 改为对象数组
   - 发布态门禁仍需 CI 生成证据

## 验证结果

```json
{
  "ok": true,
  "platform": "2.3.45",
  "go": "2.3.26",
  "adapters": 12,
  "targets": 12,
  "skills": 4,
  "gates": 9,
  "a2a": "1.0/v1.0.1",
  "mcp": "2026-07-28",
  "sourceHash": "7b26bf1124ab230fcb436076e9b0b244b78cea8d208de4357d41d702c29ff9cf",
  "generatedTargets": 12,
  "schemaChecks": 6
}
```

测试：

```text
3 tests / 3 pass
- version matrix + schemas + dist
- A2A negative fixture rejected
- tampered source cannot self-heal
```

## 仍未完成

- A2A 上游 `a2a.proto` 未 vendored；尚未用官方 proto 生成完整 schema，只做了 profile 级校验
- 本地 JSON Schema 校验器是子集实现，不是 standards-complete；CI 需换 ajv
- 12 个 adapter 只有契约，没有真实 detect/plan/apply/verify/rollback
- `contract.ts` 未做 `tsc` 类型检查（本机无 tsc）
- 没有 OpenAPI 请求/响应契约
- 生成器只输出 skeleton meta，没有平台真实格式
- Memory Hub、UI/Tauri/VS Code 壳未实现
- release-manifest components/artifacts 为空
- 九方 gate 全部 pending，CI/Compat/Release/npm-publish/healthz 未执行
- 未修改现有 Go/Node/Browser/Ops 生产线
- 未 commit/tag/Release/npm publish

## 本地验证

```bash
node packages/agent-plugins/generator/src/cli.mjs generate
node scripts/validate.mjs
node tests/p0.test.mjs
```

注意：本机沙箱下 `node --test` 子进程会出现 `spawn EPERM`；直接执行测试文件通过。CI 仍需验证 `test:ci`。

## P0-LIVE：DSH live window（2026-10-30）

已安装 live：

```text
@twinsearth/im2a-dsh-plugin 0.1.0
@twinsearth/im2a-dsh-plugin-live 0.1.0
```

live DSH 中真实调用：

```text
mcp__im2a__im2a_harness_list
ok=true, isError=false, harnesses=33
```

证据：

```text
P0-LIVE-DSH-TOOL-CALL.json
```

生命周期：

```text
host bundle：live installed
live overlay：live installed
probe：已移除
launcher token：profile 外，用户 ACL
full desktop restart：未执行；HMR 已加载
live HTTP healthz：BLOCKED（/healthz 404，/api/health 401）
host live rollback drill：未执行
```

结论：

```text
DSH live tool call = PASS
DSH live healthz = UNVERIFIED
DSH live rollback drill = PARTIAL（probe 已回滚，host 未回滚）
整体 v2.3.45 = NO-GO
```
## DSH rc.5 compatibility incident（2026-10-30）

```text
DSH: 0.2.1-rc.5
旧插件: @twinsearth/im2a-dsh-plugin 0.1.0 / engines.dsh 0.2.1-rc.3
结果: 不兼容，live 插件已卸载
新包: 0.1.1 / engines.dsh 0.2.1-rc.5
沙箱: install + dump-config PASS，无 incompatible warning
live: 未重装（六方发布优先）
旧 live 证据: rc.3-era，已标记 stale，不作为 rc.5 证据
```