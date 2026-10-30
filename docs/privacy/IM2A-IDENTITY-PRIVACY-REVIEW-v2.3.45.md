# IM2A 识别码体系 · 隐私影响评审（v2.3.45）

| 项 | 值 |
|---|---|
| 评审对象 | IM2A 识别码体系：公开用户码 / IM2A 号码、跨端身份关联、Agent 子码、历史与记忆 |
| 目标仓库 | `E:\GPT\im2a-v2.3.45`（平台 v2.3.45 / 协议 / 发布门禁 / 客户端壳） |
| 实现基线 | 网关 `version-matrix.json` 固定的 `gateway` Go `2.3.26`，源码位于 `E:\GPT\im2a-work\im2agent`（下称 `gateway/`） |
| 评审方式 | 只读静态分析：路由表、迁移 schema、身份/记忆/删除管线代码、发布门禁清单 |
| 状态 | 建议（未通过发布门禁：见 §9） |
| 敏感信息 | 本文不引用任何真实凭证、密钥、令牌值；仅引用环境变量名与代码位置 |

> **为什么同时看两个仓**：`im2a-v2.3.45` 只承载协议 schema、插件壳与发布证据，识别码的生成、搜索、鉴权、删除逻辑全部在网关仓。只看 v2.3.45 会得出"没有识别码实现"的错误结论；只看网关会漏掉"这些码如何被插件与端侧消费"。两条证据链必须合起来读。

---

## 0. 结论摘要

1. **最严重的问题不是"码可被搜到"，而是"码同时是凭证"。** `users.identifier` 既是用户可见的公开身份，又是 `POST /v1/auth/login` 的全部鉴权材料——知道码即等于拿到该账号的 JWT。公开性与机密性被压在同一字段上，这是识别码体系的根缺陷（F-01，Critical）。
2. **"Agent 子码 + 记忆"的隔离写在代码里，却在 API 边界被抵消。** `memory.GuardedStore` 有正确的跨用户拒绝规则，但 HTTP 层把调用者 `userId`/`agentId` 全部取自 query/body，从不与 JWT `sub` 比对，也不做存在性校验（F-03，Critical）。
3. **删除管线真实存在，但覆盖不完整，且不记录"保留了什么、依据是什么"。** `privacy.Erase` 覆盖 24 张表，遗漏 `messages` / `im_messages` / `user_keys` / `encrypted_context` / `l4_subjects` / `wechat_identity` 等 12+ 张个人数据表；DSR 记录只写删除计数，不写法定保留项（F-07，High）。
4. **没有保留期自动化。** 唯一带 TTL 的是 `working_memory.expires_at`，而它只在有人调用 `/v1/memory/working/gc` 时才回收；`episodes` / `facts` / `messages` / `security_events` 均无到期机制（F-08，High）。
5. **枚举面被低估。** 登录端点是最强的用户存在性预言机（存在则 200 + token，不存在则建号后 200），`/v1/directory/agents?owner=` 与 `/v1/harness/sessions` 直接按属主/在线状态列举（F-09，Medium-High）。
6. **鉴权码是共享的、无主体的。** `GATEWAY_TOKEN` 静态令牌放行时**不注入身份**（`auth.Authenticate`），MCP 桥、浏览器扩展、所有用户共用一枚令牌；`ResolveCallerUID` 在无 JWT 时回退到调用方自报的 `user_id`（F-02，High）。
7. **隐私默认值整体偏"可用优先"**：空令牌即放行匿名、broadcast 为默认投递方式、Agent 子码默认可按属主列举、记忆默认永久（§6）。
8. **发布门禁里没有隐私项。** 九门禁（commit / tag / Release / npm / CI / Compat / Release workflow / npm-publish / healthz）不含数据流、删除、留存证据（F-12，Process）。
9. **正面样板已经存在**：`dsh-session-ref` schema 把"关联码"做对了——`scope`、`expiresAt`、`revoked`、`crossOrg` 固定为 `false`。识别码体系应当照抄这个模型，而不是另起一套（§4）。

---

## 1. 评审范围、方法与边界

### 1.1 在范围内

| 域 | 具体对象 | 证据位置 |
|---|---|---|
| 公开码 | `users.identifier`（唯一索引）、登录语义 | `gateway/internal/api/auth.go`、`gateway/internal/store/migrations/016_auth.sql` |
| 鉴权码 | 静态 `GATEWAY_TOKEN`、JWT `sub`、admin token、MCP bearer | `gateway/internal/auth/identity.go`、`im2a-v2.3.45/packages/im2a-dsh-plugin/cordis.patch.yml` |
| 关联码 | `dsh-session-ref`、`devices`、`ticket_state`、`l4_subjects`、`user_keys.wxid` | `im2a-v2.3.45/packages/protocol/schemas/session-ref.schema.json`、`gateway/internal/api/multidevice.go` |
| Agent 子码 | `agent_directory`、`agent_registry`、信任档默认值、按属主列举 | `gateway/internal/api/directory.go`、`gateway/internal/store/migrations/012_agent_directory.sql` |
| 历史 | 关系、消息、已读回执、群成员 | `gateway/internal/api/im.go`、`gateway/internal/kernel/routes/routes_im.go` |
| 记忆 | working / episodic / semantic 三层、跨 Agent 授权、导出导入 | `gateway/internal/memory/guard.go`、`gateway/internal/api/memory.go`、`002_v1.sql` |
| 删除与权利 | DSR 导出/删除/查询、DSR 记录 | `gateway/internal/privacy/privacy.go`、`routes_admin.go:60-101`、`015_data_subject_requests.sql` |
| 发布与默认值 | 插件配置、门禁清单、版本矩阵 | `im2a-v2.3.45/release-manifest.json`、`version-matrix.json`、`packages/im2a-dsh-plugin/*.yml` |

### 1.2 不在范围内（明确列出，避免被当作已覆盖）

- 生产数据库实例、备份存储、对象存储、日志聚合平台的实际内容（无访问权，未采样）
- 腾讯云 IM / 推送 / 微信 等第三方控制台的**实际**开启项与 DPA 文本
- 运行时的抓包与渗透测试（本次为静态分析；验收项中给出可执行的动态验证方法）
- 真实凭证、密钥、租户 ID 的取值

### 1.3 判定口径

- 严重度：**Critical** = 可直接导致大规模个人数据非授权访问或伪造；**High** = 违反法定权利实现或跨境/保留义务；**Medium** = 可被滥用但需要额外条件；**Process** = 流程/门禁缺口。
- 每个发现必须给出：证据（文件:行）→ 影响 → 控制 → **可执行验收**。无验收项的结论视为无效。

---

## 2. 识别码体系建模：四类码必须分开治理

当前实现里所有"码"都共用同一批字段与同一套鉴权语义，这是问题的结构性来源。评审建议把它们拆成四类，每类有独立的可见性、生命周期与鉴权规则。

