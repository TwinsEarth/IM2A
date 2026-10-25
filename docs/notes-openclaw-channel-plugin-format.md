# OpenClaw 频道插件包格式（实测事实）

> 用途：给 `@im2a/openclaw-channel-relation` 建包提供**可直接照做**的字段级事实。
> 原则：**只记录实测或官方离线文档可查的内容**；无法确认的一律标 `未验证`，不猜格式。
>
> 观测环境（2026-10-25，只读）：
> - OpenClaw **2026.10.7 (157d8c2)**，安装于 Vultr `/usr/local/node24/lib/node_modules/openclaw`（下称 `$OC`）；
> - 参照插件：`openclaw-weixin@3.3.1`，安装于 `/opt/openclaw-plugins/openclaw-weixin`（下称 `$P`），同目录有 `openclaw-weixin-3.3.1.tgz`；
> - 生效配置：`/root/.openclaw/openclaw.json`；
> - 官方离线文档：`$OC/docs/plugins/**`、`$OC/docs/cli/plugins/**`（本文引用为 `openclaw/docs/...`）。

---

## 1. 真实安装布局（实测）

```
/opt/openclaw-plugins/
├── openclaw-weixin-3.3.1.tgz        # 分发包（154,836 B）
└── openclaw-weixin/                 # 解包后的插件根
    ├── package.json                 # npm 元数据 + openclaw 块
    ├── openclaw.plugin.json         # 预运行 manifest（必有）
    ├── index.ts                     # 源码入口（声明在 openclaw.extensions）
    ├── dist/index.js                # 构建产物入口（声明在 openclaw.runtimeExtensions）
    ├── dist/src/...                 # 构建后的运行时模块（channel.js 等）
    ├── node_modules/                # 插件自己的运行时依赖
    ├── assets/icon.png
    ├── README.md / README_EN.md / README.zh_CN.md
    ├── CHANGELOG.md / CHANGELOG_EN.md
    ├── LICENSE / NOTICE
    └── docs/
```

**加载方式（实测，不是 CLI 托管安装）**：配置里通过 `plugins.load.paths` 指向插件根，并在 `entries` 里 enable：

```jsonc
// /root/.openclaw/openclaw.json:2-10（节选）
{
  "plugins": {
    "load": { "paths": ["/opt/openclaw-plugins/openclaw-weixin"] },
    "entries": { "openclaw-weixin": { "enabled": true } }
  }
}
```

`openclaw plugins list` 实测输出（节选）：
```
Plugins (42/67 enabled)
  Source roots:
    stock: /usr/local/node24/lib/node_modules/openclaw/dist/extensions
  WeChat | openclaw-weixin | openclaw | enabled | /opt/openclaw-plugins/...weixin/dist/index.js | 3.3.1
```

> 结论：**外部插件 = 插件根 + `plugins.load.paths` + `plugins.entries.<id>.enabled=true`**。
> 这台机器**没有** `~/.openclaw/extensions/` 下的托管安装，也没有 CLI 安装记录；是手工解包后挂载。
> CLI 的 `openclaw plugins install <path|npm|clawhub:...>` 与 `-l/--link` 是另一条路径（见 §5）。

---

## 2. `package.json` 字段（实测 `$P/package.json`）

| 字段 | 实测值（行号） | 含义/要求（官方文档依据） |
|---|---|---|
| `name` | `"openclaw-weixin"`（:2） | **npm 包名**。ClawHub/官方插件常用 `@scope/name` 形式；具体 scope 规则 `未验证` |
| `version` | `"3.3.1"`（:3） | semver |
| `description` | 文本（:4） | 展示用 |
| `type` | `"module"`（:25） | ESM；文档要求 TypeScript ESM（`openclaw/docs/plugins/building-plugins.md:30`） |
| `files` | 白名单数组（:26-47） | npm 打包白名单；含 `index.ts`、`openclaw.plugin.json`、`dist/`、`README*`、`LICENSE`、`NOTICE`、`assets/icon.png` |
| `engines.node` | `">=22.22.3"`（:68-70） | 文档：Node 24.22.1+ 或 26.12.1+（building-plugins.md:31） |
| `peerDependencies.openclaw` | `">=2026.6.1"`（:74-76） | npm peer 元数据；兼容性契约实际看 `openclaw.compat.pluginApi`（见下） |
| `openclaw.compat.pluginApi` | `">=2026.6.1"`（:88-90） | **安装期强制的插件 API 下限**（`docs/plugins/manifest/package-json.md` 表格） |
| `openclaw.build.openclawVersion` | `"2026.10.3"`（:91-93） | 构建时宿主版本；**ClawHub 发布必需字段之一**（`docs/plugins/sdk-provider-plugins.md:210-214`） |
| `openclaw.extensions` | `["./index.ts"]`（:94-96） | 原生插件源码入口；必须在包目录内（package-json.md:35） |
| `openclaw.runtimeExtensions` | `["./dist/index.js"]`（:97-99） | 已安装包的**构建后 JS 入口**；必须在包目录内 |
| `openclaw.channel` | `{id, aliases, label, selectionLabel, docsPath, docsLabel, blurb, order}`（:100-111） | 频道目录元数据（标签/别名/文档/排序），预运行即可读 |
| `openclaw.install.npmSpec` | `"openclaw-weixin"`（:112-115） | 安装提示：默认从 npm 安装；还有 `clawhubSpec` / `localPath` / `minHostVersion` 等 |
| `publishConfig` | `{access:"public", registry:"https://registry.npmjs.org/"}` | npm 发布配置 |
| `scripts` / `devDependencies` | 构建/测试用（:48-86） | 不参与宿主加载 |

