# IM2A v2.5.0 · Skill Runtime MVP（Agent Plugins 2.0）

Status: **MVP design（pre-ADR）**。不改 Go 网关、不发版、不声称已实现。
Baseline: platform `2.4.6` / Agent Plugins profile `1.0` / MCP `2026-07-28`。
Reference: Hermes Agent 的 skill 形状（L0/L1/L2、`skill_manage`、`curator`），
信任模型按 `AGENT-PLUGINS-2.0-AND-EVOLUTION-ARCH-v2.5.0.md` §0 改造为
**proposal → staging → promotion**。

---

## 0. 结论

只做一条闭环：

> **技能是不可变的内容寻址包；agent 只能提案，curator 只能暂存，operator 才能晋升/回滚。
> 回滚是指针移动，不是文件覆盖。**

四个模型侧接口是 `skills_list` / `skill_view` / `skill_manage` / `curator`。
`promote` / `rollback` **刻意不放进这四个工具**，只存在于 operator CLI/API。
这样“agent 不能自改 active 技能”是结构属性，不是提示词约束。

---

## 1. 边界

**做**

- 2.0 `SKILL.md` 自描述与内容寻址。
- 四级接口 + 渐进披露 L0/L1/L2。
- 提案、暂存、晋升、回滚、审计账本。
- 单机文件存储，可被 stdio MCP 或现有 HTTP MCP 桥挂载。
- 8 条可执行测试。

**不做（MVP 明确拒绝）**

- 影子流量 / 金丝雀 / 自动晋升（P1）。
- LLM 评分评测（P1；MVP 只做确定性 eval runner 契约）。
- 修改 bundled 技能（MVP 只读；fork 是 P1）。
- 合并/去重技能、自动归档（P1；MVP 只扫描和提案）。
- 多租户、远程 registry、包签名（P1）。
- 任何 formation / 委托 / 凭据面改动。

---

## 2. 落点

新增一个私有包，不碰 Go 网关：

```text
packages/skill-runtime/
  src/store.mjs        # blobs / packages / refs / 原子写
  src/index.mjs        # L0 索引、session pin、delta
  src/view.mjs         # L1/L2、路径校验
  src/propose.mjs      # skill_manage
  src/stage.mjs        # G0-G2
  src/promote.mjs      # G4-G5 + rollback
  src/curator.mjs      # scan/status/report
  src/policy.mjs       # 允许动作、scope、静态拒绝表
  src/ledger.mjs       # ledger.jsonl / usage.jsonl
  mcp.mjs              # 4 个 MCP tool handler（stdio）
  cli.mjs              # operator: promote/rollback/history
  test/skill-runtime.test.mjs
  fixtures/skills/...
packages/protocol/schemas/skill-2.0.schema.json
packages/protocol/schemas/skill-package.schema.json
packages/protocol/schemas/skill-proposal.schema.json
```

- 技能真相：仓库根 `skills/`（只读）。现有
  `packages/agent-plugins/source/skills/` 的重复副本必须先合并或改为生成物；
  否则本 MVP 的 bundled 索引用 fixture root，不接生产。
- 运行时存储：`IM2A_SKILL_HOME`，默认 `~/.im2a/skills`；测试用临时目录。
- 适配器：`mcp.mjs` 走 stdio；`packages/im2a-http-mcp/server.mjs` 复用同一
  handler 暴露 streamable HTTP。skill-runtime 作为独立 MCP server 挂载，
  Go 网关不需要改；后续可由网关代理同一 JSON 形状。
- 现有 `plugin.json.skills[]` 仍是字符串（`packages/agent-plugins/source/plugin.json:6-10`）；
  2.0 改为 `{id, ref, sha256, profileVersion}`，其中 `sha256` 即 `packageSha`。
  这是晋升/回滚成为指针移动的前提。

---

## 3. 数据模型

### 3.1 `SKILL.md` 2.0

```yaml
---
profileVersion: "2.0"
name: incident-triage
description: "Triage a production incident to an owner fast."
version: "1.0.0"
tags: ["incident", "oncall"]
resources:
  - path: references/severity.md
    description: "Severity matrix and escalation thresholds"
---
# Incident Triage
...
```