| 类型 | 用途 | 允许被搜索 | 可作鉴权材料 | 生命周期 | 现有实现 |
|---|---|---|---|---|---|
| **A. 公开码**（handle / IM2A 号码） | 被人找到、加好友、展示 | 可（受策略与配额限制） | **绝不可** | 可轮换、可退役 | ❌ 与登录复用 `identifier` |
| **B. 鉴权码**（token / session / key） | 证明"我是谁" | 不可 | 是 | 短期、可撤销 | ⚠️ 静态共享令牌 + JWT，无主体绑定 |
| **C. 关联码**（跨端 / 跨会话引用） | 把两处记录指向同一主体 | 不可 | 不可（仅引用） | 有 `scope`、有 TTL、可撤销 | ✅ `dsh-session-ref` 已做对 |
| **D. 子码**（Agent 子身份） | 区分同一主体的多个 Agent | 默认不可 | 不可 | 随属主，可停用 | ⚠️ 按属主可列举、无可见性分级 |

### 2.1 现状映射表（证据）

| 码 | 存储 | 可见性现状 | 鉴权现状 | 风险 |
|---|---|---|---|---|
| `users.identifier` | `users.identifier`，partial unique index | 无字段级可见性策略；作为"用户码"对外 | **是**：`POST /v1/auth/login` 唯一入参 | F-01 |
| `GATEWAY_TOKEN` | 环境变量 / MCP 配置 | 不出网（正确） | 是，但**不注入主体** | F-02 |
| JWT `sub` | 签发于登录 | 不出网 | 是（`RequireIdentity`） | 好，但下游不消费 |
| `dsh-session-ref` | 协议 schema | 不透明串 | 明确声明"不是 bearer token" | ✅ 样板 |
| `agent_directory.agent_id` | 表 + `GET /v1/directory/agents?owner=` | **按属主可列举** | 否（但可被用于冒充上下文） | F-06 |
| `devices.id` / `devices.user_id` | 客户端生成字符串，服务端 upsert | — | 否 | F-05 |
| `l4_subjects.wxid` | 微信身份 → 稳定整数 | 否 | 否 | F-07（删除遗漏） |
| `user_keys.wxid` | 公钥身份 + 服务端加密私钥 | 否 | 间接（解锁私有数据） | F-14 |

---

## 3. 关键发现

### F-01 · 公开码即登录凭证（Critical）

**证据**
- `gateway/internal/store/migrations/016_auth.sql`：`ALTER TABLE users ADD COLUMN IF NOT EXISTS identifier TEXT;` + `CREATE UNIQUE INDEX ... ON users(identifier)`。
- `gateway/internal/api/auth.go:53-84`：`AuthLogin` 只接收 `{"identifier": "..."}`，`upsertUserByIdentifier` 查不到就**创建**用户，然后 `auth.Issue(secret, uid, ttl, "im2a")` 直接签发 72 小时 JWT。
- `gateway/internal/kernel/routes/routes_core.go:80-84`：该路由是**公开**的，只有 IP 限流（20 rps / burst 100），无验证码、无密码、无设备绑定、无二次因子。

**影响**
1. 任何知道他人公开码的人，可在 20 rps 内直接取得该账号的完整 JWT（不是"猜密码"，是"报码即登录"）。
2. 一旦公开码被设计成"可被搜索"（产品目标），攻击面从"知道码的人"扩大到"能枚举码的人"——而码通常是数字/短串，枚举成本极低。
3. 删除后账号被匿名化（`users.status=2`），但 `identifier` 未被清空，**已注销账号的码仍可被"登录"复活**（`upsertUserByIdentifier` 命中旧行直接返回原 id）。这是"删除不彻底"的一种隐蔽形态。
4. 审计上无法区分"本人登录"和"报码登录"——两条路径签发的 token 完全一致。

**控制**
- 公开码与鉴权码彻底分离（见 §4.1）：登录不得以公开码为唯一凭据。
- 过渡期最小可行：`/v1/auth/login` 在生产环境（`APP_ENV=prod`）默认 **404 或 403**，仅 dev/staging 开放；开放时必须叠加：一次性验证码 / 设备绑定 / 速率按 `identifier` 维度（不是仅 IP）。
- 注销后 `identifier` 置 NULL 并进入退役表，禁止复用与复活。

**验收**
- `[AC-01]` 以任意合法 `identifier` 单独调用登录，期望 `401`（生产构建）。测试：`curl -X POST /v1/auth/login -d '{"identifier":"<任意>"}'` → 断言 `status != 200` 且响应体不含 `access_token`。
- `[AC-02]` 静态断言：`auth` 包与登录 handler 不再引用 `users.identifier` 作为凭证来源（`grep -rn "identifier" gateway/internal/auth gateway/internal/api/auth.go` 仅剩展示用途，且每处带 `// public-handle, never auth` 注释）。
- `[AC-03]` 删除用户后重放登录：`POST /v1/privacy/delete?user_id=U` → `POST /v1/auth/login {"identifier": "<U 的原码>"}` → 断言返回的是**新用户**（新 id）或直接拒绝，不得返回原 U 的 id。
- `[AC-04]` 登录限速按主体维度生效：对同一 `identifier` 连续 6 次请求，第 6 次起 `429`；换 IP 不解除。

---

### F-02 · 静态共享令牌放行匿名主体，且横向校验可回退（High）

**证据**
- `gateway/internal/auth/identity.go:59-81`：`Authenticate` 在 `tok == staticToken` 时 `next.ServeHTTP(w, r)`——**不调用 `WithUserID`**。即静态令牌请求在业务层看来是"无身份"。
- 同文件 `62-66`：`staticToken == ""` 且请求无 token 时**直接放行**（开发态默认）。
- `gateway/internal/api/auth.go:99-110`：`ResolveCallerUID` 在无 JWT 身份时 `return queryUID, nil`——注释自认"向后兼容，逐步淘汰"。
- `gateway/internal/kernel/routes/routes_harness.go`：`/v1/harness/` 走 `c.wrap`；注释写明"MCP/浏览器扩展使用同一令牌"。
- `im2a-v2.3.45/packages/im2a-dsh-plugin/cordis.patch.yml:22`：`IM2A_HARNESS_TOKEN: !!js process.env.IM2A_HARNESS_TOKEN`——一枚令牌服务整个宿主。

**影响**
- 任何持有该令牌的组件（MCP 桥、浏览器扩展、任何拿到配置的进程）在语义上等于"所有用户"。审计只能记录"有人用了令牌"，不能记录"谁访问了谁"。
- 结合 F-03/F-04，静态令牌 + query 自报身份 = 全量横向读取。
- `staticToken == ""` 即放行，是一个"配置漏填即无认证"的失败开放（fail-open）默认。

**控制**
- 生产构建**禁止**静态令牌放行到业务路由；`GATEWAY_TOKEN` 仅用于服务间健康检查等无个人数据的端点。
- `staticToken == ""` 时，生产环境必须**拒绝启动**（fail-closed），而不是放行。
- 所有涉及个人数据的路由必须要求 JWT 主体；静态令牌命中时直接 `403 subject_required`。
- 令牌按用途拆分：MCP 令牌、扩展令牌、服务令牌各自独立、可单独撤销、带 scope。

**验收**
- `[AC-05]` 以静态令牌访问 `/v1/memory/facts?userId=1&agentId=butler` → `403`。
- `[AC-06]` `APP_ENV=prod` 且 `GATEWAY_TOKEN` 为空时进程启动失败并以非零码退出（检查启动日志含 `refuse to start: empty gateway token`）。
- `[AC-07]` 令牌清单可审计：`/v1/admin/tokens` 返回每个令牌的 `name/scope/lastUsedAt/createdAt`，不含令牌明文。
- `[AC-08]` 撤销 MCP 令牌后，MCP 桥所有工具调用返回 401，且不影响扩展令牌。

