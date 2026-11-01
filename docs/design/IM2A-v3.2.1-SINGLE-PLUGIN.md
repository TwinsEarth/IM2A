# IM2A v3.2.1 — 单包插件第一阶段（外壳）设计

状态：设计冻结候选，阶段一只交付外壳、注册表、别名层与元工具。
业务域实现不在本版范围。

## 0. 结论

| 项 | 决定 |
| --- | --- |
| 分发单元 | 1 个包 `dsh-im2a-plugin`（npm `@twinsearth/dsh-im2a-plugin`），1 个 `plugin.json`，1 个版本号 `3.2.1` |
| 内部边界 | 内核 1 + 能力模块 9 + 商用模块 5，均为包内目录，不是独立包 |
| 工具面 | 外部只调总线，命名 `im2a_<module>_<tool>` |
| 旧名兼容 | 全部 30 个旧工具名保留为别名，两个发布周期（3.2.x、3.3.x） |
| 缺失域 | identity / economy / governance 标 `planned`，不注册任何工具 |
| 商用域 | 5 个 `pro/` 模块标 `planned`，本版不加载任何商用代码 |

## 1. 范围

做：

1. 单包骨架与 `plugin.json`；
2. 模块注册表、拓扑加载、逆序卸载、依赖拒绝；
3. 总线路由与错误值；
4. 别名迁移表 `migrations/3.2.1-tool-aliases.json`；
5. 内核元工具 9 个；
6. 把现有 30 个工具挂到新名下，旧名同时可用；
7. 单 bundle：合并现 `im2a-dsh-plugin` 与 `-live` 两层为一条 MCP 行。

不做：9 个域的完整重写、5 个商用功能、Relay 许可与服务端签发、
C1–C7 的运行时新逻辑、schema 冻结哈希的强制校验。

## 2. 包结构

```text
packages/dsh-im2a-plugin/
  package.json                   # DSH bundle 清单：name/exports/icon/locale/patch
  cordis.patch.yml               # 单一 bundle，只插入一条 im2a-mcp 行
  locale/zh.json                 # 插件页标题与详情（zh）
  locale/en.json                 # 插件页标题与详情（en）
  icon.svg                       # 插件图标（SVG，< 256 KiB）
  plugin.json                    # 唯一 IM2A manifest（本版已落地）
  kernel/
    registry.mjs                 # 注册表、拓扑、别名解析、总线错误值（本版已落地）
    bus.mjs                      # 路由 + 超时 + 熔断（3.2.x 后续）
    gate.mjs                     # 宪法门控唯一插入点（3.2.x 后续）
    meta-tools.mjs               # 9 个内核元工具（3.2.x 后续）
  modules/                       # 9 个模块目录与 module.json（3.3.x）
  pro/                           # 5 个商用占位（3.3.x）
  migrations/
    3.2.1-tool-aliases.json      # 30 条别名 + 真实 schema hash（本版已生成）
    generate-aliases.mjs         # 从运行中的 bridge 生成别名表（本版已落地）
  contracts/                     # 冻结 schema 引用（3.2.x 后续）
```

阶段一允许模块的 handler 直接转发到现有 `packages/im2a-http-mcp`
的实现，不复制业务代码；这样别名层可以先上线，模块内聚在后续版本做。

## 3. plugin.json 字段

必填：`name`、`version`、`schemaVersion`、`kernel`、`modules`、`premium`。

```jsonc
{
  "schemaVersion": "3.0",
  "name": "dsh-im2a-plugin",
  "version": "3.2.1",
  "display": {
    "title": "智能体宇宙—IM2A",
    "locales": ["zh", "en"],
    "icon": "icon.svg"
  },
  "kernel": {
    "module_version": "1.0.0",
    "constitution_hash": null,        // 阶段一为 null，冻结后写入
    "meta_tools": ["status","pipeline","gate_check","constitution_audit",
                   "module_list","module_set","config_get","config_set","license_status"]
  },
  "modules": {
    "memory": {
      "module_version": "2.5.8",
      "status": "available",          // available | partial | planned | unavailable
      "required_scopes": ["memory:read"],
      "deps": [],
      "tools": ["search","get","propose","recall"]
    }
  },
  "premium": {
    "relay_required": true,
    "license_implemented": false,      // 阶段一必须显式为 false
    "modules": ["deploy_local","enterprise_net","cloud_backup","token_router","team_permissions"]
  }
}
```