规则：

- `profileVersion` / `name` / `description` 必填；`description` ≤ 60 字符。
- `resources[].path` 必须存在于包内；没有声明时，L1 manifest 用每个文件
  首个标题自动生成 `title`，作者不必手写。
- **禁止** `permissions` / `tools` / `agents` / `env` / `credentials`。
  这些是能力与授权声明，归 `plugin.json.agents[]` 和 L6，不属于技能正文。
  1.0 技能导入时这些字段被剥离并记入 `provenance.droppedFields`，不生效。
- 正文硬上限 32 KiB；超限报 `SKILL_BODY_TOO_LARGE`，要求拆到 `references/`，
  不做静默截断。

### 3.2 内容寻址

```text
$IM2A_SKILL_HOME/
  blobs/<sha256>                 # 不可变文件字节
  packages/<packageSha>.json     # 包 manifest
  refs/bundled.json              # 启动时 skills/ 只读快照，不参与晋升
  refs/active.json               # 唯一 managed active 指针
  refs/history/<skillId>.jsonl   # 指针历史
  proposals/<proposalId>.json    # 可变提案记录
  staging/<proposalId>.json      # 不可变暂存快照
  reports/<runId>.json
  ledger.jsonl                   # 变更审计，fail-closed
  usage.jsonl                    # view/list 使用记录，best-effort
  locks/<skillId>.lock
```

`packageSha = sha256(canonicalJson({profileVersion, skillId, version, description,
files:[{path, sha256, bytes}]}))`，按 path 排序；不包含 `packageSha` 自身和
自动生成的 title。

`refs/bundled.json` 是启动时对 `skills/` 的只读快照；`refs/active.json` 只存
managed 技能。effective index = bundled ∪ managed，managed 不能占用 bundled id。
bundled 升级只影响新 session（旧 session 仍 pin 旧哈希），不经过晋升。
`active.json` 只存 managed 指针：

```json
{
  "schemaVersion": "1.0",
  "indexEtag": "sha256:...",
  "skills": {
    "incident-triage": {
      "packageSha": "...",
      "version": "1.0.0",
      "origin": "agent",
      "previousSha": "...",
      "promotedAt": "2026-10-30T12:00:00Z",
      "proposalId": "prop_..."
    }
  }
}
```

`indexEtag` 是对 effective index（排序后的 `id + packageSha`）的哈希，由运行时
计算并缓存在 `refs/active.json`；bundled 快照变化时启动阶段重算。

写入顺序固定为：blob → package manifest → staging → active 指针。每一步先写
临时文件再 `rename`；active 指针是唯一提交点。进程崩溃最多留下孤儿 blob，
不会留下半更新的 active。

### 3.3 提案与账本

提案是**小记录**，正文和文件也在 blob 里：

```json
{
  "proposalId": "prop_...",
  "skillId": "incident-triage",
  "kind": "create | patch | archive",
  "status": "proposed | staged | promoted | rejected | superseded",
  "revision": 1,
  "baseSha": null,
  "candidateSha": "...",
  "draft": {
    "description": "...",
    "version": "1.0.0",
    "bodySha": "...",
    "files": [{"path": "references/severity.md", "sha256": "..."}]
  },
  "evidence": {"traceIds": [], "failureRefs": [], "reason": "..."},
  "createdBy": {"actor": "agent | curator", "agentAddress": "...", "sessionId": "..."},
  "stage": {"packageSha": "...", "gates": [], "eval": null},
  "approval": null,
  "promotion": null
}
```

`ledger.jsonl` 每次变更一行，包含 `ledgerId / at / actor / agentAddress /
sessionId / traceId / action / skillId / proposalId / fromSha / toSha /
result / reason`。**账本写失败则操作失败**（与 Hermes 的 best-effort ledger
不同；这里审计是闸门的一部分）。`usage.jsonl` 只记 `list/view/file_view`
的 hash 与 session，不记正文、不记对话内容。

---

## 4. 四个接口

### 4.1 `skills_list`（L0）