---

### F-03 · 记忆 API 用 query 自报主体，绕过 `GuardedStore`（Critical）

**证据**
- `gateway/internal/memory/guard.go:97-117`：`check()` 规则正确——跨 `userID` 一律 `AccessNone`，master 可读本用户全部子 Agent。
- `gateway/internal/api/memory.go:87-108`：`GET /v1/memory/facts` 直接 `uid := ParseUint(Query().Get("userId"))`、`agent := Query().Get("agentId")`，构造 `scopeOf(uid, agent)` 后调用 `ListFacts` / `SearchFacts`。
- `guard.go:214-216`：`ListFacts` **没有** `g.need(...)` 校验，直接把 caller scope 传给内层存储——内层按 scope 过滤，所以"读谁的"完全由 query 决定。
- `memory.go:255-272`：`GET /v1/memory/cross/facts?callerUserId=&callerAgent=&targetUserId=&targetAgent=`——**调用者身份也是入参**，`check()` 比较的是两个入参，恒等成立。
- `memory.go:275-288`：`GET /v1/memory/export?userId=&agentId=` → `ExportUser`，只要 `agentId` 填成该用户的 master 即通过。
- `memory.go:74-86`：`GET /v1/memory/search?q=` 硬编码 `Scope{UserID: 1, AgentID: "butler"}`——任何通过认证的调用者都能读到 1 号用户的语义记忆（演示残留上生产即为数据泄露）。

**影响**
- 跨用户读取他人长期记忆（事实、情景、工作记忆）与全量记忆导出，且这是识别码体系里最敏感的一类内容（偏好、关系、历史决策）。
- `GuardedStore` 提供了"安全在代码层"的错觉，评审若只读 `memory/` 包会误判为已控制。
- 由于主体来自 query，任何日志/审计都无法证明"调用的确实是本人的数据"，事后追责链断裂。

**控制**
- 主体只从认证上下文派生：`uid, ok := auth.UserIDFromContext(r)`；缺失即 `401`。
- 禁止任何 handler 从 query/body 读取 `userId` / `callerUserId` / `owner` 作为**授权依据**；如需支持管理员代查，必须走独立的管理路由 + 独立令牌 + 强制 `reason` 字段。
- `agentId` 可以做参数，但必须校验其属于该 `uid`（查 `agent_directory.owner_user`）。
- `guard.go` 中 `ListEpisodes / GetFact / SearchFacts / ListFacts` 补 `g.need(caller, target, AccessRead)`，与写路径对称。
- 删除 `memory.go:74-86` 的硬编码 scope 演示端点。

**验收**
- `[AC-09]` 用户 A 的 JWT 调 `GET /v1/memory/facts?userId=<B>&agentId=<任意>` → `403`（即使 B 的 agentId 正确）。
- `[AC-10]` `GET /v1/memory/export?userId=<B>&agentId=<B 的 master>` → `403`。
- `[AC-11]` `GET /v1/memory/cross/facts?callerUserId=<B>&callerAgent=<B>` → `403`（caller 必须等于 JWT sub）。
- `[AC-12]` 静态扫描门禁：`grep -rn 'Query().Get("userId")\|Query().Get("owner")' gateway/internal/api` 命中数归零，或每处带豁免注释 + 安全评审链接。
- `[AC-13]` 回归测试覆盖：新增表驱动测试，对 `memory` 全部 14 个路由逐一断言"A 不能读 B"。

---

### F-04 · IM 关系与历史同样以 query 指定主体（High）

**证据**
- `gateway/internal/kernel/routes/routes_im.go:26`：`auth.RequireToken(imH, c.token)`——**只校验静态令牌**，连 JWT 都不解析。
- `gateway/internal/api/im.go:399-433`：`GET /v1/im/relations?owner=<任意>` 直接返回该 owner 的全部关系链。
- `im.go:551-574`：`GET /v1/im/messages?user_id=<任意>&peer=<任意>` 返回双向会话最近 100 条。
- `im.go:578-593`：`GET /v1/im/unread?user_id=<任意>`。
- `im.go:436-464`：`POST /v1/im/relations` 的 `owner`/`peer` 全部来自 body。

**影响**
- 关系链本身是高敏感图数据（谁和谁是家人/信任/陌生），可被整表枚举；消息历史即内容数据。
- 与 F-02 叠加：一枚共享令牌 = 全体用户的关系与聊天记录。

**控制**
- `/v1/im/*` 改为 `RequireIdentity`（JWT），`owner`/`user_id` 从 `sub` 派生；peer 参数允许，但必须在本人关系链内。
- 提供分页与总量上限，禁止无界导出关系链。
- 关系链读取记入 `security_events` 并带 actor。

**验收**
- `[AC-14]` 仅持静态令牌调 `/v1/im/relations?owner=X` → `401/403`。
- `[AC-15]` A 的 JWT 调 `?owner=B` → `403`。
- `[AC-16]` 单次响应最多 200 条；无 `cursor` 时按时间倒序且返回 `nextCursor`。

---

### F-05 · 设备注册可改写归属（跨端身份劫持）（High）

**证据**
- `gateway/internal/api/multidevice.go:16-43`：`POST /v1/device/register` 的 `DeviceID` 与 `UserID` 均来自 body，无任何身份校验；
- `gateway/internal/kernel/routes/routes_ops.go:39-41`：三条路由均挂在 `c.wrap` 下——即"已认证但未绑定主体"（静态令牌即可通过，JWT 主体被 handler 忽略）。
- SQL：`INSERT INTO devices(id,user_id,type,last_seen) ... ON CONFLICT (id) DO UPDATE SET user_id=$2`——**同一设备 ID 可把归属改到任意用户**。
- `multidevice.go:132-143`：`POST /v1/sync/state` 的 `userId` 同样来自 body，直接决定 `ticket_state` 归属与 SSE 广播目标。

**影响**
- 跨端身份关联的核心映射（设备 → 用户）可被第三方改写：攻击者注册一个已被受害者使用的 `deviceId`，即可把该设备的后续同步、审批状态、SSE 推送导向自己，或污染受害者的审批终态（LWW 时间戳由客户端提供，见 `applyTicketDecision` 的 `clientTS`）。
- 审批类数据（谁批准了什么）被篡改属于完整性问题，但其数据主体是真实用户，因此也是隐私事件。

**控制**
- 设备绑定必须由**已认证主体**发起，`user_id` 从 JWT 派生；`deviceId` 由服务端签发（或客户端生成 + 服务端签名绑定）。
- `ON CONFLICT` 时若 `user_id` 不同 → `409 device_already_bound`，要求先解绑（解绑需原主体身份或双端确认）。
- 首次绑定走一次性绑定码（短 TTL、单次使用、绑定即失效），并记审计。
- 设备表增加 `bound_at` / `last_seen` / `revoked_at`，支持"查看并踢出我的设备"。