> 判断规则（官方）：**宿主加载插件代码前就要知道 → `openclaw.plugin.json`；打包/入口/npm 安装行为 → `package.json#openclaw`**（`docs/plugins/manifest/package-json.md:14-25`）。

---

## 3. `openclaw.plugin.json`（必有 manifest，实测 `$P/openclaw.plugin.json`）

> 官方要求：**每个原生插件必须在插件根提供 `openclaw.plugin.json`**；宿主不执行代码即读它做配置校验，缺失/非法 = 插件错误
> （`openclaw/docs/plugins/manifest.md:20`）。频道插件靠 **`channels` 字段**（不是 `kind`）声明自己拥有频道
> （`openclaw/docs/plugins/sdk-channel-plugins.md:80-83`）。

实测内容（行号为 `$P/openclaw.plugin.json`）：

```json
{
  "id": "openclaw-weixin",                    // :2   ← plugins.entries 的键
  "name": "WeChat",                           // :3
  "description": "Community-maintained ...",  // :4
  "version": "3.3.1",                         // :5
  "channels": ["openclaw-weixin"],            // :6   ← 声明拥有频道
  "channelConfigs": {                         // :7   ← channels.<id> 的预运行 schema
    "openclaw-weixin": {
      "schema": { "type":"object","additionalProperties":true,"properties":{ "botAgent":{...}, "blockStreaming":{...}, "replyProgressMessages":{...} } },
      "label": "WeChat",
      "description": "Weixin channel configuration."
    }
  },
  "configSchema": { "type":"object","additionalProperties":false,"properties":{} } // :33-37 ← plugins.entries.<id>.config 的 schema
}
```

Manifest 顶层字段（官方参考 `docs/plugins/manifest.md:216-283`，与频道相关的最小集）：

| 字段 | 必需 | 作用 |
|---|---|---|
| `id` | **是** | 插件规范 id；`plugins.entries.<id>` 用它 |
| `configSchema` | **是** | `plugins.entries.<id>.config` 的内联 JSON Schema（即使无配置也必须给 `{type:"object",additionalProperties:false,properties:{}}`，manifest.md:352） |
| `channels` | 频道插件需要 | 本插件拥有的 channel id 列表（发现+配置校验） |
| `channelConfigs` | 可选（频道配置必须） | `channels.<id>` 的预运行校验/UI 元数据；`channelConfigs.<id>.schema` 校验账号配置（channel doc:112-127） |
| `name` / `description` / `version` | 可选 | 展示/信息 |
| `categories` | 可选 | 目录分类 slug（1–3 个，主分类在前） |
| `contracts` / `activation` / `cliCommands` / `uiHints` 等 | 可选 | 见 manifest.md 顶表；非频道最小必需 |

> ⚠️ `channelConfigs.<id>.schema` 校验 `channels.<id>`（账号配置）；`configSchema` 校验 `plugins.entries.<id>.config`。
> 两者不可混用（channel doc:112-127）。

---

## 4. 入口导出形状（实测）

### 4.1 参照插件：手写 `register(api)`（`$P/index.ts:1-21`）

```ts
import type { OpenClawPluginApi } from "openclaw/plugin-sdk/plugin-entry";
export default {
  id: "openclaw-weixin",
  name: "WeChat",
  description: "...",
  configSchema: WeixinChannelConfigSchema,
  register(api: OpenClawPluginApi) {
    if (api.registrationMode === "cli-metadata") return;   // 元数据模式不做副作用
    assertHostCompatibility(api.runtime?.version);          // fail-fast 兼容性检查
    api.registerChannel({ plugin: weixinPlugin });          // ← 注册 channel
  },
};
```