```text
in  { query?: string, limit?: 1..100 = 50, cursor?: string }
out { items: [{id, description, version, packageSha, tags}],
      total, nextCursor?, indexEtag,
      delta?: {added:[...], removed:[id], changed:[...]} }
```

- 只返回 effective `active`（bundled 快照 + managed）；`proposed/staged` 不进入模型目录。
- 不返回正文、不返回文件内容；`description` 已在写入门槛限制到 60 字符。
- 稳定按 `id` 排序；`query` 对 id/description/tags 做大小写不敏感子串匹配。
- `delta` 是相对**本 session 首次快照**的差异；系统提示里的 L0 索引不重写
  （保 prompt cache），模型看到 delta 后自行决定是否重新加载。
- 100 个技能约 2k tokens；这是 L0 的预算上限。

### 4.2 `skill_view`（L1/L2）

```text
in  { id: string, path?: string, offset?: 1.., limit?: 1..2000 }
out L1 { id, description, version, profileVersion, packageSha, etag,
         body, bodyBytes,
         files: [{path, bytes, kind: "text"|"binary", title}],
         provenance: {origin, author, createdAt, baseSha?},
         updateAvailable?: boolean,
         disclosure: {level: 1, bodyBytes, bodyMax: 32768} }
out L2 { id, packageSha, path, fileSha, etag, text, bytes,
         lines: {offset, limit, total},
         disclosure: {level: 2} }
```

- 省略 `path` = L1：只加载 `SKILL.md` 正文 + 文件清单，不加载任何 reference。
- 指定 `path` = L2：只加载该文件的一段；路径必须**精确命中**包 manifest。
- 文件清单的 `title` 让模型在 L1 就能判断该不该进 L2，避免“先读再猜”。
- 单个文件 64 KiB；超过则用 `offset/limit` 分页，响应显式给 `lines.total`。
- 二进制返回 `BINARY_FILE` + 元数据，不返回字节；`scripts/**` 可读但绝不执行。
- 路径拒绝：绝对路径、`..`、URL 编码绕过、反斜杠、symbolic link、manifest 外文件。
- **session pin**：session 第一次 `skills_list`/`skill_view` 时快照
  `{indexEtag, packages}`。中途 active 变更时，旧 session 继续读旧包，返回
  `updateAvailable: true`；新 session 读新包。这样不会在一次任务中途换指令。
- 旧包 blob 若被清理，返回 `SKILL_VERSION_GONE`（带当前 activeSha），绝不静默换版。
- `etag` = `"sha256:<bodySha|fileSha>"`；MVP 只输出，`ifNoneMatch` 放 P1。

### 4.3 `skill_manage`（只写提案）

```text
in  { action: "create"|"patch"|"archive"|"write_file"|"remove_file",
      id: string,
      proposalId?: string,
      baseSha?: string,
      description?: string,
      version?: string,
      body?: string,
      path?: string,
      content?: string,
      evidence?: {traceIds?: string[], failureRefs?: string[], reason: string},
      message?: string }
out { proposalId, status: "proposed", skillId, kind,
      baseSha, candidateSha,
      diff: {files: [{path, change, adds?, dels?}], bodyChanged: boolean},
      gates: [{id, status, detail?}],
      next: "curator.stage",
      traceId }
```

动作语义：

| action | 必填 | 结果 |
|---|---|---|
| `create` | `id/description/body/evidence` | 新技能提案；id 不得与 effective index 冲突；`version` 默认 `0.1.0` |
| `patch` | `id/baseSha/body/evidence` | 替换正文提案；`baseSha` 必须等于当前 managed active；`version` 默认 patch bump |
| `archive` | `id/baseSha/evidence` | 移除提案；不删文件 |
| `write_file` | `proposalId/path/content` | 给未暂存提案加/改支持文件 |
| `remove_file` | `proposalId/path` | 从提案移除支持文件 |

硬规则：

- **只写 `proposals/`，永不碰 `refs/active.json`**；返回的 `candidateSha` 不是 active。
  `candidateSha` 用与 `packageSha` 相同的 canonical manifest 算法；stage 必须
  重算出同一值，否则 `CANDIDATE_DRIFT`。