**验收**
- `[AC-17]` A 的 JWT 注册 `deviceId=D`（D 已属于 B）→ `409`；B 的 `devices` 行不变。
- `[AC-18]` 未认证请求 `/v1/device/register` → `401`。
- `[AC-19]` 用户在 `/v1/devices` 能看到自己的全部设备与最近活跃时间；撤销后该设备 JWT 失效（或降级为只读）。
- `[AC-20]` `ticket_state` 的 `clientTS` 改为服务端单调时钟 + 客户端时钟仅作提示，防止时间戳伪造获胜。

---

### F-06 · Agent 子码按属主可列举，缺可见性分级（Medium-High）

**证据**
- `gateway/internal/kernel/routes/routes_relation.go:10`：`/v1/directory/` 挂在 `c.wrap` 下（认证存在，但无属主校验）。
- `gateway/internal/api/directory.go:32-44`：`GET /v1/directory/agents?owner=<任意>` 返回该 owner 全部 Agent（含 `display_name`、`trustLevel`、`attentionLevel`）。
- `directory.go:45-52`：`GET /v1/directory/agents/{id}` 无需属主关系即可按 id 取单个 Agent。
- `directory.go:23-25`：新建 Agent 默认 `TrustLevel = 4`（陌生档）——这是**正确的最小权限默认**，但没有任何字段控制"这个子码是否对陌生人可见"。
- `agent_id` 与属主存在可推导关系（`wechat_nego.go:115-117`：由 `accountID` 截断派生），因此"知道属主 → 可枚举/猜测子码"。

**影响**
- 子码是"这个人在平台上养了哪些 Agent、分别负责什么"的画像；按属主列举等于把画像对外开放。
- `display_name` 常含业务含义（"法务助手""记账"），属于可推断个人情况的间接标识。

**控制**
- `agent_directory` 增加 `visibility`（`private` 默认 / `link` / `public`）与 `share_token`（`link` 可见时使用，可撤销、可设 TTL）。
- 列表接口改为"仅返回调用者自己拥有的 Agent"；他人 Agent 只能通过显式 `share_token` 或已建立关系获取，且只返回白名单字段。
- `agent_id` 生成改为不可推导（随机 + 校验位），停止由账号 ID 派生。

**验收**
- `[AC-21]` A 的 JWT 调 `?owner=B` → 返回空列表或 `403`，且响应中不含任何 B 的 Agent 名称。
- `[AC-22]` 新建 Agent 默认 `visibility=private`；未显式公开时，第三方按 id 查询返回 `404`（不是 `403`，避免存在性泄露）。
- `[AC-23]` `share_token` 撤销后立即失效；TTL 过期返回 `410`。
- `[AC-24]` 抽样 1000 个 agent_id，无法从 id 反推属主（人工评审 + 生成算法单测）。

---

### F-07 · 删除覆盖不完整，且不记录法定保留项（High）

**证据**
- `gateway/internal/privacy/privacy.go:69-94` 的 `erasureSources` 覆盖 24 张表，并在 `169-217` 事务内删除 + 匿名化 `users`。
- 对比 `gateway/internal/store/migrations/` 的全部建表语句，**未覆盖**（含个人数据）：
  `messages`(002)、`im_messages`(008)、`im_group_messages`(004)、`temp_messages`(022)、`inbound_events`(032)、`im_webhook_events`(003a)、`wechat_identity`(019)、`wechat_bindings`(018)、`wechat_relay_messages`(026)、`bot_route_messages`(023)、`user_keys`(020)、`encrypted_context`(017)、`l4_subjects`(034)、`agent_registry`(033)、`agent_reports`(024)、`support_ticket_replies`(009)、`agent_quota_usage`(019)、`negotiation_sessions/turns`(001)、`a2a_envelopes`(011)、`content_moderation_queue`(010)。
- `privacy.go:197-203`：`users` 只清 `phone_encrypted` 并置 `status=2`，**`identifier` 保留**（配合 F-01 可复活）。
- `privacy.go:205-212`：DSR 记录只写各表删除行数；`retained`（法定保留：审计/账本/外发消息）**没有任何结构化记录**，仅在代码注释里。
- `routes_admin.go:60-101`：DSR 仅管理员可用，无用户自助入口、无 SLA 计时、无完成/驳回状态机（`status` 恒为 `completed`）。

**影响**
- 用户行使删除权后，其聊天内容（`messages.text`）、微信身份映射、加密上下文、记忆命名空间映射仍然在库；对外声明"已删除"即构成不实陈述。
- 审计/账本保留本身合法（等保 6 个月、财税凭证），但"哪些保留、依据哪条、保留到何时"没有进入记录 → 无法向监管证明是"依法保留"而非"删漏了"。
- 备份未纳入：恢复备份会让已删除数据复活，且无 tombstone 重放机制。

**控制**
- 补全 `erasureSources`，并对**确实需要保留**的表建立 `retainedSources`（表、字段、法律依据、到期日、最小化处理方式）。
- 保留类数据做**字段级最小化**：`messages` 至少将 `sender` 替换为不可逆 pseudonym、`text` 按策略清空或加密丢弃密钥；`user_keys` 删除私钥密文（保留公钥骨架不影响对账）。
- `users.identifier` 在删除时置 NULL 并写入 `retired_handles`（含 `retired_at`、原因），禁止复用。
- DSR 记录增加：`retained: [{table, columns, basis, retainUntil, minimization}]`、`backupTombstone: {issuedAt, effectiveFrom}`、`requestedAt/completedAt`、`slaDeadline`、`operator`。
- 备份策略：写入 tombstone 表 → 恢复时重放 tombstone → 验证扫描。

**验收**
- `[AC-25]` **覆盖契约测试**：解析 `migrations/*.sql` 得到全部表，与 `erasureSources ∪ retainedSources ∪ exemptSources(白名单+理由)` 求差集；差集非空即测试失败。新增表必须显式归类。
- `[AC-26]` 删除后扫描：对用户 U 执行删除，随后在全部表中以 U 的标识符（identifier、wxid、device id、user id、手机号密文）检索，**命中数 0**（除 `retainedSources` 声明的表与列）。
- `[AC-27]` DSR JSON 包含 `retained` 数组且每项含 `basis` 与 `retainUntil`；缺字段则契约测试失败。
- `[AC-28]` 备份恢复演练：删除 U → 触发一次全量恢复 → 再次扫描 → 仍为 0 命中，且 DSR 记录中有 `backupTombstone`。
- `[AC-29]` 复活的码不能登录（见 AC-03）。

---

### F-08 · 无保留期自动化（High）

**证据**
- `002_v1.sql`：`working_memory.expires_at` + 部分索引（设计正确），但回收靠 `POST /v1/memory/working/gc`（`memory.go:399`）按 scope 手动触发。
- `episodes` / `facts` / `messages` / `im_messages` 无 `expires_at`、无分区、无归档策略。
- `security_events`（`014_security_events.sql`）记录 `path`、`client_ip`、`user_agent`、`actor`，无保留上限；`audit_events` 同理。
- 全仓检索无 retention/TTL 定时任务（仅 WS 连接清理与 runtime GC）。

**影响**
- "目的消失即删除"无法执行；数据只增不减，随时间成为最大负债。
- IP + UA 属于可识别信息，永久保留在 `security_events` 中，与最小化原则冲突。