`status` 是事实声明，不是装饰：`planned` 的模块不得出现在工具列表里。

## 4. 加载与卸载

拓扑序（固定，不依赖目录遍历）：

```text
kernel
 → memory → evolution
 → asset            (硬依赖 memory, evolution)
 → identity         (硬依赖 asset, evolution)
 → autonomy         (硬依赖 memory, asset, evolution)
 → economy          (硬依赖 asset, identity；soft: observability)
 → fleet            (硬依赖 asset, autonomy；soft: economy)
 → governance       (硬依赖 identity；soft: observability)
 → observability    (横切，最后)
```

规则：

1. 用 Kahn 算法对**硬依赖** `deps` 求拓扑序；同层按 manifest 声明顺序稳定排序；
2. 出现环 → 加载失败，错误里给出环上的模块名；
3. 若硬依赖处于 `planned` 或 `unavailable` → 依赖方降为 `unavailable` 并给出依赖链，
   不静默跳过；
4. `soft_deps` 只表达顺序意图（例如 `fleet` 想在 `economy` 之后加载），
   **不参与可用性判定**——否则 `economy` 计划态会连带把可用的 `fleet` 关掉；
5. 卸载按加载逆序；被依赖方仍在时拒绝卸载，返回依赖链；
6. 热更新：新版本先进入 `staged`，依赖方全部切换后旧版本才 `unload`。

阶段一只实现 1–5，热更新留到 3.3.x。

## 5. 总线

外部调用名 `im2a_<module>_<tool>`；
manifest 内部命名 `im2a.<module>.<tool>`（仅文档与配置使用）。

MCP 工具名必须是 `^[a-z0-9_]+$`，且当前 `docs/contracts/mcp-bridge-tool-catalog-2.0.schema.json`
的名字规则是「`im2a_` 加 3–41 位后缀」，所以**不能用点号**。
`im2a_observability_pipeline_dryrun` 正好 29 位，符合现有规则，不需要改 schema。

失败一律是值，不是协议错误：

```jsonc
{ "isError": true,
  "errorCode": "module_unavailable" | "scope_denied" | "timeout" | "circuit_open"
             | "upstream_unavailable" | "not_implemented",
  "module": "economy",
  "retryable": false,
  "detail": "module economy is planned in 3.2.1" }
```

- `not_implemented` 专供 `planned` 域，禁止返回假成功；
- 工具面不因许可状态变化而增删（能力闸与许可闸分离）；
- 阶段一不做熔断计数，只保留错误码位置。

## 6. 宪法门控

门控只在 `kernel/gate.mjs` 实现，所有总线调用先过门控再分发。

| 约束 | 阶段一落地 |
| --- | --- |
| C1 scope | 校验调用方 scope ⊇ 模块 `required_scopes`，缺失返回 `scope_denied` |
| C2 reserved_ratio | 只读暴露，禁止任何模块写；阶段一不新增写入口 |
| C3 轨迹授权 | 沿用现有 trace 授权字段，双写不新增 |
| C4 来源不可覆盖 | 沿用 `skill-claim.mjs` 的 origin 保留逻辑 |
| C5 私钥不代签 | 无新增签名路径 |
| C6 不可变制品 | 阶段一只校验包 `version` 单调，不做哈希冻结 |
| C7 检测与聚合分离 | 保持 `curator_audit` 只读 |

门控记录一条只读审计事件（内存环形缓冲 + 可选 JSONL），阶段一不落库。

## 7. 元工具（9 个，全部 `im2a_kernel_*`）