- 运行时根据结构化字段生成 frontmatter；agent 不能伪造 `profileVersion`、
  `permissions`、`tools` 或 `env`。`body` 是 frontmatter 之后的正文，不得以
  `---` 开头；`description` 不得含换行或控制字符。
- 正文/文件统一 NFC；非 NFC 直接 `NFC_REQUIRED`。
- `path` 仅允许 `references/`、`templates/`、`examples/`、`scripts/` 下的文本文件；
  单文件 ≤ 64 KiB，单包 ≤ 256 KiB、≤ 20 个文件。
- 同一 actor 对同一 skill 同时只能有 1 个 `proposed`；每 session 最多 20 个提案。
- `bundled` 技能返回 `SKILL_READ_ONLY`；MVP 不允许改仓库技能，P1 再开 fork。
- 错误码：`SKILL_NOT_FOUND`、`SKILL_EXISTS`、`SKILL_READ_ONLY`、`BASE_MISMATCH`、
  `PROPOSAL_NOT_FOUND`、`PROPOSAL_NOT_OWNED`、`PROPOSAL_NOT_EDITABLE`、
  `INVALID_ID`、`DESCRIPTION_TOO_LONG`、`BODY_TOO_LARGE`、`PATH_INVALID`、
  `FILE_TOO_LARGE`、`NFC_REQUIRED`、`PROPOSAL_LIMIT`。

### 4.4 `curator`（只读 + 暂存）

```text
in  { action: "status"|"scan"|"propose"|"stage"|"report",
      proposalId?, skillId?, kind?, evidence?, evalRun?, dryRun?, limit? }
out status  { lastScanAt, counts: {active, staged, proposed, stale, archived},
              pinned: [], pendingProposals: [] }
out scan    { runId?, scannedAt, candidates: [{skillId, reason, metrics,
                                               suggestedAction}] }
out propose { proposalId, status: "proposed", ... }
out stage   { proposalId, status: "staged", packageSha, gates, eval }
out report  { markdown, counts, candidates }
```

- **挂载范围**：只挂给 curator service identity，不挂给主 agent。主 agent
  调用 `stage` 直接 `FORBIDDEN`。
- `status/scan/report` 只读；`scan` 不自动归档、不改 active。
- `propose` 只支持 `archive`（以及 P1 的 merge），等价于以 curator actor 调
  `skill_manage(archive)`；patch 由 curator 作为 actor 调用 `skill_manage`
  产生提案，再 `stage`，避免 curator 拥有第二套写入口。
- `stage` 顺序执行 G0-G2；`evalRun` 给了就校验并附加，没给则 `evalStatus:
  "pending"`，晋升会被挡住。暂存记录不可变；重新 stage 生成新 revision。
- curator **不能** `promote`、`rollback`、`pin`、`grant`、读凭据、调业务工具。
  `policy.mjs` 是静态允许表；运行时和测试都查它。

扫描规则（确定性，无 LLM）：

| reason | 条件 | 建议 |
|---|---|---|
| `stale` | active、非 pinned、`lastLoadedAt < now-14d` | 观察/重评 |
| `archive` | active、非 pinned、`lastLoadedAt < now-30d`、origin=agent | archive 提案 |
| `unused` | `loads=0` 且 `createdAt < now-14d` | 观察，不归档 |
| `failing` | 最近 7 天 `failureRefs ≥ 3` | patch 提案 |
| `overlap` | 描述 token Jaccard ≥ 0.8 | P1 merge |

`pinned`、被 cron/formation 引用的技能不进入 archive 候选；零使用有 14 天
宽限期（“没被用过”不等于“可删除”）。

### 4.5 晋升 / 回滚（operator only）

四个模型工具之外，单独两条 admin 接口（同一 handler 也暴露为
`node packages/skill-runtime/cli.mjs`）：

```text
POST /v1/skills/proposals/{proposalId}/promote
  { approvalId, expectedActiveSha, idempotencyKey, reason }
  -> { skillId, activeSha, previousSha, promotedAt, proposalId, ledgerId }

POST /v1/skills/{id}/rollback
  { toSha?, reason, idempotencyKey }
  -> { skillId, activeSha, previousSha, rolledBackAt, ledgerId }

GET  /v1/skills/{id}/history
```