**控制**
- 建立 `retention` 包 + 每日调度（cron），声明式策略表：`(table, subjectColumn, ttl, action=delete|anonymize|archive, legalHold)`。
- 建议初始值（需法务/DPO 确认，此处为工程建议）：
  | 数据 | TTL | 动作 |
  |---|---|---|
  | `working_memory` | 24h（已有字段） | delete |
  | `episodes` | 180d | delete（或聚合后 delete） |
  | `facts` | 365d 无强化 | delete（`DecayConfidence` 已有衰减基础） |
  | `im_relations`（陌生档） | 180d 无互动 | anonymize → 降档 |
  | `security_events` | 180d | 聚合后 delete（保留计数与类型） |
  | `audit_events` / `paid_message_ledger` | 法定期限（等保 ≥6 个月，财税按规） | archive + 最小字段 |
  | `data_subject_requests.result` | 导出内容 30d | 清空 `result`，保留请求元数据 |
- 法务保留（legal hold）必须显式标注，不能靠"忘了删"实现。

**验收**
- `[AC-30]` 造 400 天前的 `episodes`、2 年前的 `security_events`，运行 retention sweep，对应行数归零且审计留一条 `retention_sweep` 记录（含删除计数）。
- `[AC-31]` `working_memory` 过期行在 24 小时内被自动清除（无需人工调 GC）。
- `[AC-32]` 策略表覆盖率：每张含时间列的表都在策略表中出现或被标记 `legalHold`；CI 契约测试保证。
- `[AC-33]` sweep 幂等：连续运行两次，第二次删除计数为 0。

---

### F-09 · 枚举与爬取防护不足（Medium-High）

**证据**
- `routes_core.go:80-84`：登录端点限流按 **IP**（20 rps），不限 `identifier` 维度；且响应可区分"存在"与"不存在"以外的更多信息（返回 `user_id` 自增整数，暴露注册顺序与规模）。
- `directory.go:32-44`：按 `owner` 列举 Agent，无分页上限、无配额。
- `im2a-http-mcp/server.mjs:188-190`：`im2a_presence` 直接返回网关 `/v1/harness/sessions`——在线会话列表。
- `im2a-http-mcp/server.mjs:208-216`：`im2a_send` 的 `to` 缺省为 `broadcast`，消息投递给所有在线会话。

**影响**
- 公开码一旦可搜索，攻击者可低成本建立"码 → user_id → 画像"的映射库；`user_id` 递增本身泄露用户规模与注册时序。
- presence 与 broadcast 让"谁在线、有哪些 Agent 在跑"成为公开信息，属于跨租户的元数据泄露。

**控制**
- 登录：按 `identifier` 维度限速 + 指数退避 + 验证码；存在与不存在返回**同构响应**（同状态码、同耗时带、同 body 形状，不含 `user_id`）。
- 对外标识一律使用**不可枚举的随机码**（如 12 位 base32 + 校验位），内部自增 id 不出网。
- 目录/关系/搜索类接口：强制分页、单主体配额（如每用户每日 200 次查询）、异常模式检测（同一令牌高频枚举不同主体 → 熔断 + 告警）。
- presence 默认只返回同租户/同 scope 的会话；`broadcast` 改为**显式 opt-in**，默认 `to` 必填。

**验收**
- `[AC-34]` 以 1000 个随机码探测登录接口，`200` 比率为 0（全部拒绝），P95 响应时差与基线 < 10%。
- `[AC-35]` 对同一码 6 次/分钟后返回 `429`，且换源 IP 不解除。
- `[AC-36]` 响应体中不出现自增 `user_id`（替换为 `subject_ref`，随机、每主体稳定、不可反推）。
- `[AC-37]` 单个令牌 1 分钟内枚举 50 个不同 `owner` 时触发熔断（`429` + 告警事件）。
- `[AC-38]` 未指定 `to` 调用 `im2a_send` → `400`（不再默认 broadcast）。

---

### F-10 · 可见性与传播边界（Medium）

**证据**
- `im2a-http-mcp/server.mjs:208-216`：broadcast 默认投递。
- `routes_core.go:71-75`：`/v1/events` 返回 `deps.Bus.List()` 全量事件（含 `memory.Fact` payload，见 `guard.go:200-202` 的 `publish`——事实的 `key/value` 进入事件总线）。
- `/v1/harness/` 与 `/v1/im/` 共享同一令牌（见 F-02）。

**影响**
- 记忆事实（可能含健康、财务、关系信息）通过事件总线传播到任何能读 `/v1/events` 的组件；这不是"日志"，是数据面。
- "谁能看到我的 Agent 在做什么"缺一个可被用户理解的开关。

**控制**
- 事件总线按 `user_id` + `agent_id` 分区，订阅方必须声明 scope；`/v1/events` 只返回调用者 scope 内事件。
- 记忆事件默认只发 `key` 的哈希与事件类型，**不发 value**；需要 value 的订阅方走受控读接口。
- 面向用户提供"可见性中心"：公开码是否可搜索、子码是否可被发现、历史是否可被 Agent 检索，三个开关默认关闭/最小。

**验收**
- `[AC-39]` A 的 JWT 调 `/v1/events` 不返回 B 的任何事件（含 `userId` 字段过滤断言）。
- `[AC-40]` 事件 payload 中不含 `facts.value` 明文（测试：写入一条 value=`"<哨兵串>"`，订阅断言哨兵串不出现，仅 `valueHash`）。
- `[AC-41]` 关闭"历史可被 Agent 检索"后，Agent 侧检索接口返回空且记录一条策略拒绝审计。

---

### F-11 · 跨境与第三方数据流缺乏清单与控制（High，合规）

**证据**
- `kernel.go:470,489`：`model.NewOpenAICompatible(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL"))`——模型端点由环境变量决定，**代码层无区域/域名白名单**。
- `kernel.go:534`：推送客户端 `SDKAppID` **硬编码**在源码中（值不在此引用），端点由配置决定。
- `routes_im.go`：人类 IM 走腾讯云 PaaS（webhook 回调）。
- 全仓无出境数据清单、无 DPA 登记、无"模型调用是否携带记忆/历史"的开关。

**影响**
- 若 `LLM_BASE_URL` 指向境外端点，记忆、历史、会话内容即构成出境；PIPL 第 38-39 条要求单独同意/安全评估/标准合同之一。
- 硬编码的第三方应用标识使"换租户/换环境"必须改代码，配置漂移会导致测试数据流入生产第三方账号。

**控制**
- 出境数据流清单（表）：字段 → 接收方 → 国家/地区 → 传输方式 → 法律依据（安全评估/标准合同/单独同意）→ 保留期 → 是否可关闭。
- 模型调用做**数据最小化 + 脱敏**：默认只发当前轮次必要上下文；记忆/历史默认**不外发**，需显式开启并记录。
- egress 白名单：仅允许清单内域名；`LLM_BASE_URL` 变更需走配置审计。
- 硬编码第三方标识移入配置，并在启动时校验与 `APP_ENV` 匹配。
- 为每个第三方建立 DPA 台账与删除 API 对接（删除请求要扇出到第三方）。