| 工具 | 权限 | 阶段一行为 |
| --- | --- | --- |
| `im2a_kernel_status` | 只读 | 已加载模块、版本、许可状态、门控健康度 |
| `im2a_kernel_pipeline` | 执行 | 只接受已注册 pipeline，未注册返回 `not_implemented` |
| `im2a_kernel_gate_check` | 只读 | 客户端预检，返回 allow/deny 与原因 |
| `im2a_kernel_constitution_audit` | 只读 | C1–C7 的静态自检结果 |
| `im2a_kernel_module_list` | 只读 | 模块、状态、依赖、工具数 |
| `im2a_kernel_module_set` | 写（本地） | enable/disable；依赖存在时拒绝 |
| `im2a_kernel_config_get` | 只读 | 白名单 key |
| `im2a_kernel_config_set` | 写（本地） | 白名单 key，`reserved_ratio` 不在白名单 |
| `im2a_kernel_license_status` | 只读 | 恒返回 `{ implemented: false, state: "not_implemented" }` |

`im2a_kernel_license_status` 必须如实返回未实现；
本轮不存在 Relay 握手代码（见 §10），禁止假装已接入。

## 8. 别名迁移

`migrations/3.2.1-tool-aliases.json` 每条：

```jsonc
{ "old_name": "memory_recall_v2", "new_name": "im2a_memory_recall",
  "module": "memory", "since": "3.2.1", "deprecated_after": "3.4.0",
  "status": "active", "schema_hash": "sha256:…" }
```

30 条映射：

| 模块 | 旧名 → 新名 |
| --- | --- |
| memory | memory_search → im2a_memory_search；memory_get → im2a_memory_get；memory_propose → im2a_memory_propose；memory_recall_v2 → im2a_memory_recall |
| asset | skill_view → im2a_asset_view；skill_manage → im2a_asset_manage；skill_claim → im2a_asset_claim；skills_list → im2a_asset_list；contribution_audit → im2a_asset_audit；skill_guard_check → im2a_asset_guard_check |
| autonomy | drive_list → im2a_autonomy_drive_list；drive_manage → im2a_autonomy_drive_manage；drive_propose → im2a_autonomy_propose；drive_calibrate → im2a_autonomy_calibrate；goal_buffer → im2a_autonomy_buffer；goal_schedule → im2a_autonomy_schedule；scheduler_cycle → im2a_autonomy_scheduler_cycle；scheduler_audit → im2a_autonomy_scheduler_audit |
| evolution | trace_ingest → im2a_evolution_ingest；reflect_trigger → im2a_evolution_reflect；curator → im2a_evolution_curator；curator_audit → im2a_evolution_curator_audit |
| fleet | im2a_harness_list → im2a_fleet_harness_list；im2a_send → im2a_fleet_send；im2a_recv → im2a_fleet_recv；im2a_presence → im2a_fleet_presence；im2a_history → im2a_fleet_history |
| observability | evolution_pipeline_dryrun → im2a_observability_pipeline_dryrun；autonomy_pipeline_dryrun → im2a_observability_pipeline_dryrun（`pipeline` 参数区分） |
| kernel | im2a_whoami → im2a_kernel_whoami |

冲突处理：两个 dryrun 合并为一个新名，旧名各自作为别名保留，
新名按 `pipeline` 参数分流（`evolution` / `autonomy`）。

校验规则（阶段一必须进测试）：

1. 30 个旧名一个不少、一个不多；
2. 新名唯一，且匹配 `^im2a_[a-z][a-z0-9_]{2,40}$`；
3. `planned` 模块不得有映射条目；
4. `schema_hash` 与工具目录里注册的 schema 一致，漂移即失败；
5. 5 个预算域之外不得新增顶层前缀（避免又回到散落工具）。

## 9. 商用 pro/

阶段一只写 5 个占位 `module.json`，全部 `status: planned`，
`relay_required: true`，不注册工具、不加载代码。

加载期静态检查（阶段一实现为校验函数，接入在 3.3.x）：

- 模块声明的工具名若与已加载模块冲突 → 拒绝；
- 模块若声明 `patches: [...]` 意图改写 2.x → 拒绝；
- 模块 `required_scopes` 超出内核上限 → 拒绝；
- 未经签名的产物 → 拒绝（签名校验留到 3.3.x）。

## 10. 事实校正（本轮取证，影响交付承诺）