### 4.2 官方推荐 helper：`defineChannelPluginEntry`（`openclaw/plugin-sdk/channel-core`）

官方文档 `docs/plugins/sdk-entrypoints/define-channel-plugin-entry.md`：helper 自动调用 `api.registerChannel({plugin})`（:19-21），字段：

| 字段 | 必需 | 说明 |
|---|---|---|
| `id` / `name` / `description` | 是 | 插件标识/展示 |
| `plugin` | 是 | `ChannelPlugin` 对象 |
| `configSchema` | 否 | 默认空对象 schema |
| `setRuntime` / `registerCliMetadata` / `registerFull` / `registerCapabilities` | 否 | 按 registration mode 门控（cli-metadata 不注册 channel；full 才跑运行时） |

> 对 `@im2a/openclaw-channel-relation`：**推荐 `defineChannelPluginEntry`**（官方 checklist 要求入口用 `defineChannelPluginEntry` 或 `definePluginEntry`，
> `docs/plugins/building-plugins.md:404-410`）。若需要与参照插件完全同构，手写 `register(api)` 也被实测宿主接受。

### 4.3 `ChannelPlugin` 对象实测字段（`$P/dist/src/channel.js:67-...`）

| 字段 | 实测（行号） | 说明 |
|---|---|---|
| `id` | `"openclaw-weixin"`（:68） | 与 manifest `channels` 一致 |
| `meta` | :69-78 `{id, aliases, label, selectionLabel, docsPath, docsLabel, blurb, order}` | 频道目录元数据（与 package.json#openclaw.channel 同构） |
| `configSchema` | :79 | 账号配置 schema |
| `capabilities` | :80-84 `{chatTypes:["direct"], media:true, blockStreaming:true}` | 能力声明；**不是权限系统** |
| `streaming` | :85-90 `{blockStreamingCoalesceDefaults:{minChars,idleMs}}` | 流式合并参数 |
| `messaging` | :91-96 `{targetResolver:{looksLikeId}}` | 目标 id 解析 |
| `agentPrompt` | :97-105 `{messageToolHints:()=>string[]}` | 给 Agent 的提示片段 |
| `reload` | :106 `{configPrefixes:["channels.openclaw-weixin"]}` | 配置变更重载前缀 |
| `config` | :107-121 `{listAccountIds,resolveAccount,isEnabled,isConfigured,describeAccount}` | 账号解析/状态（**同步**、预运行可用） |
| `outbound` | :122-194 `{deliveryMode:"direct", textChunkLimit:4000, normalizePayload, beforeDeliverPayload, sendText, sendMedia}` | 出站适配 |
| `status` | :195-216 `{defaultRuntime, collectStatusIssues, buildChannelSummary, buildAccountSnapshot}` | 状态面板 |
| `auth` | :217-300 `{login: async(...)}` | 登录/配对流程（可选） |
| `gateway` | :301+ `{startAccount: async(ctx)=>{...}}` | 传输层生命周期（长轮询/监听）；宿主按账号启动 |

> 说明：该对象**没有** `permissions` 字段；任务描述里的 "permissions" 若指权限模型，OpenClaw 频道插件是
> **capabilities（能力声明）+ 宿主 capability consent / trust**，不是插件自定义 permissions。`未验证`：是否存在其他可选
> 权限声明字段（本次未在类型定义中确认，故不写入建议）。

---

## 5. 安装与链接（CLI，官方文档）

`openclaw plugins install` 支持的来源（`docs/cli/plugins/install.md:48-63`）：

```bash
openclaw plugins install <package>                    # npm（默认）
openclaw plugins install clawhub:<package>            # ClawHub only
openclaw plugins install npm:<package>                # npm only
openclaw plugins install npm-pack:<path.tgz>          # 本地 npm-pack tarball
openclaw plugins install git:github.com/<owner>/<repo>
openclaw plugins install <path>                       # 本地路径/归档
openclaw plugins install -l <path>                    # link（不复制）
```

`-l/--link` 语义（install.md:307-315）：
- 要求本地路径已存在；**不会复制**，而是**加入 `plugins.load.paths`**（install.md:307-308）；
- 不支持与 `--marketplace`/`git:` 组合；非交互 link 需 `--force` 确认来源（不改变 link 模式）；
- 通过 `plugins.load.paths` 选中的本地副本（含 `--link`）**不继承官方包信任**；需要 trusted plugin state 的频道会拒绝启动（install.md:85-92）。