**验收**
- `[AC-42]` 出境清单存在且被 CI 校验：每个 `LLM_BASE_URL` / 渠道端点 / 推送端点都在清单中出现，否则构建失败。
- `[AC-43]` 关闭"允许外发记忆"后，构造含哨兵串的记忆，抓取出站请求断言哨兵串不出现。
- `[AC-44]` egress 拦截测试：把 `LLM_BASE_URL` 指向非白名单域名，请求被拒绝并告警。
- `[AC-45]` 源码中不含硬编码的第三方 AppID/密钥（`grep` 门禁 + secret 扫描）。

---

### F-12 · 发布门禁无隐私项（Process）

**证据**
- `im2a-v2.3.45/release-manifest.json`：`nineGates` = commit / tag / Release / npm / CI / Compat / Release workflow / npm-publish / healthz；**无 privacy / data-flow / retention gate**。
- `version-matrix.json` 同样只描述版本与协议 pin。
- 现状 `Compat` 门禁为 `blocked`（证据含 exitCode 1），说明门禁机制本身可用——但缺维度。

**影响**
- "删除管线覆盖表""出境清单""日志零 PII"这类结论无法进入发布证据，导致每次发版都要靠人记得。这与"可验证事实"的目标直接冲突。

**控制**
- 新增第十道门禁 `privacy`，最小证据集：
  1. 覆盖契约测试通过（AC-25）
  2. 删除后扫描 0 命中（AC-26）
  3. 日志 PII 扫描 0 命中（下文 C-11）
  4. 数据地图再生成且无未分类新字段（C-16）
  5. 出境清单完整性（AC-42）
- 门禁未过 → `release-manifest.json` 校验失败 → 阻断下一道门禁（与现有规则一致）。

**验收**
- `[AC-46]` 移除 `nineGates.privacy` 后 `npm run validate` 失败；补齐证据后通过。
- `[AC-47]` `privacy` 门禁证据包含上述 5 个 artifact 的路径与 SHA256（不引用凭证）。

---

### F-13 · 日志与审计中的标识符与 IP（Medium）

**证据**
- `security/security.go:43-45`：`security_events(event_type, method, path, status_code, client_ip, actor, user_agent, duration_ms, detail)`。
- `security.go:63`：`ClientIP` 取 `X-Forwarded-For` 首段——可被客户端伪造，进而污染审计。
- `masking/masking.go` 提供了 `Phone/IDCard/Email/Name/Address/Text` 脱敏能力，但**未见**其在 `security_events.detail` 或 `log.Printf` 路径上被调用。

**影响**
- 审计表本身就是个人数据仓库；`detail` 为自由文本，容易带入 PII。
- 可伪造的 IP 削弱审计证明力。

**控制**
- 审计写入前统一过 `masking.Text()`；`detail` 只允许结构化白名单字段。
- `client_ip` 仅信任来自受控反代列表的 `X-Forwarded-For`，否则用 `RemoteAddr`。
- 日志中禁止输出 `identifier`、手机号、wxid；必要的关联用 `subject_ref`（随机、可轮换）。

**验收**
- `[AC-48]` 注入含手机号/邮箱/身份证的请求，日志与 `security_events.detail` 中命中数为 0（正则扫描流水线）。
- `[AC-49]` 伪造 `X-Forwarded-For: 1.2.3.4` 且来源不在反代白名单时，审计记录的 IP 为真实 `RemoteAddr`。

---

### F-14 · 身份密钥材料与删除义务（Medium）

**证据**
- `020_user_keys.sql`：`user_keys(wxid, user_id, public_key, private_enc, private_nonce)`——**服务端保管加密私钥**，注释明确"只有 wxid 与服务器保管的私钥配对，才能解锁/调用该用户私有数据"。
- `user_keys` 与 `l4_subjects` 均**不在** `erasureSources` 中。
- `017_encrypted_context.sql`：服务端只存 nonce + 密文（端到端设计正确），但删除时未清理。

**影响**
- 主密钥一旦泄露 = 全量用户私钥可解，属于"一钥全崩"的单点。
- 删除后保留私钥密文，意味着"已删除用户"的加密数据在理论上仍可被解开——与删除承诺矛盾。

**控制**
- 私钥主密钥独立 KMS/HSM 保管，支持轮换；轮换时重加密 `private_enc`。
- 删除时销毁该用户私钥密文与 `encrypted_context` 记录（或至少销毁密钥使密文不可解）。
- `user_keys` 只允许服务端内部调用，不出网、不出现在任何列表接口。

**验收**
- `[AC-50]` 删除用户后 `user_keys WHERE wxid=<U>` 行数为 0；`encrypted_context WHERE user_id=<U>` 为 0。
- `[AC-51]` 主密钥轮换演练：轮换后旧数据仍可解、旧密钥失效。
- `[AC-52]` `user_keys` 不出现在任何 `GET` 路由的响应字段中（扫描路由 → 响应结构体）。

---

## 4. 目标设计

### 4.1 公开码 / 鉴权码分离（核心）

```text
公开码表（可搜索，绝不参与鉴权）
  user_handles(
    handle          text primary key,        -- 随机 12 位 base32 + 校验位
    user_id         bigint not null,
    discoverable    boolean not null default false,   -- 默认不可被搜索
    searchable_by   text not null default 'relation', -- all|relation|nobody
    created_at, retired_at, retire_reason
  )
  -- 约束：handle 永不作为任何 auth 路径的输入；轮换后旧码进入灰名单，
  -- 解析为 410 并提示"该号码已变更"，而不是静默指向新主体。

凭证（不可搜索，不对外展示）
  auth_credentials(user_id, kind, secret_hash, device_id, expires_at, revoked_at)
  -- 登录必须：验证码/设备绑定/口令 三者之一 + handle 仅用于定位候选账号
```

关键规则：
1. **handle 是定位符，不是证明。** 攻击者拿到 handle 只能发起一次"请求验证"，不能获得会话。
2. **`discoverable=false` 是默认。** 用户主动开启才可被搜索，且可随时关闭（关闭即刻生效，缓存 TTL ≤ 60s）。
3. **轮换与退役。** 换码后旧码进入 `retired_handles`，对外返回统一的"不可用"，避免"旧码 → 新主体"的串号。
4. **内部 id 不出网。** 对外一律 `subject_ref`（随机、稳定、不可反推）。

### 4.2 关联码：照抄 `dsh-session-ref`

现有 schema 已经定义了正确的关联码语义，应当作为所有跨端/跨会话引用的统一模板：

| 字段 | 语义 | 对识别码体系的意义 |
|---|---|---|
| `scope` | 解析该引用所需的调用方 scope | 跨端引用必须声明"谁能解析" |
| `expiresAt` | 到期 | 关联不是永久的 |
| `revoked` | 可撤销 | 设备丢失/换号时立刻切断 |
| `crossOrg` = `false` | 禁止跨组织 | 跨租户关联默认关闭 |

建议把 `devices`、`ticket_state`、`share_token` 全部改为携带 `scope + expiresAt + revoked` 的引用，而不是裸 id。

### 4.3 子码：默认不可枚举