- 鉴权：operator token + `skill.promote` / `skill.rollback` scope。
- 晋升必须有 L6 签发的 `approvalId`；approver ≠ proposal.createdBy；
  approval 绑定 `packageSha`、一次性、带过期时间。
- `expectedActiveSha` 做乐观并发；不匹配返回 `BASE_MISMATCH`，不覆盖别人刚晋升的版本。
  create 的 `expectedActiveSha` 必须是 `null`（该 id 尚不存在）。
- 回滚不需要二次审批（目标是曾经 active 的哈希），但必须 operator + reason。
  `toSha` 缺省为 `previousSha`；只接受该 skill 历史里出现过的哈希，
  防止把指针指向任意包。create 的回滚目标是 `null`，即把该 id 从 active 移除，
  包仍保留在 blobs 中。
- 原子提交：持有 per-skill lock → 写临时 active → rename → 写 ledger。
  ledger 失败则回滚指针，返回失败。
- `idempotencyKey` 重放返回第一次结果，不产生第二条 ledger。
- 回滚是**可逆**的：回滚会把 `previousSha` 设为被回滚掉的版本，再回滚一次即可前进。
- 本地指针切换 p99 < 100 ms；验收口径仍是“≤60 s 完成一次可审计回滚”。

---

## 5. 渐进披露

| 级别 | 入口 | 返回 | 预算 | 何时用 |
|---|---|---|---|---|
| L0 | 系统提示 + `skills_list` | id + 60 字符 description | 100 技能 ≈ 2k tokens | 每轮都在 |
| L1 | `skill_view(id)` | `SKILL.md` 正文 + 文件清单 | 正文 ≤ 32 KiB | 判断相关后 |
| L2 | `skill_view(id, path)` | 单个 reference 的一段 | 单文件 ≤ 64 KiB，分页 | 正文指向细节时 |

三条约束：

1. **不静默截断**：L1 超限直接报错要求拆分；L2 用 `offset/limit` 并显式返回
   `lines.total`。
2. **哈希绑定**：每次 L1/L2 都返回 `packageSha`（+ L2 的 `fileSha`），
   usage/ledger 记录同一对哈希；这样“昨天能跑、今天不行”能归因到某次晋升。
3. **session pin**：一次任务中途不换指令；新版本通过 `updateAvailable` /
   `delta` 告知，由模型决定是否重读。

文件清单的 `title` 来自 `resources[].description` 或文件首个标题，最多 120 字符；
这让 L2 的选择有依据，而不是让模型盲猜文件名。

---

## 6. 生命周期与闸门

```text
skill_manage(create/patch/archive)
        │
        ▼
   [proposed] ──reject──> [rejected]
        │
   curator.stage（G0-G2）
        ▼
   [staged] ──newer stage──> [superseded]
        │
   G3 eval（≥20 例，0 回归）
        │
   G4 approval（L6，approver ≠ proposer）
        │
   G5 atomic pointer + ledger
        ▼
   [active] ──rollback──> [active: previousSha]
```

| 闸门 | 在哪 | 检查 | 失败 |
|---|---|---|---|
| G0 schema | stage | profileVersion、id、description、包 manifest | `STAGE_SCHEMA` |
| G1 content | stage | 32 KiB / 64 KiB / 20 文件 / NFC / 路径 / 二进制 / 密钥模式 / 禁止能力字段 | `STAGE_CONTENT` |
| G2 base | stage | create 的 id 未被占用；patch/archive 的 `baseSha == active`；evidence 非空 | `BASE_MISMATCH` |
| G3 eval | promote | `cases ≥ 20`、`packageSha` 匹配、`regressions == 0`、passRate ≥ baseline | `EVAL_BLOCKED` |
| G4 approval | promote | approval 未过期、未使用、绑定 packageSha、非自批 | `APPROVAL_BLOCKED` |
| G5 commit | promote | per-skill lock、expectedActiveSha、ledger 可写 | `PROMOTION_BLOCKED` |