信任/安装策略（install.md:80-100）：
- ClawHub 包与 OpenClaw bundled/official catalog 属可信来源；
- 任意 npm/npm-pack/git/本地路径/marketplace 会警告并要求确认；非交互安装需 `--force`；
- `security.installPolicy` 与 capability consent 仍可能拦截。

安装后启用：`openclaw plugins enable <id>`（或配置 `plugins.entries.<id>.enabled=true`，manifest 之外的发现优先级见
`docs/plugins/manifest/package-json.md` 「Discovery precedence」）。

> 本次 Vultr 参照插件**不是** CLI 安装：目录 `/opt/openclaw-plugins/openclaw-weixin` + `plugins.load.paths` 手工挂载（§1）。
> 若要复刻"可管理安装"，建议对 `@im2a/openclaw-channel-relation` 用 `openclaw plugins install npm-pack:<tgz>` 或 `-l <path>`；
> 但**具体哪一种在宿主上更稳，需在目标 OpenClaw 版本上实测**（本机未验证 npm-pack 安装路径）。

---

## 6. ClawHub 分发要求（已知 / 未验证）

**已知**（官方离线文档可查）：
- 发布命令：`clawhub package publish <namespace>/<package> [--dry-run]`（`docs/plugins/sdk-provider-plugins.md:482-490`）；
  `clawhub skill publish` 是发布 skill，**不是插件包**（同文档 :491）。
- **ClawHub 发布必需**：`package.json` 的 `openclaw.compat.pluginApi` 与 `openclaw.build.openclawVersion`
  （`docs/plugins/sdk-provider-plugins.md:210-214`）。
- 插件搜索/安装：`openclaw plugins search "<q>"` 搜 ClawHub 的可安装 `code-plugin`/`bundle-plugin`（`docs/plugins/manage-plugins.md:123-124`）；
  `openclaw plugins install clawhub:<package>[@version|@beta]`（manage-plugins.md:228-231）。
- 信任：社区 ClawHub 安装会检查 release trust record，blocking 时拒绝安装（install.md:209 附近）。

**未验证（不要照抄，需在 ClawHub 上确认）**：
- 包命名规则（namespace 是否必须 = npm scope？`@im2a/...` 与 `im2a/...` 的映射）；
- README 必备章节、LICENSE/NOTICE 要求、`files` 白名单的强制程度；
- 发布认证（token/登录）、registry 端点、版本不可变策略、签名/校验和流程；
- `openclaw.build.pluginSdkVersion` 是否也必需（文档只点名 `openclawVersion`）。

---

## 7. 对照表：参照插件 vs `@im2a/openclaw-channel-relation`（字段级计划）

> 左列是**实测事实**；右列是**建议值**（供 task-35 建包；标 `待定` 的需产品确认）。

| 字段/文件 | `openclaw-weixin`（实测） | `@im2a/openclaw-channel-relation`（建议） |
|---|---|---|
| npm `name` | `openclaw-weixin` | `@im2a/openclaw-channel-relation` |
| `version` | `3.3.1` | `0.1.0`（首发） |
| `type` | `module` | `module` |
| `engines.node` | `>=22.22.3` | 跟随宿主实测版本（≥22.22.3） |
| `peerDependencies.openclaw` | `>=2026.6.1` | 与 `openclaw.compat.pluginApi` 同值（按实际构建宿主） |
| `openclaw.compat.pluginApi` | `>=2026.6.1` | 构建/测试所用宿主的下限（必需） |
| `openclaw.build.openclawVersion` | `2026.10.3` | 构建宿主版本（ClawHub 必需） |
| `openclaw.build.pluginSdkVersion` | 未声明 | 建议声明（待确认是否必需） |
| `openclaw.extensions` | `["./index.ts"]` | `["./index.ts"]` |
| `openclaw.runtimeExtensions` | `["./dist/index.js"]` | `["./dist/index.js"]` |
| `openclaw.channel`（package） | `{id, aliases, label, selectionLabel, docsPath, docsLabel, blurb, order}` | `{id:"im2a-relation", label:"IM2A Relation", blurb:"…", order:<未定>}` |
| `openclaw.install` | `{npmSpec, defaultChoice:"npm", minHostVersion}` | `{clawhubSpec:"<namespace>/openclaw-channel-relation", defaultChoice:"clawhub", minHostVersion}`（以 ClawHub 实名为准） |
| `files` | index/manifest/assets/dist/docs/README* /CHANGELOG*/LICENSE/NOTICE | `["index.ts","openclaw.plugin.json","dist/","assets/icon.png","README.md","LICENSE","NOTICE"]` |
| `openclaw.plugin.json` | 见 §3 | `id:"im2a-relation"`、`channels:["im2a-relation"]`、`channelConfigs["im2a-relation"].schema`（API base/token 等）、`configSchema` |
| 入口 | 手写 default + `register(api)` | **`defineChannelPluginEntry`**（官方推荐）；`plugin: relationChannelPlugin` |
| `ChannelPlugin` 最小字段 | id/meta/configSchema/config | 至少 `id`/`meta`/`configSchema`/`config`；按需 `outbound`/`gateway`/`status`；`security`/`pairing` 若复用聊天通道能力再加 |
| `capabilities` | `{chatTypes:["direct"],media:true,blockStreaming:true}` | 关系/付费触达首版建议只声明实际具备的（如 `chatTypes:["direct"]`）；**不虚报** |
| 依赖 | `qrcode-terminal` | 尽量 0 运行时依赖（IM2A 侧已有 HTTP client） |
| README/LICENSE/NOTICE | MIT + NOTICE（腾讯/上游） | LICENSE 与归属由 Lead 定；若移植参照代码需保留 NOTICE 并声明修改 |