- `agent_directory` 增加 `visibility`（`private` 默认 / `link` / `public`）与 `share_token`（TTL、单次或多次、可撤销）。
- 生成算法改为不可推导（随机 + 校验位），停止由 `accountID` 截断派生。
- 列表接口只返回调用者自己的 Agent；他人 Agent 通过 `share_token` 解析，且只返回白名单字段（`display_name`、能力标签），不返回 `trustLevel`、`attentionLevel`、创建时间。
- 信任档默认 `4`（陌生）保持——这是当前实现里做得对的地方（`directory.go:23-25`）。

### 4.4 历史与记忆：主体由服务端派生

- 所有 memory / im / device 路由的 `userId` 从 JWT `sub` 派生；`agentId` 可作为参数但必须校验属于该主体。
- `GuardedStore` 的读路径补 `need()` 校验，与写路径对称。
- 记忆分层保留期（见 F-08 表）+ 用户可见的"记忆中心"（查看、删除单条、清空、关闭采集）。
- 记忆事件只发 `keyHash` + 类型，不发 value。

---

## 5. 控制清单（含验收）

| ID | 控制 | 实现要点 | 验收 | 证据物 |
|---|---|---|---|---|
| C-01 | 公开码与鉴权码分离 | 新增 `user_handles` / `auth_credentials`；handle 不进 auth 路径 | AC-01/02/03 | 表结构 + 登录契约测试 |
| C-02 | 主体服务端派生 | 全路由 `UserIDFromContext`；禁 query 取 uid | AC-09/10/11/12 | 扫描门禁报告 |
| C-03 | 静态令牌不得承载个人数据 | 静态令牌仅限无 PII 端点 | AC-05 | 路由白名单 |
| C-04 | 设备绑定强身份 | 服务端签发 deviceId + 一次性绑定码 | AC-17/18 | 绑定流程时序 + 测试 |
| C-05 | 设备归属不可改写 | `ON CONFLICT` 冲突返回 409 | AC-17 | 单测 |
| C-06 | 子码默认私有 | `visibility=private`，`share_token` 可撤销 | AC-21/22/23 | schema + 测试 |
| C-07 | 子码不可推导 | 随机 id + 校验位 | AC-24 | 生成器单测 |
| C-08 | 枚举防护 | 主体维度限速 + 同构响应 + 熔断 | AC-34/35/37 | 压测报告 |
| C-09 | 内部 id 不出网 | `subject_ref` 替换自增 id | AC-36 | 响应契约 |
| C-10 | 删除覆盖契约 | `erasureSources ∪ retainedSources` 覆盖全表 | AC-25/26 | 契约测试输出 |
| C-11 | 保留项入记录 | DSR 增 `retained`/`basis`/`retainUntil` | AC-27 | DSR JSON 样例（脱敏） |
| C-12 | 备份 tombstone | 删除写 tombstone，恢复重放 | AC-28 | 恢复演练记录 |
| C-13 | 保留期自动化 | retention 包 + 每日 sweep | AC-30/31/32/33 | sweep 审计记录 |
| C-14 | 日志零 PII | `masking.Text` 覆盖日志与审计 detail | AC-48 | 扫描流水线 |
| C-15 | 事件总线分区 | 按 scope 订阅，value 不出总线 | AC-39/40 | 订阅契约 |
| C-16 | 数据地图再生 | CI 扫描 schema 变更 + PII 模式 | AC-53 | 数据地图 artifact |
| C-17 | 出境清单 | 清单 + egress 白名单 + DPA 台账 | AC-42/43/44 | 清单 + 抓包证据 |
| C-18 | 密钥治理 | KMS/HSM + 轮换 + 删除销毁 | AC-50/51 | 轮换演练记录 |
| C-19 | 可见性中心 | 三个开关（可搜索/可发现/可检索）默认关闭 | AC-41 | 产品开关清单 |
| C-20 | 发布隐私门禁 | 第十道门禁 + 5 项证据 | AC-46/47 | release-manifest 片段 |

补充验收：
- `[AC-53]` 数据地图 CI：新增迁移文件未在数据地图中登记字段分类时，构建失败；输出 `field → store → purpose → basis → retention → deletePath` 六元组。
- `[AC-54]` 每个控制项在 `docs/privacy/` 下有对应证据链接；缺失即隐私门禁失败。

---

## 6. 隐私默认设置（现状 → 目标）

| 项 | 现状 | 风险 | 目标默认 |
|---|---|---|---|
| 空 `GATEWAY_TOKEN` | 放行匿名（`identity.go:62-66`） | 配置漏填即无认证 | 生产拒绝启动 |
| 静态令牌 | 不注入主体即放行 | 无主体审计 | 仅白名单端点 |
| 登录 | 公开 + 仅 identifier | 报码即登录 | 生产关闭/需二次因子 |
| 用户码可搜索 | 无策略 | 可枚举 | `discoverable=false` |
| Agent 子码 | 按 owner 可列举 | 画像泄露 | `private` + `share_token` |
| Agent 信任档 | 默认 4（陌生） | — | ✅ 保持 |
| `im2a_send` 目标 | 缺省 `broadcast` | 越界广播 | `to` 必填 |
| presence | 全量在线会话 | 元数据泄露 | 同 scope |
| `working_memory` TTL | 有字段，手动 GC | 过期不清 | 自动 sweep |
| `episodes`/`facts` | 永久 | 无限留存 | 180d/365d |
| 记忆外发模型 | 无开关 | 出境风险 | 默认不外发 |
| CORS | 白名单（空值行为待确认） | 取决于默认 | 空 = 拒绝跨域 |
| 日志 PII | 未统一脱敏 | 审计表成 PII 仓 | 写入前脱敏 |
| `NATIVE_ENABLED` | 默认 0 | — | ✅ 保持（默认关闭） |

---

## 7. 数据地图（关键字段）

| 字段 | 存储 | 目的 | 依据 | 保留 | 删除路径 | 现状 |
|---|---|---|---|---|---|---|
| `users.identifier` | `users` | 账号定位 | 合同履行 | 账号存续 | ❌ 未清空 | F-01/F-07 |
| `users.phone_encrypted` | `users` | 登录/找回 | 合同履行 | 账号存续 | ✅ 置 NULL | OK |
| `account_identities.open_id` | `account_identities` | 第三方登录 | 同意 | 账号存续 | ✅ 删除 | OK |
| `devices.*` | `devices` | 多端同步 | 合同履行 | 设备绑定存续 | ✅ 删除 | F-05 |
| `user_keys.private_enc` | `user_keys` | 解锁私有数据 | 安全必要 | 账号存续 | ❌ 未删除 | F-14 |
| `l4_subjects.wxid` | `l4_subjects` | 记忆命名空间 | 合同履行 | 账号存续 | ❌ 未删除 | F-07 |
| `encrypted_context.ciphertext` | `encrypted_context` | 端侧加密上下文 | 同意 | 用户控制 | ❌ 未删除 | F-07/F-14 |
| `agent_directory.*` | `agent_directory` | 子码目录 | 合同履行 | 子码存续 | ✅ 删除 | F-06 |
| `working_memory.value_json` | `working_memory` | 短期记忆 | 同意 | 24h | ✅ 删除 | F-08 |
| `episodes.summary` | `episodes` | 情景记忆 | 同意 | 无上限 | ✅ 删除 | F-08 |
| `facts.value` | `facts` | 语义记忆 | 同意 | 无上限 | ✅ 删除 | F-08 |
| `messages.text` | `messages`/`im_messages` | 会话 | 合同履行 | 无上限 | ❌ 未处理 | F-07 |
| `security_events.client_ip` | `security_events` | 安全审计 | 法定义务 | 无上限 | ⚠️ 法定保留 | F-13 |
| `audit_events.*` | `audit_events` | 不可变审计 | 法定义务 | 无上限 | ⚠️ 法定保留 | F-08 |
| `data_subject_requests.result` | 同表 | DSR 证据 | 法定义务 | 无上限 | ⚠️ 需 30d 清空 | F-07/F-08 |