`evalRun` 最小形状：

```json
{
  "suiteId": "incident-triage-v1",
  "packageSha": "...",
  "cases": 20, "passed": 20, "failed": 0,
  "regressions": 0, "baselineSha": null,
  "reportSha": "...", "runner": "deterministic",
  "completedAt": "2026-10-30T12:00:00Z"
}
```

MVP 只实现 deterministic runner 契约（读 fixture 跑断言）；不实现 LLM 评分。
G3 的存在是为了让“没有评测就没有晋升”成为代码事实，而不是 P2 的口号。

---

## 7. P0 / P1

### P0（MVP 阻断项，全部做完才算闭环）

1. **2.0 技能契约**：`SKILL.md` frontmatter + package manifest + proposal schema；
   1.0 投影时剥离能力字段。
2. **内容寻址存储**：blobs / packages / bundled 快照 / managed active 指针 /
   history；原子 rename；崩溃只留孤儿 blob。
3. **四个接口**：`skills_list`、`skill_view`、`skill_manage`、`curator`，
   含上表的错误码与权限。
4. **渐进披露**：L0/L1/L2、文件清单 title、session pin、`updateAvailable`、
   `SKILL_VERSION_GONE`。
5. **提案-暂存-晋升**：proposed/staged/promoted/rejected/superseded 状态机；
   G0-G5；operator 审批。
6. **回滚**：指针移动、历史校验、可逆、幂等、ledger 先行。
7. **curator 最小权限**：scan/propose/stage；静态 policy + 运行时拒绝；
   无凭据/业务工具。
8. **审计与测试**：ledger/usage 分离；8 条测试进 `npm run test:ci`。

### P1（MVP 之后，按顺序）

1. 影子/金丝雀：staged 包按 session 比例放量，eval/失败率对比，自动中止。
2. LLM 评分评测：每技能 ≥20 例、基线自动采集、回归定位到用例。
3. curator 合并：overlap 提案、umbrella 重构、保留 support files。
4. bundled 技能 fork/override：插件升级冲突检测与三方合并。
5. Web 审核台：diff、闸门、eval 报告、一键晋升/回滚、历史。
6. 包签名与远程 registry；多租户隔离。
7. 自动 stale 转换（仍不自动 archive）；pin/引用保护。
8. `skill_view` 的 `ifNoneMatch`、全文检索、L0 排序。

---

## 8. 八条测试

测试入口：`node --test packages/skill-runtime/test/*.test.mjs`，加入
`package.json` 的 `test:ci`。全部使用临时 `IM2A_SKILL_HOME`、假时钟、
假 approval issuer、deterministic eval fixture；不联网。