1. **Relay 许可握手在 IM2A 仓库里不存在**。
   `packages/**` 内没有 license/lease/grace 代码；同类语义在
   `@twinsearth/w2m-dsh-plugin` 里。3.2.1 不得声称"已接许可"。
2. `im2a-http-mcp@0.22.0` 在 `127.0.0.1:18082` 实测健康：
   `initialize` 200、`tools/list` 30、`serverInfo.version=0.22.0`。
3. 单 bundle 安装后宿主侧已挂载 `mcp__im2a__*` 共 30 个（2026-11-02 00:xx 实测），
   此前「工具面可达、宿主侧未注册」的问题由单一 bundle 修复。
   真正卡住调用的是网关 `127.0.0.1:18080` 缺失，见
   [本地网关的 Windows 构建](IM2A-LOCAL-GATEWAY-WINDOWS.md)。
4. 契约冲突：`agent-plugin-2.0.schema.json` 对 `streamable-http`
   要求 `url` + `tlsRequired`，并对 `https` 有强制 pattern；
   而 profile 实际用的是 `http://127.0.0.1:18082/mcp`。
   3.2.1 必须二选一：放宽为「回环明文需 `allowInsecureLoopback: true`」，
   或强制 TLS。悬着会在企业交付评审被打回。
5. `.npmrc` 里私有 Verdaccio 处于注释禁用状态，安装实际回源公网；
   国内分发需要固定 scope registry 并附离线 tarball。

## 11. 阶段一验收

```text
plugin.json 自检                          PASS   validateManifest()
模块拓扑与逆序卸载                         PASS   tests/v321-plugin-shell.test.mjs
30 条别名映射完整性 + 新名唯一              PASS   30/30，来自运行中 bridge 的真实 schema hash
planned 模块无工具、返回 not_implemented    PASS   identity/economy/governance
tools/list = 旧名 + 新名 + 元工具           PASS   MCP 宿主侧挂载 30 个 mcp__im2a__*
单 bundle 安装后一条 MCP 行                 PASS   Config.listConfigs 仅 include:im2a-mcp
kernel_status 如实报告 planned/unavailable  PASS   statusReport()
真实工具调用（L3）                          PASS   im2a_whoami / presence / harness_list
```

已通过：`node --test tests/v321-plugin-shell.test.mjs` → 8 passed / 0 failed。
L3 证据见 [本地网关的 Windows 构建与验证](IM2A-LOCAL-GATEWAY-WINDOWS.md)。

## 12. 品牌与插件页展示

| 项 | 值 |
| --- | --- |
| 插件名 | `dsh-im2a-plugin`（npm：`@twinsearth/dsh-im2a-plugin`） |
| 主标题 | 智能体宇宙—IM2A |
| 详情页（zh） | `locale/zh.json` → `meta.title` / `meta.description` |
| 详情页（en） | `locale/en.json` → `meta.title` / `meta.description` |
| 图标 | `icon.svg`（SVG，< 256 KiB，随包分发） |
| 导出口 | `"./locale/*.json"`、`"./icon"`，均在 `files` 中 |

详情页文案（zh，原文照录）：

> 一个面向 Agent 开发&开放的即时连接网络：让每个人都拥有自己专属的 AI 秘书、
> 助理与管家&团队。传统 IM 连接人，人类被海量信息奴役；IM2A 连接 Agent 网络，
> 人类退居幕后，掌握最终裁决权。这不仅是 IM，更是 AGI 时代个人生活方式的
> 「数字平权」基础设施。

插件页读的是 `locale/*.json` 的 `meta`，不是 `plugin.json`；两处都写是为了让
包内文档与插件页一致，单一事实来源仍是 locale 文件。

## 13. 待决策

1. `allowInsecureLoopback` 与 TLS 二选一（§10.4）。
2. 别名保留两个发布周期的弃用时间点需写进 `CHANGELOG`。
3. DSH 侧 IM2A 工具未挂载是否本轮修复（需要重启，当前会话不宜重启）。
4. npm scope 用 `@twinsearth/dsh-im2a-plugin` 还是无 scope 的 `dsh-im2a-plugin`。