> 该表是工程视角的最小集；正式数据地图必须由 CI 从 schema 生成并人工补 `purpose/basis`，见 AC-53。

---

## 8. 合规映射

| 义务 | 相关条款（PIPL 为主） | 当前缺口 | 对应控制 |
|---|---|---|---|
| 告知同意、目的限定 | §14、§17 | 记忆/历史采集无目的声明，无开关 | C-19、C-17 |
| 最小必要 | §6 | 收集 `identifier` 兼作凭证、IP/UA 永久 | C-01、C-13、C-14 |
| 个人权利（查阅/复制/更正/删除） | §44-47 | 删除不完整、无自助入口、无 SLA | C-10/11/12 |
| 删除后不得再次使用 | §47 | 已注销账号可凭原码复活 | AC-03 |
| 安全保障措施 | §51 | 静态共享令牌、query 自报主体 | C-02/03/04 |
| 跨境提供 | §38-39 | 无清单、无区域控制、模型端点可变 | C-17 |
| 自动化决策告知 | §24 | Agent 审批/计费链路无告知 | 设计评审项 |
| 等保 2.0 审计留存 | 6 个月 | 有留存，无到期与最小化 | C-11/13 |
| GDPR（如涉欧盟用户） | Art.5/15/17/32/44 | 同上 + 无 DPIA 记录 | 建议补 DPIA |

---

## 9. 发布门禁建议（v2.3.45 起）

```text
nineGates + privacy
  privacy 证据集：
    1. erasure-coverage.json      # AC-25 输出（表覆盖矩阵）
    2. deletion-scan.json         # AC-26 输出（0 命中证明）
    3. log-pii-scan.json          # AC-48 输出
    4. data-map.json              # AC-53 输出（六元组）
    5. cross-border-register.json # AC-42 输出（不含凭证）
  任一缺失或 status != pass → release-manifest 校验失败 → 阻断后续门禁
```

当前 `Compat` 门禁已处于 `blocked`（证据含 exitCode 1），说明门禁链路本身有效；新增 `privacy` 不改变机制，只增加维度。

---

## 10. 优先级建议

| 优先级 | 项 | 理由 |
|---|---|---|
| **P0（发版前必须）** | F-01、F-02、F-03、F-05、F-07 | 可直接导致非授权访问、伪造或"假删除" |
| **P0** | AC-25/26/27（删除覆盖与记录） | 监管问询第一站 |
| **P1（30 天内）** | F-04、F-06、F-08、F-09、F-11 | 横向越权、画像泄露、留存与出境 |
| **P1** | C-20 隐私门禁 | 防止回潮 |
| **P2（90 天内）** | F-10、F-13、F-14、C-16 | 治理与长期负债 |

---

## 11. 限制与未验证项

1. **动态验证缺失**：本文全部结论来自静态阅读，未对运行实例发起请求；AC 项即为补齐动态验证的脚本化清单。
2. **第三方控制台未核验**：腾讯云 IM/推送、微信、模型供应商的实际配置与数据驻留未知，F-11 的出境判定需以实际配置复核。
3. **`/admin/wechat-bind` 为公开页**（`routes_admin.go:112-114` 注释自述），本次未核对其渲染内容是否含他人 PII，列为待查项。
4. **`masking` 的实际调用点**未全量排查，F-13 的"未覆盖日志"基于未发现调用推断，需以 AC-48 的扫描结果确认。
5. **备份与对象存储**无访问权，F-07 的备份结论基于"代码中无 tombstone 机制"推断。
6. **`im2a-v2.3.45` 内不含识别码实现**，本文对网关仓的引用以 `version-matrix.json` 固定的 `gateway 2.3.26` 为准；若实际部署版本不同，需按同一清单复核。

---

## 附录 A · 验收项索引（可直接转工单）

| 批次 | 验收项 | 关联发现 | 类型 |
|---|---|---|---|
| P0 | AC-01 ~ AC-04 | F-01 | 契约测试 / 静态扫描 |
| P0 | AC-05 ~ AC-08 | F-02 | 路由白名单 / 启动自检 |
| P0 | AC-09 ~ AC-13 | F-03 | 横向越权回归 |
| P0 | AC-17 ~ AC-20 | F-05 | 设备绑定 |
| P0 | AC-25 ~ AC-29 | F-07 | 删除覆盖 / 扫描 / 备份 |
| P1 | AC-14 ~ AC-16 | F-04 | IM 主体派生 |
| P1 | AC-21 ~ AC-24 | F-06 | 子码可见性 |
| P1 | AC-30 ~ AC-33 | F-08 | 保留期 sweep |
| P1 | AC-34 ~ AC-38 | F-09 | 枚举防护 |
| P1 | AC-42 ~ AC-45 | F-11 | 出境 / egress |
| P1 | AC-46 ~ AC-47 | F-12 | 发布门禁 |
| P2 | AC-39 ~ AC-41 | F-10 | 事件总线 |
| P2 | AC-48 ~ AC-49 | F-13 | 日志 PII |
| P2 | AC-50 ~ AC-52 | F-14 | 密钥材料 |
| 持续 | AC-53 ~ AC-54 | 全部 | 数据地图 / 证据链 |

### 执行顺序建议

1. 先做 **AC-12（扫描门禁）**：一条 grep 就能列出全部"query 取主体"的调用点，作为 F-03/F-04/F-06 的修复清单。
2. 再做 **AC-25（覆盖契约测试）**：它会自动暴露所有未分类的表，避免人工遗漏。
3. **AC-26（删除后扫描）** 是唯一能对外证明"删除已生效"的证据，应最先形成可持续运行的流水线。
4. 其余按 P0 → P1 → P2 推进，每完成一项即在 `docs/privacy/` 落一份证据文件，供隐私门禁引用。

---

## 附录 B · 与本次评审相关的正面实践（应保留）

- `dsh-session-ref` schema：`scope` + `expiresAt` + `revoked` + `crossOrg=false`，是关联码的正确样板。
- `GuardedStore` 的跨用户一律拒绝规则（`guard.go:98-101`）——规则本身正确，问题只在 API 边界没有把主体喂对。
- Agent 默认信任档 4（陌生）= 最小权限默认（`directory.go:23-25`）。
- `encrypted_context` 服务端不存密钥（`017_encrypted_context.sql`），端到端方向正确。
- `working_memory.expires_at` + 部分索引：保留期的正确建模，只差调度器。
- 九门禁机制本身可用（`release-manifest.json` 已有 `blocked` 状态与证据结构），加一道隐私门禁即可复用全部基础设施。

---

*本文档不含任何真实凭证、密钥或令牌值。所有环境变量仅以名称引用。*