| # | 名称 | 动作 | 必须断言 |
|---|---|---|---|
| T01 | `disclosure-budget` | 3 个技能、1 个含 2 个 reference；依次 L0/L1/L2 | L0 无 `body`、总字节 < 4 KiB；L1 有正文、`files` 有 title、无 reference 内容；L2 只返回目标文件；三者 `packageSha` 一致 |
| T02 | `package-boundary` | `../secret`、绝对路径、`%2e%2e`、反斜杠、symlink、二进制、>64 KiB 文件 | 前五类分别 `PATH_INVALID`/`PATH_NOT_FOUND`；symlink 在 stage 被拒；二进制 `BINARY_FILE`；大文件分页且 `lines.total` 正确；包外文件零读取 |
| T03 | `proposal-only` | create / patch / archive；再并发晋升另一版本 | 每次 `status=proposed`，`active.json` 字节不变，`skills_list` 不变；旧 base 的 patch 再 stage 返回 `BASE_MISMATCH`；非法 id/长描述/超限正文/非 NFC 均拒绝且不落提案；1.0 的 `permissions/tools/agents` 被剥离并记入 `droppedFields` |
| T04 | `stage-gates` | 缺 evidence、含密钥串、base 不匹配、合法提案各 stage 一次 | 前三者 G1/G2 失败且 active 不变；合法者 `staged`、`packageSha` 稳定；重复 stage 幂等；同技能新提案把旧 staged 标为 `superseded` |
| T05 | `promotion-gates` | 未 stage、19 例 eval、1 个回归、自批、过期 approval、合法晋升、同 key 重放 | 前五类全部 `*_BLOCKED` 且 active 不变；合法晋升后 `activeSha` 变更、`previousSha` 正确、ledger 恰好 1 条；重放同 key 同结果且不新增 ledger |
| T06 | `rollback` | 晋升 v2 → 回滚 v1 → 再回滚 v2；回滚 create；回滚 archive；非法 toSha | 每次指针/正文哈希正确，`previousSha` 可逆，history 条数正确；非法目标 `ROLLBACK_TARGET_INVALID`；耗时 < 60 s（本地断言 < 1 s）；同 key 不重复记账 |
| T07 | `curator-least-privilege` | 静态 policy + 运行时调用 + 包内密钥 | curator 允许动作恰为 `status/scan/propose/stage/report`；`promote/rollback/pin/grant` 全部 `FORBIDDEN`；主 agent 调 `stage` 被拒；无法读 `IM2A_HARNESS_TOKEN`/凭据；ledger actor=`curator` |
| T08 | `session-pin-delta` | session A 先 list（v1），晋升 v2，再 view/list；开 session B | A 的 view 返回 v1 正文 + `updateAvailable=true`；A 的 list 返回 `delta.changed` 且 `indexEtag` 指向 v2；B 返回 v2 且 delta 为空；人为删 blob 后 A 得 `SKILL_VERSION_GONE`，不静默换 v2 |

补充断言（可放在 T05/T06 内，不单独计数）：promote/rollback 的 `traceId`
出现在 ledger；`usage.jsonl` 不含正文；所有拒绝路径都不产生 active 变更。

---

## 9. 演示路径

```text
1.  seed：4 个 bundled 技能 → skills_list 返回 4 条，无正文。
2.  agent：skill_manage(create, incident-triage) → proposed。
3.  curator：curator.stage(proposal, evalRun 20/20) → staged。
4.  operator：promote(approvalId, expectedActiveSha) → active；新 session 可 view。
5.  agent：patch v2 → stage → promote。
6.  旧 session：view 仍返回 v1 + updateAvailable；新 session 返回 v2。
7.  operator：rollback → v1；history 两条；再 rollback → v2。
```

验收命令：

```text
node --test packages/skill-runtime/test/*.test.mjs
node packages/skill-runtime/cli.mjs history incident-triage
```

---

## 10. 与 Hermes 的取舍

| Hermes | 本 MVP | 原因 |
|---|---|---|
| `skills_list` / `skill_view` L0/L1/L2 | 采纳，加 `packageSha`/`fileSha` 与 session pin | 省 token 且可归因 |
| `skill_manage` create/patch/delete | 采纳形状，改为只写提案 | 单租户特性不可移植（R1） |
| curator 的 usage/ledger/rollback | 采纳 usage + ledger + 指针回滚 | 可维护、可审计 |
| curator 自动 stale/archive | 只扫描、只提案，不自动改 active | 发布面不能静默消失 |
| agent 可改任意技能 | 拒绝 | 提案 + 审批 + 回滚 |
| 系统提示保持稳定 | 采纳，L0 索引 + delta 注记 | prompt cache 不破 |
| 技能可声明 env 并注入 | 拒绝 | R5；能力归 L6/plugin |
| bundled 技能只读 | 采纳；P1 再 fork | 避免插件升级被运行时覆盖 |

---

## 11. 明确不承诺

- 这不是“技能质量自动变好”；MVP 只保证**变更可控、可审计、可回滚**。
- G1 的密钥扫描是模式匹配，不是证明；真正的门是 G4 人工审批。
- deterministic eval 只验证 fixture 断言，不代表真实任务质量；LLM 评分在 P1。
- 单机文件存储没有多租户隔离；跨机器/远程 registry 是 P1。
- 没有影子流量；晋升是“eval + 审批后直接切”，P1 才加金丝雀。
- 不改 Go 网关、不改 ident-code、不改 A2A/MCP 版本。