---

## 8. 对照后的最小建包骨架（依据官方样例，可直接起步）

```
openclaw-channel-relation/
├── package.json           # §2 字段（含 openclaw block）
├── openclaw.plugin.json   # §3（id/channels/channelConfigs/configSchema）
├── index.ts               # defineChannelPluginEntry(...)
├── src/
│   ├── channel.ts         # ChannelPlugin 对象（createChatChannelPlugin 或手写）
│   └── client.ts          # 调 IM2A API
├── assets/icon.png
├── README.md
├── LICENSE
└── dist/                  # tsc 构建产物（runtimeExtensions 指向 dist/index.js）
```

官方 Quickstart 的最小 `package.json` + `openclaw.plugin.json` 样例见
`openclaw/docs/plugins/building-plugins.md:63-120`；频道版文件结构见
`openclaw/docs/plugins/sdk-channel-plugins.md:644-657`。

---

## 9. 未验证清单（不要在实现中假设）

1. `@im2a/...` 在 ClawHub 的命名映射与发布权限；
2. ClawHub README/LICENSE/NOTICE/files 的**强制**要求；
3. `defineChannelPluginEntry` 在 **2026.10.7** 的实际运行行为（本次只读源码/文档，未写插件实测）；
4. 外部插件用 `npm-pack` 安装 vs `plugins.load.paths` 挂载的差异（本机只观察到后者）；
5. 频道对象 `security`/`pairing`/`gateway` 对非聊天通道（关系/付费触达）是否必需（未验证）；
6. “permissions” 是否是 OpenClaw 频道插件的正式字段（本次类型定义中未见）。

---

## 10. 证据索引

| 结论 | 证据 |
|---|---|
| manifest 必须存在于插件根 | `openclaw/docs/plugins/manifest.md:20` |
| `channels` 字段标记频道 manifest | `openclaw/docs/plugins/sdk-channel-plugins.md:80-83` |
| manifest 最小/顶层字段 | `openclaw/docs/plugins/manifest.md:124-283`、`:352` |
| package.json vs manifest 分工 | `openclaw/docs/plugins/manifest/package-json.md:14-25`、`:35` |
| 入口 helper / registerChannel | `openclaw/docs/plugins/sdk-entrypoints/define-channel-plugin-entry.md:19-40` |
| 频道插件文件结构 | `openclaw/docs/plugins/sdk-channel-plugins.md:644-657` |
| 建包 checklist | `openclaw/docs/plugins/building-plugins.md:26-44,404-410` |
| install 来源 / --link | `openclaw/docs/cli/plugins/install.md:48-63,307-315` |
| ClawHub 发布命令/必需字段 | `openclaw/docs/plugins/sdk-provider-plugins.md:210-214,482-491` |
| 真实 package.json | `/opt/openclaw-plugins/openclaw-weixin/package.json:2-115` |
| 真实 manifest | `/opt/openclaw-plugins/openclaw-weixin/openclaw.plugin.json:2-37` |
| 真实入口导出 | `/opt/openclaw-plugins/openclaw-weixin/index.ts:1-21` |
| ChannelPlugin 字段 | `/opt/openclaw-plugins/openclaw-weixin/dist/src/channel.js:67-330` |
| 实际加载方式 | `/root/.openclaw/openclaw.json:2-10` + `openclaw plugins list` 输出 |
