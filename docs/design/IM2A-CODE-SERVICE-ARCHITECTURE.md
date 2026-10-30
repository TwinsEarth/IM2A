# IM2A 识别码服务端架构

- 状态：Proposed（设计评审稿）
- 日期：2026-10-30
- 适用版本：IM2A Platform v2.3.45 / Go Gateway v2.3.26
- 范围：唯一识别码、授权码、用户码、IM2A 号码、Agent 子码、明码的编码规则、验证、等级权限、编码库管理，以及发号、存储、TTL/回收、并发冲突和服务边界。
- 非范围：UI 交互细节、具体定价、法务结论、实现源码。本文只给契约、数据模型、状态机与架构决策，不粘贴实现代码。

---

## 0. 结论摘要

1. **两平面硬解耦**：公开寻址面（Public Addressing Plane）只回答“这是谁、往哪里送”；私密鉴权面（Private Credential Plane）只回答“你被允许做什么”。两平面不共享命名空间、数据库 schema、数据库角色、密钥、缓存、API 监听端口和日志字段。
2. **Postgres 是唯一事实源**：码的归属、状态、预留、配额、授权码元数据以 Postgres 为准；Redis 只做缓存、限流、发号缓冲和短期租约，丢了可以重建。
3. **号码不因吊销/注销/释放而回收，其他公开码隔离后受限复用**：任何曾经 `ACTIVE` 的 IM2A 号码都不因吊销/注销/释放重新分配给另一个主体，只做 tombstone；显式转移是唯一例外（双方同意 + 双人审批 + 审计，号码本身不变）。用户码/Agent 子码/明码按隔离期规则处理，绝不在没有 tombstone、审计和适用限制的情况下直接改指向新主体。可立即回池的只有“从未激活的预留”。
4. **发号器用“号段租约 + 本地缓冲 + 唯一索引兜底”**：允许断号，不允许重号；崩溃丢失的未发号码视为永久空洞，不做回收，避免双发。
5. **授权码是 opaque 高熵凭据**：80 bit token_id + 130 bit secret、服务端 HMAC 存储、短 TTL、单次使用、可即时吊销；不是 JWT，不是公开码的附属字段，不出现在 URL、日志、埋点和公开解析响应中。
6. **等级是“供给权限”，不是“信任等级”**：账号等级决定能拥有多少码、能否选靓号、预留多久、授权码 TTL 多长；信任等级决定谁能联系谁。高等级不等于可打扰任何人。
7. **服务边界先逻辑后物理**：v1 用“码注册表 + 凭据模块”一个可部署单元，公开解析器独立部署；当解析 QPS、合规或团队边界需要时，再把凭据服务拆成独立部署。
8. **最大风险不是技术，而是语义漂移**：有人把公开码当凭据或授权主体用，或把授权码当公开地址查。必须用 schema、网络、密钥、契约测试和评审清单把这条红线钉死。

---

## 1. 术语与码型

### 1.1 码型总览

| 码型 | 定义 | 公开性 | 典型形态 | 生命周期 |
|---|---|---|---|---|
| 内部唯一识别码 | 主体/码/凭据的内部主键，UUIDv7，永不出现在公开 API | 不公开 | `0192f...` | 永久，随主体合并而保留映射 |
| IM2A 号码 | 人或组织的稳定公开主地址，10 位数字，含校验位 | 公开 | `123 456 7897` | 永久；不因吊销/注销回收；可审批转移 |
| 用户码 | 人的可读别名/handle，全局唯一，6–20 位 ASCII | 公开 | `@alice` | 可改；旧码进入别名期与隔离期 |
| Agent 子码 | 挂在 owner 号码下的 Agent 公开子地址 | 公开 | `1234567897.01` | 随 Agent 生命周期；删除后隔离 |
| 明码 | 可公开、可轮换的分享码，用于海报/二维码/离线场景 | 公开 | `M7K3-M9Q2-X4R7` | 有 TTL，可续期/轮换；过期后 404 |
| 授权码 | 一次性/短时私密凭据，用于绑定设备、认领 Agent、邀请关系、转移确认 | 私密 | `im2a_ac_...` | 5–30 分钟，单次使用，可吊销 |

### 1.2 三个容易混淆的点

**“唯一识别码”的两种用法。** 本文把“唯一识别码”限定为内部主键（`subject_id`/`code_id`/`credential_id`），因为它不承担公开寻址；若产品需要“对外唯一码”，使用 IM2A 号码/用户码/Agent 子码，不要另造第四套公开 ID。内部 ID 一旦公开，就会诱使客户端把它当公开地址或授权主体，破坏本架构的红线。

**用户码与 IM2A 号码不是一回事。** 号码是稳定地址，适合印刷、拨号、跨平台引用；用户码是人的可读别名，适合社交传播，可改。两者都是公开寻址，但生命周期和回收规则不同。一个主体最多一个主号码；用户码在满足等级条件后可选配。

**明码有两种可能语义，本设计默认第二种：**

- 语义 A：明码只是号码/用户码/子码的“可见字符串渲染”，不是独立实体。此时不需要 `PUBLIC_ALIAS` 表，解析器直接格式化。
- 语义 B（本文默认）：明码是一个独立的、可轮换的公开别名，指向某个公开名片。它不暴露稳定号码，适合海报、二维码、临时分享；泄露后可轮换，过期后失效。这样“稳定地址”和“可分享公开码”也解耦。

如果产品确认采用语义 A，删除 `PUBLIC_ALIAS` 码型即可，其余架构不变。

### 1.3 三个内部概念

| 概念 | 说明 |
|---|---|
| `subject_id` | 主体内部 ID，UUIDv7。人、Agent、组织、设备都可有 subject；公开 API 不返回。 |
| `code_id` | 码记录内部 ID，UUIDv7。一个 subject 可对应多条码记录。 |
| `credential_id` | 授权码元数据内部 ID，UUIDv7。授权码明文只在签发时返回一次。 |

---

## 2. 核心原则

### 2.1 公开寻址与私密鉴权解耦（红线）

一句话：**公开码只回答“这是谁/往哪送”，授权码只回答“你被允许做什么”。**

五层分离：

| 层 | 公开寻址面 | 私密鉴权面 |
|---|---|---|
| 命名空间 | `IM2A_NUMBER`、`USER_HANDLE`、`AGENT_SUBCODE`、`PUBLIC_ALIAS` | `AUTH_CODE`（只在 credential schema） |
| 存储 | `code` schema，读多写少 | `credential` schema，写少读严；独立 DB role |
| 密钥 | 公开缓存签名 key、CDN key | 授权码 pepper、绑定 token key；KMS/HSM |
| 网络/API | 互联网边缘，匿名可读，CDN 可缓存 | 内网 mTLS，仅服务间/步进认证后调用；禁止公网路由 |
| 语义 | 解析只返回公开名片投影 | 校验只返回 `subject_id`、scope、绑定结果；不返回公开名片 |

**必须禁止的反模式：**

- 把 IM2A 号码或用户码当 JWT 的 `sub` 或授权主体的唯一依据。
- 把公开码和授权码放在同一张表、同一 schema、同一缓存。
- 提供一个“查码 + 验码”二合一的公开接口。
- 在公开解析响应里返回 `subject_id`、owner、等级、配额、邮箱/手机号、是否拉黑你。
- 把授权码放进 URL query、Referer、前端埋点、APM 标签、Nginx access log。
- 在边缘缓存授权码校验结果。
- 号码被吊销后重新分配给另一个主体。
- 把“码存在”当成“已授权”。

### 2.2 其他原则

1. **Postgres 唯一事实源**：任何“这个码属于谁、能不能用”的判断，最终以 Postgres 唯一约束和事务为准。
2. **号码不因吊销/注销/释放而回收**：IM2A 号码是永久地址，回收即冒充风险；只允许双方同意 + 双人审批的显式转移。用户码/子码/明码按隔离规则受限复用。
3. **等级 ≠ 信任**：供给权限与关系权限分开建模、分开执行。
4. **契约优先**：OpenAPI 3.1 + AsyncAPI；错误码、分页、幂等、限流、鉴权语义全部写进契约。
5. **最小公开投影**：默认不公开；公开字段逐项评审。
6. **幂等 + 乐观并发**：所有创建/变更接口支持 `Idempotency-Key`；更新用 `ETag/If-Match` 或 `version`。
7. **可审计**：谁、在什么审批下、把哪个码从什么状态改到什么状态，必须可回放。
8. **先模块化单体，后按压力拆分**：不要为了“微服务”先付出分布式一致性成本。

---

## 3. 总体架构

### 3.1 逻辑架构

| 组件 | 职责 | 拥有的数据 | 对外接口 | 明确不做 |
|---|---|---|---|---|
| Public Resolver（公开解析器） | 公开码 → 公开名片投影；批量解析；可用性查询；二维码/名片页 | Redis 缓存、只读副本 | `/v1/public/*` | 不写库、不读 credential schema、不返回内部 ID |
| Code Registry（码注册表） | 码生命周期、预留/确认/释放、配额、等级策略、发号器、别名 | `code` schema、`code_pool`、`policy` | `/v1/me/codes`、`/internal/v1/codes` | 不存密码、不存 session、不存授权码明文 |
| Credential Service（凭据服务） | 授权码签发/校验/吊销/轮换、绑定 token | `credential` schema、KMS | `/v1/credentials`、`/v1/bindings` | 不决定码归属、不做公开解析 |
| Admin Backoffice（管理面） | 号池、黑名单、策略发布、强制吊销/转移、对账、审计导出 | 通过 Registry/Credential 管理 API 操作 | `/v1/admin/*` | 不直连业务库；破坏性操作需双人审批 |
| Account/Profile（既有） | 主体身份、公开资料 | 用户/组织资料 | 事件/内部 API | 不拥有码 |
| Auth（既有） | 登录、OIDC/JWT、步进认证 | 会话/凭据 | 内部 API | 不签发识别码 |
| Agent Registry（既有） | Agent 元数据、能力、归属 | Agent 记录 | 内部 API | 不拥有公开子码 |
| Payment（既有） | 靓号/会员购买、退款 | 账本 | 内部 API/事件 | 不直接改码状态 |
| Anti-abuse（既有） | 黑名单、混淆字符、风控 | 风控数据 | 内部 API | 不拥有码 |
| Audit（既有） | 不可变审计、哈希链 | 审计日志 | 事件/查询 | 不改业务状态 |
| Event Bus | 领域事件、outbox 投递 | 事件日志 | AsyncAPI | 不作为事实源 |

### 3.2 部署形态（v1 推荐）

- **Code Registry + Credential Service 同一可部署单元（code-service）**，但使用两个独立 HTTP listener、两个 DB schema、两个 DB role、两个 KMS key；credential listener 只在内部网络暴露，禁止挂到公网 ingress。这样先拿到逻辑隔离和较低运维成本，后续可无痛拆分。
- **Public Resolver 独立部署**，放在 CDN/WAF 后面，只连 Redis 和只读副本；不连主库，不连 credential schema。它是最容易被攻击、也最容易水平扩展的一层，必须与写面和私密面物理隔离。
- **Admin 独立 host/端口**，SSO + MFA + IP allowlist/VPN；不与公开面共享 Cookie 域。
- 生产拓扑：3 AZ、无状态 Pod、HPA 按 QPS/延迟、PodDisruptionBudget；Postgres 主 + 同步副本 + 异步只读副本；Redis Cluster；KMS/Secret Manager；事件总线 Kafka/NATS JetStream。
- 所有服务默认拒绝入站，只放行明确来源；公开解析器出站只允许 Redis、只读副本、KMS 验签（如有）。

### 3.3 服务边界规则

1. Code Registry 只拥有“码 ↔ 主体”的映射和码状态；主体资料由 Account/Profile 拥有，通过 `subject_id` 引用，不做跨服务外键。
2. Credential Service 只拥有授权码和绑定；需要校验目标码时，调用 Registry 的窄接口 `GET /internal/v1/codes/{id}:binding-view`，只返回 `code_id`、`subject_id`、`status`、`code_type`，不返回公开资料。
3. Public Resolver 只读 `code_public_view`（物化视图/只读副本视图），字段白名单固定；数据库 role 对 `credential` schema 无任何权限。
4. Admin 不直连数据库；所有操作走管理 API，写审计和审批单。
5. 跨服务一致性用 outbox + 至少一次投递；消费者必须幂等。
6. 任何服务不得把公开码作为服务间身份凭证；服务间用 mTLS + workload identity。

### 3.4 关键调用链

**公开解析：**

客户端 → CDN/WAF → Public Resolver → Redis（命中即返回）→ 未命中则只读副本 → 组装公开投影 → 回填缓存 → 返回。全过程不触碰 credential schema、不要求登录、不返回内部 ID。

**自助领号：**

客户端（OIDC JWT）→ Code Registry：校验 `sub`、等级、配额 → 事务内分配/预留 → 写 code + quota + outbox → 返回码/预留。若为靓号，进入 Payment saga：预留 → 支付 → 确认；支付失败/超时则释放预留。

**设备绑定：**

用户步进认证 → Credential Service 签发授权码（明文只返回一次）→ 客户端在 POST body 提交授权码 + 设备信息 → Credential Service 原子消费 → 创建 binding token → 返回长期绑定凭据。授权码不进入公开解析链路。

**Agent 子码：**

Agent Registry 创建 Agent → Code Registry 在 owner 配额内分配子码 → 公开解析可查到 Agent 名片 → 实际发消息仍需 owner 的信任/同意/授权码，子码本身不授予通信权。

### 3.5 与既有 IM2A 的集成

- 既有 `users.identifier` 不废弃，转为“用户码别名/历史别名”，新主地址由 Code Registry 分配。
- 既有 `agentId` 保持内部 ID；新增 `agent_subcode` 映射；对外展示优先子码。
- 既有 JWT 的 `sub` 继续是内部 `subject_id`；不得改成公开码。
- 既有审计链接入 `code.*` / `credential.*` 事件；保持哈希链。
- 既有支付账本用于靓号购买；Code Registry 不记金额，只记 `payment_ref`。
- 既有插件体系不承载识别码服务：这是身份/一致性核心，应该是核心服务，不是可卸载插件。
- 技术栈建议沿用 v2 网关（Go + Postgres + Redis），识别码服务作为核心域服务；TypeScript SDK 只做契约客户端，不复制业务规则。

---

## 4. 编码规则

### 4.1 设计约束

- 全局唯一、可读、可校验、抗混淆、无 PII、无强语义（类型/地区不写死在码里，便于迁移）。
- 公开码要防“输错一位就找到别人”；私密码要防枚举和暴力破解。
- 公开码可以接受一定可枚举性，但必须配合限流、批量上限和异常检测；私密码必须高熵。
- 所有码在入库前归一化；展示格式与规范格式分离。

### 4.2 码型规格

| 码型 | 规范格式 | 字符集/长度 | 校验 | 分配方式 | 备注 |
|---|---|---|---|---|---|
| IM2A 号码 | 10 位数字 | `[1-9]` + 8 位数字 + 1 位 Luhn 校验 | Luhn mod 10 | 号池：顺序/随机/靓号 | 展示 `3-3-4`；不伪装成电话号码 |
| 用户码 | `@` + 6–20 位 | `[a-z][a-z0-9_]{5,19}` | 语法 + 黑名单 + 混淆骨架 | 自选/系统建议 | 大小写不敏感；仅 ASCII v1 |
| Agent 子码 | `<owner-number>.<slot>` | slot 2–6 位 `[a-z0-9]`，首字符字母或数字 | 语法 + owner 存在 + 配额 | 系统分配/自选 | 全局唯一由 owner 号码保证 |
| 明码 | `M` + 10 位数据 + 1 位校验 | Crockford Base32 | 加权 mod 32 校验字符 | 随机 | 50 bit 数据；可轮换 |
| 授权码 | `im2a_ac_` + 16 位 token_id + `_` + 26 位 secret | Crockford Base32；token_id 80 bit，secret 130 bit | 解析 token_id + HMAC(secret) 常量时间校验 | CSPRNG | 只出现在私密 API |
| 内部 ID | UUIDv7 | 128 bit | 无 | 应用生成 | 不公开 |

### 4.3 归一化与校验流水线

1. **长度闸门**：先拒绝超过最大长度的输入，防止正则/DB 放大攻击。
2. **Unicode 处理**：NFKC 归一化；用户码 v1 只接受 ASCII，非 ASCII 直接拒绝（展示名另存，不进入地址）。
3. **类型识别**：按前缀/形态识别，不做“猜类型”：
   - 纯 10 位数字 → 号码；
   - `@` 开头 → 用户码；
   - 号码 + `.` + slot → Agent 子码；
   - `M` 开头且长度匹配 → 明码；
   - `im2a_ac_` 开头 → 授权码，且只允许进入 Credential API，公开解析器直接拒绝。
4. **语法校验**：按类型正则/长度/字符集；号码做 Luhn；明码做校验字符。
5. **策略校验**：保留字、黑名单、混淆骨架、连续/重复/纯数字等“高冒犯/高仿冒”模式。
6. **存在性/状态校验**：查 Registry；公开解析只返回 `ACTIVE` 且 `visibility` 允许的码。
7. **写路径**：在同一个事务内做配额、唯一性、策略、幂等；唯一索引是最终仲裁者。

**统一失败语义：** 公开解析对“不存在/已吊销/不可见”一律返回同一个 `404 CODE_NOT_FOUND`，避免泄露“这个码曾经存在”。别名在宽限期返回 `200 + moved_to`；宽限期后同样 404。所有者自查接口可以看到真实状态。

### 4.4 校验位与字母表

- **号码校验位**：9 位 payload + Luhn 校验位 = 10 位。Luhn 能发现所有单字符错误和绝大多数相邻换位；对“拨错一位找到别人”的防护足够。号码空间约 9 亿，按需扩容到 11 位需新码型/新号池，不在原号段上加长。
- **明码校验字符**：Crockford Base32 值 + 加权 mod 32，输出 0–9/A–Z 的子集；用于人工输入纠错，不承担安全职责。
- **Crockford Base32**：去掉 `I/L/O/U`，减少 `0/O`、`1/I/L` 混淆；授权码不做人类纠错归一化，按原样精确匹配。
- **用户码混淆骨架**：NFKC + casefold + 同形字映射（`0/O`、`1/I/l`、`5/S`、`2/Z`、`rn/m`、西里尔/希腊同形字）后比较；与高价值活跃码骨架相同则拒绝。
- **禁止模式**：全同数字、顺子、重复块、`000/111/123456` 等；品牌/政府/紧急号码；多语言冒犯词；竞品名。具体规则由编码库策略版本管理，不写死在代码。

### 4.5 保留与命名空间

- `admin`、`support`、`help`、`im2a`、`system`、`root`、`security`、`official`、`team`、`agent`、`api`、`www`、`mail`、`null`、`undefined` 等永久保留。
- 号码不编码地区/类型；地区、用途、等级只作为元数据。这样号码可以跨区迁移、组织升级、个人转组织，不需要换号。
- 未来多区域时，用 `region` 元数据 + 号池隔离保证唯一；不把区域码写进号码，避免“号码即归属”的僵化。
- 靓号/短号只从 `VANITY` 号池分配，走支付与审批；不与普通号池混用。

### 4.6 示例与反例

**合法示例（均为虚构，不代表真实号段）：**

- 号码：`123 456 7897`（10 位，Luhn 通过；仅作格式示例）
- 用户码：`@alice_2026`
- Agent 子码：`1234567897.01`
- 明码：`M7K3-M9Q2-X4R7`（形态示例；校验字符按算法生成）
- 授权码：`im2a_ac_...`（26 位 Base32，仅在签发响应中出现一次）

**反例：**

- `123456789`（少一位/无校验）→ `CODE_INVALID`
- `@alice!`（非法字符）→ `CODE_INVALID`
- `1234567890.`（空 slot）→ `CODE_INVALID`
- `@Admin`（保留字）→ `CODE_RESERVED`
- `@a1ice` 与 `@alice` 混淆骨架冲突 → `CODE_TAKEN`/`CODE_RESERVED`
- 用 `im2a_ac_...` 调公开解析 → `404`，且不记录明文
- 用 `@alice` 当 Bearer token → `401`，公开码不是凭据

---

## 5. 数据模型

### 5.1 Schema 分区

| Schema | 内容 | DB role | 访问方 |
|---|---|---|---|
| `code` | 码、别名、预留、号池、租约、配额、策略、黑名单、outbox | `code_rw` / `code_ro` | Registry 写；Resolver 只读视图 |
| `credential` | 授权码、使用记录、绑定、吊销 | `cred_rw` / `cred_ro` | Credential Service only |
| `admin` | 审批单、导入任务、对账运行 | `admin_rw` | Admin Backoffice |
| `audit` | 审计（或复用平台审计） | append-only | 所有服务写事件，审计服务读 |

`code` 与 `credential` 可以是同一个 Postgres 集群，但必须是不同 database/schema + 不同 role；Public Resolver 的 role 对 `credential` 零权限。合规要求高时，credential 独立集群。

### 5.2 核心表

**`code_subject`（主体投影，非主体主数据）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `subject_id` | uuid PK | 内部主体 ID |
| `subject_type` | enum | `HUMAN`/`AGENT`/`ORG`/`DEVICE` |
| `status` | enum | `ACTIVE`/`MERGED`/`DELETED` |
| `public_profile_id` | uuid | 公开名片 token（随机 UUIDv4，可选） |
| `updated_at` | timestamptz | 事件同步时间 |

**`code`（码记录）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `code_id` | uuid PK | 内部 ID |
| `code_type` | enum | `IM2A_NUMBER`/`USER_HANDLE`/`AGENT_SUBCODE`/`PUBLIC_ALIAS` |
| `normalized_value` | text | 归一化后的唯一值；永不修改 |
| `display_value` | text | 展示格式；可调整分组 |
| `status` | enum | 见状态机 |
| `subject_id` | uuid | 码指向的主体；号码/用户码/明码的 owner |
| `owner_subject_id` | uuid | Agent 子码的 owner；其他类型等于 subject_id |
| `visibility` | enum | `PUBLIC`/`UNLISTED`/`PRIVATE` |
| `tier_at_issue` | text | 签发时等级，用于 grandfathering |
| `grandfathered` | boolean | 降级后是否保留 |
| `issued_at` | timestamptz | 分配时间 |
| `activated_at` | timestamptz | 生效时间 |
| `expires_at` | timestamptz | 预留/明码 TTL；永久码为 null |
| `quarantine_until` | timestamptz | 隔离结束时间 |
| `revoked_at` | timestamptz | 吊销时间 |
| `revoke_reason` | text | 受控枚举 + 备注 |
| `version` | bigint | 乐观并发版本 |
| `metadata` | jsonb | 地区、语言、Agent 类型、组织等；无 PII |
| `payment_ref` | text | 靓号支付引用，可空 |
| `created_by` | uuid | 操作者/服务 |
| `updated_at` | timestamptz | 更新时间 |

**`code_alias`（旧码 → 新码）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `alias_id` | uuid PK | |
| `code_type` | enum | 别名所属码型 |
| `alias_value` | text | 旧码归一化值，全局唯一 |
| `target_code_id` | uuid | 新码 |
| `reason` | enum | `RENAMED`/`TRANSFERRED`/`MERGED` |
| `status` | enum | `ACTIVE`/`EXPIRED`/`REVOKED` |
| `expires_at` | timestamptz | 宽限期结束 |
| `quarantine_until` | timestamptz | 隔离结束；到期后 namespace 可释放 |
| `created_at` | timestamptz | |

**`code_namespace`（统一命名空间）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `namespace_key` | text PK | `code_type:normalized_value`，code 与 alias 共用 |
| `current_owner_kind` | enum | `CODE`/`ALIAS`/空 |
| `current_owner_id` | uuid | 当前占用记录；释放后为空 |
| `status` | enum | `LIVE`/`RELEASED`/`PERMANENT` |
| `released_at` | timestamptz | 释放时间 |
| `created_at` / `updated_at` | timestamptz | 永不删除 |

`code` 与 `code_alias` 写入前必须在同一事务内争用 `code_namespace` 的 `LIVE` 占用；唯一主键是“码与别名绝不撞”的最终保证。号码的 namespace 只会 `LIVE → PERMANENT`；其他类型可 `LIVE → RELEASED → LIVE`（隔离期满后的受限复用）。`code` 自己的唯一索引仍保留，作为第二道防线。

**`code_reservation`（预留）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `reservation_id` | uuid PK | |
| `subject_id` | uuid | 预留人 |
| `code_type` | enum | |
| `requested_value` | text | 归一化候选；系统分配可为空 |
| `code_id` | uuid | 已创建的 `RESERVED` 码记录 |
| `status` | enum | `PENDING`/`PAID`/`CONFIRMED`/`EXPIRED`/`CANCELLED`/`REFUNDED` |
| `expires_at` | timestamptz | TTL |
| `price_cents` | integer | 靓号价格快照 |
| `payment_ref` | text | 支付单 |
| `idempotency_key` | text | 唯一 |
| `version` | bigint | CAS |
| `created_at` | timestamptz | |

**`code_pool`（号池）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `pool_id` | uuid PK | |
| `code_type` | enum | 号码/明码等 |
| `region` | text | 区域/租户 |
| `purpose` | enum | `CONSUMER`/`BUSINESS`/`ORG`/`TEST`/`VANITY`/`EMERGENCY`/`LEGACY` |
| `allocation_mode` | enum | `SEQUENTIAL`/`RANDOM`/`VANITY_ONLY`/`IMPORTED` |
| `range_start` | bigint | 号码 payload 或序号 |
| `range_end` | bigint | |
| `next_value` | bigint | 高水位；随机模式可为空 |
| `status` | enum | `DRAFT`/`ACTIVE`/`DRAINING`/`EXHAUSTED`/`RETIRED` |
| `lease_block_size` | integer | 顺序模式每批租约大小 |
| `version` | bigint | |

**`code_lease`（发号租约）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `lease_id` | uuid PK | 也是 fencing token 的一部分 |
| `pool_id` | uuid | |
| `node_id` | text | 实例标识 |
| `range_start` / `range_end` | bigint | 本批范围 |
| `next_value` | bigint | 本地已发到哪 |
| `status` | enum | `ACTIVE`/`EXHAUSTED`/`ABANDONED` |
| `leased_at` / `expires_at` | timestamptz | 观测用，不用于回收 |
| `version` | bigint | |

**`subject_quota`（配额）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `subject_id` | uuid | 配额主体；Agent 子码配额记在 owner 上 |
| `code_type` | enum | |
| `limit` | integer | 当前策略快照 |
| `used` | integer | 活跃码数 |
| `reserved` | integer | 预留数 |
| `version` | bigint | 原子更新 |

**`policy_tier_version`（等级策略，版本化）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `policy_version` | bigint PK | 版本号 |
| `tier` | text | `T0`–`T4` |
| `code_type` | enum | |
| `max_active` | integer | 最多活跃码 |
| `max_reserved` | integer | 同时预留 |
| `min_length` / `max_length` | integer | 用户码长度 |
| `vanity_allowed` | boolean | 是否可选靓号 |
| `pattern_policy` | jsonb | 允许的正则/模式 |
| `reservation_ttl_seconds` | integer | |
| `quarantine_days` | integer | |
| `alias_window_days` | integer | |
| `transfer_allowed` | boolean | |
| `visibility_default` | enum | |
| `rate_limit_profile` | text | |
| `effective_from` / `effective_to` | timestamptz | |
| `published_by` / `approval_id` | uuid | 双人审批 |

**`blocklist_entry`（编码库黑名单）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `entry_id` | uuid PK | |
| `category` | enum | `RESERVED`/`OFFENSIVE`/`BRAND`/`GOV`/`EMERGENCY`/`COMPETITOR`/`LEGAL_HOLD` |
| `pattern` | text | 精确值/前缀/正则 |
| `match_type` | enum | `EXACT`/`PREFIX`/`REGEX`/`SKELETON` |
| `severity` | enum | `BLOCK`/`REVIEW`/`WARN` |
| `source` | text | 法务/品牌/风控/人工 |
| `active` | boolean | |
| `created_by` / `approved_by` | uuid | |

**`code_event_outbox`（可靠事件）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `event_id` | uuid PK | |
| `aggregate_type` / `aggregate_id` | text/uuid | `code`/`credential` |
| `event_type` | text | `code.issued` 等 |
| `payload` | jsonb | 不含授权码明文 |
| `status` | enum | `PENDING`/`PUBLISHED`/`FAILED` |
| `attempts` | integer | |
| `next_retry_at` | timestamptz | |
| `created_at` | timestamptz | |

**`idempotency_record`**

| 字段 | 类型 | 说明 |
|---|---|---|
| `key` | text PK | 客户端 key + 路由 |
| `request_hash` | text | 方法+路径+body 哈希 |
| `response_status` / `response_body` | integer/jsonb | 可重放响应；不得含授权码明文，敏感响应只存 ID/摘要 |
| `status` | enum | `IN_PROGRESS`/`COMPLETED` |
| `expires_at` | timestamptz | 24h |

**`credential`（独立 schema）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `credential_id` | uuid PK | 内部 ID；可在签发响应/管理面返回，不作为公开解析字段 |
| `token_id` | text UNIQUE | 16 位 Crockford Base32，随机 80 bit；用于 O(1) 查找，不是秘密 |
| `secret_hash` | bytea | HMAC-SHA256(pepper, secret)；常量时间比较 |
| `subject_id` | uuid | 签发对象 |
| `target_code_id` | uuid | 可选绑定目标 |
| `purpose` | enum | `BIND_DEVICE`/`CLAIM_AGENT`/`INVITE`/`RECOVER`/`TRANSFER` |
| `scopes` | jsonb | 允许动作 |
| `status` | enum | `ACTIVE`/`USED`/`EXPIRED`/`REVOKED`/`LOCKED` |
| `max_uses` / `used_count` | integer | 通常 1 |
| `expires_at` | timestamptz | |
| `revoked_at` | timestamptz | |
| `pepper_version` | integer | 轮换用 |
| `attempt_count` | integer | |
| `locked_until` | timestamptz | |
| `created_by` | uuid | |
| `created_at` | timestamptz | |

**`credential_use`（使用记录）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `use_id` | uuid PK | |
| `credential_id` | uuid | |
| `subject_id` | uuid | |
| `result` | enum | `OK`/`INVALID`/`EXPIRED`/`USED`/`LOCKED`/`SCOPE_MISMATCH` |
| `ip_hash` | bytea | 日盐哈希，不存原 IP |
| `device_id` | text | 客户端设备标识 |
| `request_id` | text | |
| `created_at` | timestamptz | |

**`binding`（长期绑定）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `binding_id` | uuid PK | |
| `subject_id` | uuid | |
| `device_id` / `agent_id` | text | |
| `token_hash` | bytea UNIQUE | 长期 token 的 HMAC |
| `scopes` | jsonb | |
| `status` | enum | `ACTIVE`/`REVOKED`/`EXPIRED` |
| `expires_at` / `revoked_at` | timestamptz | |
| `last_seen_at` | timestamptz | |

**`admin_approval`（双人审批）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `approval_id` | uuid PK | |
| `action` | enum | `REVOKE`/`TRANSFER`/`RECYCLE`/`POLICY_PUBLISH`/`BLOCKLIST_REMOVE` |
| `target_id` | uuid | |
| `requested_by` / `approved_by` | uuid | 必须不同人 |
| `reason` / `ticket_ref` | text | |
| `status` | enum | `PENDING`/`APPROVED`/`REJECTED`/`EXPIRED` |
| `created_at` / `decided_at` | timestamptz | |

**`import_job` / `reconciliation_run`**

| 表 | 关键字段 | 说明 |
|---|---|---|
| `import_job` | `job_id`, `pool_id`, `file_hash`, `status`, `total`, `ok`, `failed`, `report` | 批量导入，幂等按文件哈希 |
| `reconciliation_run` | `run_id`, `started_at`, `finished_at`, `diff_count`, `report` | 每日对账 |

### 5.3 约束与不变量

- `code`：`UNIQUE(code_type, normalized_value)`。`IM2A_NUMBER` 永远占用原值（含 `RETIRED`/`REVOKED`）；其他类型释放时把 `normalized_value` 改为 `released:<code_id>` 墓碑值，原值由 `code_namespace` 保留，隔离期满后可按策略复用。
- `code`：部分唯一索引 `UNIQUE(subject_id, code_type) WHERE code_type='IM2A_NUMBER' AND status IN ('RESERVED','ACTIVE','SUSPENDED')`，保证一人一个主号码。
- `code`：`status='ACTIVE'` ⇒ `subject_id IS NOT NULL AND activated_at IS NOT NULL`。
- `code`：`status='RESERVED'` ⇒ `expires_at IS NOT NULL`。
- `code`：`normalized_value` 在 live 期间不可更新；`code_type` 永远不可更新；只允许状态/元数据/owner 变更（转移），释放时按上一条改成墓碑值。
- `code_alias`：`alias_value` 在 `ACTIVE` 期间唯一；过期/释放后改为 `released:<alias_id>`，历史由 `code_namespace` 保留；alias 与 code 不得同时以 `LIVE` 占用同一 `namespace_key`；`target_code_id` 必须指向 `code` 行而非另一个 alias，禁止链式/环，解析最多 1 跳。
- `code_reservation`：`idempotency_key` 唯一；同一 subject+type 最多一个 `PENDING`。
- 改名/转移必须在同一事务内完成 `code_namespace` 的让出/占用；不允许先建 alias 再改 code，避免同一 `namespace_key` 出现两个 `LIVE`。
- `code_pool`：`range_start <= next_value <= range_end + 1`；范围不重叠（按 type/region 排他约束）。
- `subject_quota`：`UNIQUE(subject_id, code_type)`；`used <= limit`、`reserved <= max_reserved` 由事务和 `CHECK` 共同保证。
- `credential`：`token_id` 唯一；`used_count <= max_uses`；`expires_at > created_at`；`secret_hash` 不可逆。
- `binding`：`token_hash` 唯一；一个设备同一 subject 最多一个 `ACTIVE` 绑定（部分唯一索引）。
- 码/配额/审计表不使用硬删除；幂等键、过期凭据哈希、短期日志可按保留策略删除。公开码只做状态迁移。

**关键索引：**

- `code (subject_id, status, code_type)`：我的码列表。
- `code (owner_subject_id, code_type, status)`：Agent 子码配额/列表。
- `code (status, expires_at)`：预留/明码清扫。
- `code_namespace (status, updated_at)`：释放/复用扫描。
- `code_reservation (status, expires_at)`：过期清扫。
- `credential (subject_id, status)`、`credential (expires_at)`：凭据列表/清理。
- `code_event_outbox (status, next_retry_at)`：outbox 投递。
- 审计表按 `created_at` BRIN + `(actor_id, created_at)`、`(target_id, created_at)`。

### 5.4 状态机

**公开码（号码/用户码/子码/明码）：**

`RESERVED` → `ACTIVE`（确认/支付成功）
`RESERVED` → `EXPIRED`（TTL 到期）
`RESERVED` → `CANCELLED`（用户取消/管理员取消）
`ACTIVE` → `SUSPENDED`（滥用/丢失设备/风控，可恢复）
`SUSPENDED` → `ACTIVE`（申诉成功/管理员恢复）
`ACTIVE` → `QUARANTINED`（释放/删除/转移，隔离期）
`QUARANTINED` → `ACTIVE`（仅同 owner，隔离期满，需审计）
`ACTIVE`/`SUSPENDED`/`QUARANTINED` → `RETIRED`（永久退休/主体删除）
`ACTIVE`/`SUSPENDED` → `REVOKED`（严重违规/法务，公开码终态）

**预留：**

`PENDING` → `PAID`（支付成功）→ `CONFIRMED`（确认成功）
`PENDING` → `EXPIRED`（TTL）/ `CANCELLED`（用户/管理员取消）
`PAID` → `REFUNDED`（硬超时/确认失败，退款完成）
`PAID` 不自动过期；超时告警/人工。

**授权码：**

`ACTIVE` → `USED`（原子消费）
`ACTIVE` → `EXPIRED`（TTL）
`ACTIVE` → `REVOKED`（主动/管理员/风控）
`ACTIVE` → `LOCKED`（失败次数超限）→ `ACTIVE`（锁定到期）或 `REVOKED`

**关键限制：**

- `REVOKED`/`RETIRED` 的公开码不可回到 `ACTIVE`；如属误操作，只能走双人审批的“纠错”流程并留审计，不能普通恢复。
- 跨主体复用不是把旧行恢复为 `ACTIVE`，而是旧行永久 `RETIRED`、新建 `code` 行并复用 `code_namespace` 的值；旧行保留用于审计和 404 墓碑。
- 号码不因吊销/注销/释放重新分配给另一个 subject；显式转移是唯一例外（双方同意 + 双人审批 + 审计）。用户码/子码/明码只在隔离期满后按规则处理。
- 状态迁移必须带 `version` CAS 和审计事件；批量迁移走后台任务，逐条审计。

### 5.5 Redis Key 设计

| Key | 值 | TTL | 用途 |
|---|---|---|---|
| `res:v1:{type}:{normalized}` | 公开投影 JSON + `code version` | 300s | 解析缓存 |
| `res:neg:v1:{type}:{hash}` | `1` | 30s | 负缓存，防穿透 |
| `res:ver:{code_id}` | 版本号 | 1h | 缓存版本比对 |
| `rev:epoch` | 整数 | 永久 | 全局吊销纪元；变更即递增 |
| `rl:{scope}:{subject}` | token bucket | 窗口 | 限流 |
| `alloc:buf:{node}:{pool}` | list | 10m | 发号本地缓冲 |
| `lock:reservation:{id}` | token | 30s | 仅 advisory，不做事源 |
| `cred:fail:{credential_id}:{ip_hash}` | 计数 | 15m | 凭据+来源锁定；PG 仍为准 |
| `idem:{key}` | 响应哈希 | 24h | 幂等快路径；PG 持久化 |
| `pool:remaining:{pool}` | 整数 | 60s | 号池余量观测 |

规则：key 带环境/区域前缀；不存明文授权码；不存 PII；IP 只存日盐哈希；Redis 丢失去回源 Postgres，不允许“只存在 Redis”的业务状态。

### 5.6 容量与分区

- 1M 人 ×（1 号码 + 1 用户码）+ 3M Agent × 1 子码 + 5M 明码 ≈ 10M 行；3 年目标 100M 行。
- `code` 单表 + `code_type` 前导索引可支撑 10M；超过 50M 按 `code_type` LIST 分区或按 `created_at` 月分区。
- `code_event_outbox`、`credential_use`、`audit` 按月分区；outbox 保留 7 天，审计 7 年。
- 读副本承担解析回源；主库只承担写和强一致读。
- 连接池：PgBouncer transaction mode；Resolver 独立只读连接池，最大连接数按副本 capacity 的 60% 配置。

---

## 6. 发号器

### 6.1 四种分配模式

| 模式 | 适用 | 做法 | 冲突处理 |
|---|---|---|---|
| `SEQUENTIAL` | 普通号码 | 号池高水位 + 号段租约 + 本地缓冲 | 唯一索引兜底；断号接受 |
| `RANDOM` | 隐私敏感的号码/明码 | CSPRNG 在范围内取候选，插入；冲突重试 | 最多 5 次，指数退避；仍失败则换号池 |
| `VANITY_ONLY` | 靓号/自选用户码 | 用户提交候选，校验后直接插入 | 唯一索引仲裁；失败返回 409 + 建议 |
| `IMPORTED` | 迁移/批量导入 | 逐行插入，文件哈希幂等 | 行级错误报告；不中断整批 |

### 6.2 号段租约（顺序模式）

1. 节点启动或本地缓冲低于 20% 时，向 Postgres 申请一个 block（默认 1,000）。
2. 事务内对 `code_pool` 行做 `SELECT ... FOR UPDATE`，把 `next_value` 前进 block，返回 `[start, end]`，插入 `code_lease`，提交。
3. 节点把 block 放入本地内存队列；发号时取一个值，计算校验位，写 `code` 行。
4. 节点崩溃：未发出的值成为永久空洞；`code_lease` 标记 `ABANDONED`，**不重新租给其他节点**。这是“允许断号、不允许重号”的核心取舍。
5. 号池剩余低于阈值时告警；`DRAINING` 状态停止新租约，只消耗现有缓冲。

**为什么不在 Redis 里发号？** Redis 的持久化和主从切换不能保证“绝不重号”；Postgres 唯一索引 + 单行高水位才是权威。Redis 只做缓冲，不做事实源。

### 6.3 随机码

- 明码/随机号码：CSPRNG 生成候选 → 归一化 → 校验位 → 插入 `code`；唯一冲突则重试。
- 随机号码在 `code_pool` 范围内取值，避免与顺序号段重叠；范围用排他约束保证。
- 明码 50 bit 数据空间，10M 总量碰撞期望约 4%，重试即可；超过 10M 提升到 11 位数据（55 bit），作为新码型版本，不混用旧长度。
- 随机生成必须避免模偏差：拒绝采样，不用 `random % range`。

### 6.4 凭据发号

- 令牌格式 `im2a_ac_<token_id>_<secret>`：`token_id` 80 bit 随机（16 位 Crockford Base32），`secret` 130 bit CSPRNG（26 位）；不做顺序/时间编码。
- 服务端按 `token_id` 查行，计算 `HMAC-SHA256(pepper, secret)` 并常量时间比较；`secret_hash` 落库，明文不落库。
- 明文只在签发响应返回一次；不落库、不入日志、不入事件、不入缓存。
- 轮换 pepper 时保留 `pepper_version`；旧 token 用旧版本验证，逐步自然过期。

### 6.5 失败、重试、断号

- 发号事务失败：已取出的值视为烧毁（断号），不回滚到池子；避免“回滚后另一个事务拿到同号”。
- 写 `code` 行冲突：顺序号段理论上不会冲突；若冲突，说明号池范围重叠或数据被污染，立即告警并停止该池发号，人工对账。
- 批量导入：按文件哈希幂等；重复行、非法行、黑名单行进入错误报告；可 dry-run。
- 所有发号写 `code.issued` 事件；事件失败不回滚发号，靠 outbox 重试。

### 6.6 吞吐与扩展

- 顺序发号：本地缓冲命中时纯内存 + 一次 INSERT，单节点可到数千/秒；瓶颈在 Postgres 写事务，必要时按号池分片到多个主库（按 region/pool），但同一号池单写。
- 随机发号：每次 INSERT，冲突率低；批量场景用多值 INSERT + `ON CONFLICT DO NOTHING` + 重试。
- 水平扩展：无状态节点 + 各自租约；号池是天然分片键；不要做全局单点发号器。
- 顺序号段大小：1,000 是默认；节点多、重启频繁时增大到 10,000，减少租约次数，代价是断号空洞变大。

### 6.7 多节点与多区域

- 同一号池同一时刻只有一个写主；多节点通过号段租约拿到不相交区间。
- 多区域：每个 region 独立号池，号码不跨区分配；全局唯一由“region + 号池范围”保证。未来若要全球单号池，必须单写主 + 跨区只读，不做 active-active 发号。
- 时钟：TTL/过期一律用数据库 `now()`；应用时钟只用于日志/追踪；NTP 偏差 > 1s 告警。
- fencing：`code_lease.lease_id` + `version` 写入审计；过期节点恢复后不得继续使用旧缓冲，启动时清空本地缓冲并重新租约。

---

## 7. API

### 7.1 通用约定

- 契约：OpenAPI 3.1（`openapi/identifier-service.yaml`）+ AsyncAPI（`asyncapi/code-events.yaml`）；CI 做 breaking-change 检查，客户端 SDK 由契约生成。
- 版本：`/v1`；公开面 `/v1/public`，自助面 `/v1/me`，管理面 `/v1/admin`，内部面 `/internal/v1`。
- 认证：
  - 公开解析：匿名或 API key（更高限额）；不接收 Cookie。
  - 自助：OIDC JWT，`sub` = `subject_id`；服务端只信 `sub`，忽略 body 里的 `subject_id`。
  - 内部：mTLS + workload identity + 短时 service JWT，`aud` 明确。
  - 管理：SSO + MFA + IP allowlist/VPN；破坏性操作双人审批。
- 幂等：所有创建/变更 POST 支持 `Idempotency-Key`；同 key 不同 body → `409 IDEMPOTENCY_CONFLICT`；保留 24h；`IN_PROGRESS` 超 30s 允许接管，防进程崩溃卡死。
- 并发：PATCH 用 `If-Match`/ETag；服务端返回 `version`；不匹配 → `412 VERSION_CONFLICT`。
- 分页：cursor 分页，`limit` 最大 100；禁止 offset 深分页。
- 错误信封：`error.code` 稳定、`error.message` 可读、`error.request_id` 可追踪；不返回内部堆栈/SQL。
- 追踪：`X-Request-ID` 透传；W3C `traceparent`；响应回带 `X-Request-ID`。
- 缓存：公开解析 `Cache-Control: public, max-age=60, stale-while-revalidate=30`；私密接口 `no-store`。
- 内容类型：JSON；二维码/名片页单独 content-type。
- CORS：公开解析仅白名单 GET；自助/管理面严格 Origin；凭据面不配置浏览器 CORS，只允许服务端调用。
- 弃用：至少 6 个月；`Deprecation`/`Sunset` header；契约测试锁定。

### 7.2 Public Resolver（公开面）

| 方法 | 路径 | 鉴权 | 幂等 | 用途 |
|---|---|---|---|---|
| GET | `/v1/public/resolve/{code}` | 匿名/API key | 否 | 单个公开码解析 |
| POST | `/v1/public/resolve:batch` | API key | 是 | 批量解析，最多 100 |
| GET | `/v1/public/availability` | 匿名 | 否 | 用户码/明码可用性 |
| GET | `/v1/public/cards/{code}` | 匿名 | 否 | 名片页/OG meta |
| GET | `/v1/public/qr/{code}` | 匿名 | 否 | 二维码 PNG/SVG |
| GET | `/v1/public/health` | 匿名 | 否 | 健康检查 |

**解析响应字段（白名单）：**

`code`、`code_type`、`display_name`、`avatar_url`、`kind`（HUMAN/AGENT/ORG）、`verified`（认证徽章，不是等级）、`status`（公开面只返回 ACTIVE；SUSPENDED/REVOKED 统一 404，所有者自查可见真实状态）、`accepting_messages`（`OPEN`/`REVIEW`/`CLOSED`，粗粒度）、`agent`（`agent_kind`、`capabilities_summary`、`agent_card_url`）、`profile_url`、`moved_to`（别名宽限期）、`updated_at`、`cache_ttl`。

**可见性语义：** `PUBLIC` 可解析、可被公开名片/搜索引用；`UNLISTED` 仅精确码可解析、不进入搜索/目录；`PRIVATE` 公开面 404，只用于 owner 内部或已建立的关系通道。

**禁止返回：** `subject_id`、`code_id`、owner ID、邮箱/手机号、账号等级、配额、信任等级、是否拉黑你、精确解析次数、IP、内部状态机细节。

**批量解析：** 200 + 每项 `{code, status, result?}`；单次最多 100；每 API key 10 次/分钟；错误项不导致整批失败。批量接口必须鉴权，防止公开爬取。

**可用性查询：** `type` + `value`；返回 `available` + `reason`（`INVALID`/`RESERVED`/`TAKEN`/`BLOCKED`）；`TAKEN` 不返回 owner；匿名 30 次/分钟，超过触发验证码。

### 7.3 自助面（用户/组织）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/v1/me/codes` | 我的码列表（号码/用户码/子码/明码） |
| GET | `/v1/me/codes/{code_id}` | 我的码详情（含真实状态） |
| POST | `/v1/me/codes/claims` | 直接领号（系统分配，无自选） |
| POST | `/v1/me/codes/reservations` | 预留自选码/靓号 |
| POST | `/v1/me/codes/reservations/{id}:confirm` | 确认预留（可带支付引用） |
| DELETE | `/v1/me/codes/reservations/{id}` | 取消预留 |
| PATCH | `/v1/me/codes/{code_id}` | 改可见性/展示偏好（不改码值） |
| POST | `/v1/me/codes/{code_id}:rotate` | 轮换明码（仅 PUBLIC_ALIAS） |
| POST | `/v1/me/codes/{code_id}:release` | 释放子码/明码（号码不可释放） |
| POST | `/v1/me/codes/{code_id}:suspend` | 挂失/暂停（立即生效） |
| POST | `/v1/me/codes/{code_id}:resume` | 恢复 |
| POST | `/v1/me/codes/{code_id}:transfer` | 申请转移（T3+，需审批） |
| GET | `/v1/me/codes/{code_id}/history` | 我的审计子集 |

**预留请求字段：** `code_type`、`preferred_value`（可选）、`pool`（可选）、`ttl_seconds`（受等级上限约束）、`idempotency_key`。响应：`reservation_id`、`code`、`display_value`、`expires_at`、`status`、`price_cents`（如有）。

**确认请求：** `payment_ref`（靓号必填）、`idempotency_key`。确认是 CAS：只有 `PENDING`（免费）或 `PAID`（付费）且未过期才能成功；过期 → `409 RESERVATION_EXPIRED`；已确认 → 幂等返回原结果。已支付预留取消走退款流程，不直接释放码。

**直接领号：** 只允许 `IM2A_NUMBER` 的普通池；服务端分配；同一 subject 已有活跃号码 → `409 QUOTA_EXCEEDED`；同一 `Idempotency-Key` 的重试返回原结果，不重复发号。

### 7.4 Agent 子码

| 方法 | 路径 | 用途 |
|---|---|---|
| PUT | `/v1/agents/{agent_id}/subcode` | 幂等分配子码（可带 `slot`） |
| GET | `/v1/agents/{agent_id}/subcode` | 查询当前子码 |
| POST | `/v1/agents/{agent_id}/subcode:rotate` | 仅泄露/被冒用时轮换 |
| DELETE | `/v1/agents/{agent_id}/subcode` | 释放，进入隔离 |

规则：调用方必须是 Agent owner 或有 `agents:write` 服务权限；slot 在 owner 范围内唯一；配额由等级决定；轮换后旧子码进入 tombstone，解析 404，不自动跳转；删除 Agent 不自动删除子码，先隔离。

### 7.5 凭据面（私密）

| 方法 | 路径 | 鉴权 | 用途 |
|---|---|---|---|
| POST | `/v1/credentials` | 用户 JWT + 步进认证 | 签发授权码 |
| POST | `/internal/v1/credentials:verify` | 内部 mTLS | 校验授权码 |
| POST | `/v1/credentials/{id}:revoke` | 用户/内部/管理员 | 吊销 |
| POST | `/v1/credentials/{id}:rotate` | 用户/内部 | 轮换（旧码短宽限） |
| GET | `/v1/credentials` | 用户 JWT | 元数据列表（不含明文） |
| POST | `/v1/bindings` | 授权码 + 设备信息 | 创建长期绑定 |
| POST | `/v1/bindings/{id}:rotate` | 用户/内部 | 轮换 binding token |
| DELETE | `/v1/bindings/{id}` | 用户/管理员 | 解绑 |

**签发请求：** `purpose`、`target_code`（可选）、`scopes`、`ttl_seconds`（受等级/用途上限）、`max_uses`（默认 1）、`step_up_token`。响应：`credential_id`、`token`（仅此一次）、`expires_at`、`scopes`、`purpose`。`Cache-Control: no-store`；日志只记 `credential_id` 前 8 位，不记 token。

**校验请求：** `token`、`purpose`、`target_code`（可选）、`context`（设备、IP、nonce、user_agent）。响应：`valid`、`subject_id`、`scopes`、`binding_token`（如创建绑定）。失败统一 `CREDENTIAL_INVALID`，不区分“不存在/过期/已用/错 scope”，避免 oracle；内部审计记录细分原因。

**约束：** 授权码只接受 POST body；不接受 query/path；不接受 GET；不在错误信息回显；校验接口只在内网；边缘缓存禁止。

**幂等与一次性明文的冲突：** 授权码签发/轮换/绑定创建是幂等的例外——`idempotency_record` 只存 `credential_id`/`binding_id`、purpose、expires_at 和请求哈希，不存 token；同一 key 重试返回 `409 CREDENTIAL_ALREADY_ISSUED` 或 `409 BINDING_ALREADY_CREATED`（可带 `credential_id` 元数据，不含 token），客户端应重新签发/轮换（旧码可吊销），绝不为了幂等而二次返回明文。

### 7.6 管理面

| 方法 | 路径 | 用途 | 审批 |
|---|---|---|---|
| POST/GET | `/v1/admin/pools` | 创建/查询号池 | 创建需审批 |
| POST | `/v1/admin/pools/{id}:import` | 批量导入 | 是 |
| GET | `/v1/admin/imports/{job_id}` | 导入进度/错误报告 | 否 |
| POST | `/v1/admin/pools/{id}:quarantine` | 法务/风控隔离号段 | 是 |
| GET/POST | `/v1/admin/blocklist` | 黑名单管理 | 删除需审批 |
| GET | `/v1/admin/codes` | 搜索/筛选（脱敏） | 否 |
| POST | `/v1/admin/codes/{id}:revoke` | 强制吊销 | 双人 |
| POST | `/v1/admin/codes/{id}:transfer` | 强制转移 | 双人 |
| POST | `/v1/admin/codes/{id}:recycle` | 隔离期满回收 | 双人 |
| POST | `/v1/admin/policy/tiers:simulate` | 策略影响模拟 | 否 |
| POST | `/v1/admin/policy/tiers:publish` | 发布新版本 | 双人 |
| GET | `/v1/admin/audit` | 审计导出/流式 | 否（只读） |
| POST | `/v1/admin/reconciliation:run` | 触发对账 | 否 |
| GET | `/v1/admin/reconciliation/{run}` | 对账报告 | 否 |
| GET/POST | `/v1/admin/approvals` | 审批单列表/批准 | 批准人不能是申请人 |

管理面独立 host；不共享公开面 Cookie；所有响应脱敏；导出走异步任务 + 签名 URL；审计日志不可改。

### 7.7 内部面

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/internal/v1/codes:allocate` | 账号开通时分配号码/用户码 |
| GET | `/internal/v1/codes/{id}` | 窄字段查询 |
| GET | `/internal/v1/codes/{id}:binding-view` | 凭据服务校验目标码 |
| POST | `/internal/v1/subjects/{id}:merge` | 账号合并，码转移/别名 |
| GET | `/internal/v1/revocations?since=` | 边缘吊销增量同步 |

内部接口只接受 mTLS；调用方必须在服务目录注册；所有请求带 `X-Request-ID` 和调用方 `service_id`；禁止公网 ingress。

### 7.8 错误码

| 错误码 | HTTP | 含义 |
|---|---|---|
| `CODE_INVALID` | 400 | 语法/校验位/长度错误 |
| `CODE_NOT_FOUND` | 404 | 不存在/不可见/已吊销（统一） |
| `CODE_TAKEN` | 409 | 已被占用 |
| `CODE_RESERVED` | 409 | 保留字/黑名单 |
| `CODE_EXPIRED` | 409 | 码/别名过期 |
| `CODE_SUSPENDED` | 423 | 暂停（仅自助/管理面；公开面统一 404） |
| `QUOTA_EXCEEDED` | 409 | 超出等级配额 |
| `TIER_FORBIDDEN` | 403 | 等级不允许该操作 |
| `RESERVATION_EXPIRED` | 409 | 预留过期 |
| `RESERVATION_CONFLICT` | 409 | 并发预留冲突 |
| `CREDENTIAL_INVALID` | 401 | 授权码无效（统一） |
| `CREDENTIAL_ALREADY_ISSUED` | 409 | 同幂等键重试，不重放明文 |
| `BINDING_ALREADY_CREATED` | 409 | 同幂等键重试，不重放 binding token |
| `CREDENTIAL_LOCKED` | 423 | 尝试过多，锁定 |
| `CREDENTIAL_SCOPE_MISMATCH` | 403 | 仅内部审计/管理面细分；对外统一 `CREDENTIAL_INVALID` |
| `IDEMPOTENCY_CONFLICT` | 409 | 同 key 不同 body |
| `VERSION_CONFLICT` | 412 | ETag/版本冲突 |
| `RATE_LIMITED` | 429 | 限流；带 `Retry-After` |
| `DEPENDENCY_UNAVAILABLE` | 503 | 依赖不可用；可重试 |
| `ADMIN_APPROVAL_REQUIRED` | 409 | 需要双人审批 |

### 7.9 限流

| 接口 | 匿名/IP | 用户/主体 | API key |
|---|---|---|---|
| 单码解析 | 60/min | 600/min | 6,000/min |
| 批量解析 | 禁止 | 10/min | 100/min（每次 ≤100） |
| 可用性查询 | 30/min | 60/min | 600/min |
| 预留/领号 | — | 10/h，3 个并发预留 | — |
| 授权码签发 | — | 5/h，20/天 | — |
| 授权码校验 | — | 5/min/credential | 20/min/IP |
| 管理面 | — | — | 100/min + 审批 |

实现：Redis token bucket；本地 fallback；`429` 带 `Retry-After` 和 `X-RateLimit-*`；对批量/异常 ASN 加验证码或人工审核。

### 7.10 关键流程

**A. 自选用户码（无支付）**

`GET availability` → `POST reservations`（创建 `RESERVED` 码 + PENDING 预留）→ 用户确认 → `POST :confirm`（CAS 到 ACTIVE，更新 quota，写 outbox）→ 返回。超时未确认 → 清扫器置 `EXPIRED`，码回池（仅未激活）。

**B. 靓号（有支付）**

`POST reservations` → 返回价格和 `expires_at` → 支付 → 支付回调带 `payment_ref` → `POST :confirm`（校验金额/币种/支付单归属）→ ACTIVE。确认失败可重试；预留过期但支付成功 → 自动退款/挂账，人工工单；不允许“支付成功但码被抢”。

**C. 设备绑定**

用户步进认证 → `POST /v1/credentials`（scope=`bind:device`，TTL 10 分钟）→ 客户端展示一次 → 客户端 `POST /v1/bindings`（token + device，用户 JWT）→ Credential Service 内部校验并原子消费 → 返回 binding token。授权码泄露窗口只有 10 分钟且单次；客户端不直接调用内部校验接口。

**D. Agent 子码**

创建 Agent → `PUT subcode`（配额内分配）→ 公开解析可见 Agent 名片 → 陌生人发消息仍走信任/审批/付费流程；子码不授予通信权。

**E. 公开解析**

`GET /v1/public/resolve/1234567897` → CDN/Resolver → Redis → 只读副本 → 公开投影。若 `rev:epoch` 变化或缓存 `version` 落后 → 回源；吊销码 404；别名宽限期 200 + `moved_to`。

---

## 8. TTL、回收与生命周期

### 8.1 TTL 矩阵

| 对象 | 默认 TTL | 可否续期 | 到期动作 | 是否回收 |
|---|---|---|---|---|
| 号码预留 | 默认 10 min（按等级 5 min–24 h） | 可续 1 次 | `EXPIRED`，码回池（未激活） | 是（仅未激活） |
| 已支付待确认 | 15 min（硬上限 2 h） | 不自动续 | 告警/人工；不自动回池 | 否 |
| 用户码预留 | 10 min | 可续 1 次 | 同上 | 是（仅未激活） |
| 明码 | 365 天 | 可续期 | `EXPIRED`，解析 404；1 年后可再分配 | 是（隔离后） |
| Agent 子码 | 随 Agent | 不适用 | `QUARANTINED` 90 天 | 仅同 owner 可再分配 |
| 授权码 | 5–30 min（T4 可到 60） | 否 | `EXPIRED` | 否（随机值，无回收意义） |
| 授权码锁定 | 15 min | 否 | 自动解锁 | 不适用 |
| 幂等键 | 24 h | 否 | 删除 | 不适用 |
| 公开解析缓存 | 5 min | 不适用 | 回源 | 不适用 |
| 负缓存 | 30 s | 不适用 | 回源 | 不适用 |
| 吊销纪元 | 永久 | 不适用 | 递增 | 不适用 |
| 审计 | 7 年 | 不适用 | 归档 WORM | 不适用 |

### 8.2 回收原则

1. **号码一旦 `ACTIVE` 永不回收（显式转移除外）；用户码/子码/明码按隔离规则受限复用。** 可立即回池的只有从未激活的预留；任何复用都必须有 tombstone、审计和适用限制。
2. **号码是永久地址。** 用户注销 → `RETIRED` tombstone；解析 404；号码不因注销重新分配（显式转移除外）。这样旧名片、旧二维码不会指向新人。
3. **用户码改名 → 别名期 90 天 → 隔离 180 天。** 隔离期内不可被他人申请；高价值/品牌码永久保留。隔离期满后可释放给其他主体，但旧码必须 404（不做自动跳转），并做混淆/品牌复核。
4. **Agent 子码删除 → 隔离 90 天。** 只允许同一 owner 重新激活；跨 owner 永久不分配，除非产品明确接受“历史子码指向新 Agent”的风险（不推荐）。
5. **明码轮换 → 旧码宽限 30 天 → 404 → 隔离 1 年。** 明码本来就是可轮换分享码，回收风险低于号码，但仍要防止旧海报指向新主体。
6. **授权码不回收。** 随机 130 bit，回收没有收益；过期即失效，哈希按保留策略删除。
7. **任何回收都必须有审计和原因码；批量回收需双人审批。**

### 8.3 清扫器

| 任务 | 频率 | 批量 | 逻辑 |
|---|---|---|---|
| 预留过期 | 30s | 500 | `SKIP LOCKED` 扫描 `PENDING` 且过期；CAS 置 `EXPIRED`；释放码；发事件；`PAID` 不自动过期，超 15 min 告警；禁止单独按 `code.expires_at` 清扫 `RESERVED` |
| 明码过期 | 1h | 1,000 | 置 `EXPIRED`；发事件；清缓存 |
| 子码隔离到期 | 每日 | 500 | 同 owner 可恢复的置 `QUARANTINED` 结束；跨 owner 保持 tombstone |
| 授权码过期 | 1h | 5,000 | 置 `EXPIRED`；保留哈希到保留期后删除 |
| 别名过期 | 1h | 1,000 | 置 `EXPIRED`；解析 404 |
| 别名隔离到期 | 每日 | 500 | `code_namespace` 置 `RELEASED`；按策略允许复用；审计 |
| 租约超时标记 | 5min | — | 超过 `expires_at` 的 `ACTIVE` 租约置 `ABANDONED`；不回收号段 |
| 号池余量 | 5min | — | 计算余量；70/85/95% 告警；`DRAINING` |
| 对账 | 每日 02:00 | — | 见 11.5 |
| outbox 重试 | 连续 | 500 | 指数退避；超过 10 次进 DLQ |

所有任务必须幂等、可 dry-run、有指标；清扫器不依赖应用时钟，用 DB `now()`；多实例用 `SKIP LOCKED` 或 leader election，避免重复处理。

### 8.4 删除与隐私

- 用户注销：号码 `RETIRED`，用户码 `RETIRED`/别名，子码 `RETIRED`，明码 `REVOKED`；公开名片 tombstone；授权码全部吊销；PII 按隐私政策删除/匿名化。
- 审计保留 7 年；授权码哈希在过期/吊销 30 天后删除；IP 哈希 30 天；原始 IP 仅安全事件依法保留。
- 数据主体请求：导出码列表/审计；删除请求走统一隐私流程；法律保留（欺诈/支付/诉讼）例外。

---

## 9. 并发与冲突

### 9.1 抢占同一个码

- 不做“先查再插”。直接 `INSERT`，由 `UNIQUE(code_type, normalized_value)` 仲裁。
- 失败方返回 `409 CODE_TAKEN`，附带 3 个可用建议（同前缀/加数字），不暴露占用者。
- 高并发下唯一索引冲突是正常路径；指标记录冲突率，超过 5% 说明前端可用性检查失效或有人恶意刷。
- 预留与直接领号共用同一唯一索引；预留行本身就是 `code` 的 `RESERVED` 记录，避免“预留表有、码表没有”的双写不一致。

### 9.2 预留过期 vs 确认

- 确认用 CAS：`UPDATE ... WHERE code_id=? AND status='RESERVED' AND expires_at > now() AND reservation_id=? AND version=?`；免费码要求 reservation `PENDING`，付费码要求 `PAID`，由应用层校验 `price_cents`/`payment_ref`。
- 清扫器只处理 `PENDING` 过期；`PAID` 不自动回池，超 15 min 告警并转人工。
- 确认失败返回 `409 RESERVATION_EXPIRED`；客户端可重新预留，不能“复活”旧预留。
- 支付成功但确认失败：预留置 `PAID` 并延长 15 min，重试确认；硬超时（2 h）后退款/人工，不允许把已支付订单直接改成 ACTIVE 绕过 CAS。

### 9.3 配额并发

- `subject_quota` 行原子更新：`used = used + 1 WHERE used < limit`；返回 0 行则 `409 QUOTA_EXCEEDED`，整个事务回滚。
- 预留同时占 `reserved` 配额；确认时 `reserved-1, used+1`；过期时 `reserved-1`。
- 夜间对账用 `COUNT(*)` 校正 `used/reserved`；差异告警，不自动改生产数据（需审批）。
- 转移在同一事务内更新 `code.subject_id`、双方 `subject_quota`、写审计和 outbox；不改变 `code_id`/`normalized_value`；转移审批单必须先 APPROVED。

### 9.4 授权码消费

- 先按 `token_id` 查行；未知 token_id 走 dummy HMAC；再做 HMAC 常量时间比较，最后原子消费；避免时序 oracle。
- 校验通过后原子消费：`used_count = used_count + 1 WHERE credential_id=? AND used_count < max_uses AND expires_at > now() AND status='ACTIVE'`；返回 0 行则拒绝。
- 锁定按 credential + IP/设备 计数，避免攻击者用已知 token_id 对某凭据做全局锁死；owner/管理员可解锁。
- 两个并发请求只有一个成功；失败方统一 `CREDENTIAL_INVALID`（内部审计记 `USED`）。
- 绑定创建与消费在同一事务/同一幂等键下；重复提交返回同一 binding，不重复消费。

### 9.5 缓存一致性

- 写路径：Postgres 提交 → outbox 事件 → 消费者删除/更新缓存；同时 TTL 兜底。
- 读路径：缓存项带 `code version`；命中后先查本地吊销过滤器/吊销集合，再返回；版本落后则回源。
- 吊销：递增该码 `version`、删除该码缓存、加入吊销集合并递增 `rev:epoch`；边缘节点每 5 秒拉取增量，不逐请求比较全局 epoch，避免一次吊销导致全量缓存回源。公开解析可容忍秒级延迟，授权校验不接受缓存。
- v1 可用全局 `rev:epoch` 做简单失效；吊销频繁/缓存规模大时，改为 per-code version + 吊销集合/布隆过滤器。
- 缓存击穿：单飞锁 + 逻辑过期；负缓存 30 秒；批量接口不走 CDN 缓存。
- 不允许“先删缓存再写库”；不允许把 Redis 当唯一状态。

### 9.6 支付 saga

1. 预留（库存锁定，`PENDING`）→ 2. 创建支付单（带 `reservation_id`）→ 3. 支付成功回调（`PENDING → PAID`，延长 15 min）→ 4. 确认码（`PAID → CONFIRMED`）→ 5. 失败补偿/退款。
- 每一步幂等：`payment_ref`、`reservation_id`、`Idempotency-Key` 三元组。
- 预留过期但支付成功：自动退款；若退款失败，挂账 + 工单；不得把码给未确认的订单。
- 支付成功但确认超时：重试确认；仍失败则人工介入，预留保持 `PAID`（不自动回池，硬上限 2 h），避免被清扫器抢走。
- 支付/码服务不在同一事务；用 saga + 事件，不用分布式事务。

### 9.7 多区域

- 同一码命名空间单写；跨区只读副本；解析可本地读，写回源。
- 全球唯一：region 前缀 + 号池范围；或全球单号池单写主。v1 选前者。
- 冲突处理：跨区不做 active-active 写；若未来要做，必须按码哈希分片，每片单写，且客户端路由到 owner region。
- 灾难切换：提升只读副本前，先确认没有未完成写/租约；切换后旧主必须 fencing，防止双写。

---

## 10. 等级权限

### 10.1 等级模型

- 账号等级 `T0`–`T4`，由订阅/认证/组织合同决定；与“信任等级”（熟人/陌生人/家庭）完全分开。
- 等级影响“能拥有什么码、能怎么用”，不影响“谁能联系你”。
- 所有额度来自 `policy_tier_version`，可版本化、可模拟、可灰度；不写死在代码。
- 签发时记录 `tier_at_issue`；降级不追溯吊销，使用 `grandfathered` 保留既有码。

### 10.2 权限矩阵（建议默认值，非最终定价）

| 能力 | T0 未验证 | T1 基础 | T2 认证 | T3 专业 | T4 组织 |
|---|---|---|---|---|---|
| IM2A 号码 | 1 个临时（30 天） | 1 个永久 | 1 个永久 | 1 个 + 1 备用 | 组织号 + 成员号 |
| 用户码 | 不可 | 1 个（注册满 7 天） | 1 个 | 1 个 + 2 个别名 | 组织命名空间 |
| Agent 子码 | 0 | 1 | 5 | 20 | 100/组织 |
| 明码 | 1 个（7 天） | 3 个 | 10 个 | 50 个 | 500 个 |
| 授权码 TTL 上限 | 5 min | 10 min | 15 min | 30 min | 60 min |
| 自选/靓号 | 否 | 否 | 有限（付费） | 是（付费） | 是（合同） |
| 预留时长 | 5 min | 10 min | 30 min | 60 min | 24 h |
| 转移 | 否 | 否 | 否 | 申请 + 人工 | 合同 + 人工 |
| 号码隐藏（UNLISTED） | 否 | 是 | 是 | 是 | 是 |
| 批量解析 | 10/min | 100/min | 1,000/min | 10,000/min | 合同 |
| 审计导出 | 否 | 否 | 90 天 | 1 年 | 合同 |

T0 的“临时号码”是试用号，30 天到期转 `RETIRED`，不回收、不转正；转正需升级并申请新号或按运营规则保留。所有数字均为建议默认值，最终以策略版本为准。

### 10.3 执行点

| 执行点 | 检查 | 失败行为 |
|---|---|---|
| 领号/预留 | 等级、配额、码型、长度、靓号权限、预留 TTL | `403 TIER_FORBIDDEN` / `409 QUOTA_EXCEEDED` |
| 确认 | 预留归属、支付状态、配额 | `409`，不部分成功 |
| Agent 子码 | owner 配额、Agent 归属、slot 规则 | `403`/`409` |
| 明码轮换 | 等级、数量、TTL | `403` |
| 授权码签发 | 等级 TTL 上限、scope、步进认证 | `403`/`401` |
| 公开解析 | 只检查 `visibility`/状态，不检查等级 | 统一 404 |
| 管理操作 | 管理员角色 + 审批 | `409 ADMIN_APPROVAL_REQUIRED` |

### 10.4 降级与升级

- **升级**：立即生效；不要求换号；新配额可立即使用。
- **降级**：已有码不删除；`grandfathered=true`；新领号/新子码被阻止；超出配额的子码给 30 天只读宽限，之后 `SUSPENDED`（可导出，不删除）；明码到期不续；通知用户。
- **欠费**：不立即吊销公开码（地址稳定性优先）；限制新增和部分管理功能；严重滥用才 `SUSPENDED`。
- **组织转个人/个人转组织**：走转移审批；号码保留；owner 变更；审计；公开名片更新。

### 10.5 等级 ≠ 信任

- 等级高的人不能因此给陌生人发消息；信任等级/同意/付费流程仍然生效。
- 等级低的人可以被家人/朋友授予高信任；信任不改变码的供给额度。
- 公开解析不返回等级；避免“等级即身份”的社会工程。
- 授权码的 scope 由“等级 + 信任 + 步进认证”共同决定，任何单一维度都不足。

---

## 11. 编码库管理

### 11.1 号池

- 按 `code_type + region + purpose` 建池；池之间范围不重叠。
- 池状态：`DRAFT` → `ACTIVE` → `DRAINING`（停止新租约）→ `EXHAUSTED` → `RETIRED`。
- 每个池有 `allocation_mode`、`lease_block_size`、`min/max`、`reserved_prefixes`、`price_tier`。
- 号池容量看板：余量、消耗速率、预计耗尽天数、异常冲突率；70/85/95% 告警。
- 应急池：预留 `EMERGENCY` 池，只有双人审批可启用；用于事故/法务/大客户。

### 11.2 导入/导出

- 支持 CSV/JSONL；字段：`value`、`type`、`region`、`purpose`、`tier`、`notes`。
- 导入前 dry-run：语法、校验位、重复、黑名单、混淆、范围重叠；输出逐行报告。
- 文件 SHA-256 作为幂等键；同一文件重复导入不重复插入。
- 异步任务；最大 100 万行/批；失败行不阻塞成功行；报告可下载（签名 URL，短期）。
- 导出只导出码值和状态，不导出授权码/密钥/PII；审计导出单独审批。

### 11.3 黑名单与混淆

- 类别：系统保留、冒犯性、品牌/商标、政府/紧急、竞品、法务保留、高价值保护。
- 匹配：精确、前缀、正则、混淆骨架；`BLOCK`/`REVIEW`/`WARN` 三级。
- 多语言冒犯词库；定期更新；品牌方投诉快速下架（双人审批）。
- 混淆：NFKC + casefold + 同形字映射；与活跃高价值码骨架相同 → 拒绝；短码（<8）额外 Levenshtein ≤1 检查。
- 任何黑名单删除都要审批 + 审计；防止“内部人放行靓号”。

### 11.4 策略版本

- 等级策略是数据，不是代码：`DRAFT` → `SIMULATE` → `APPROVE` → `PUBLISH`（带 `effective_from`）。
- 发布前模拟：多少主体会超限、多少子码会进入宽限、多少预留会受影响；输出报告。
- 支持回滚到上一版本；回滚不自动恢复已过期/已吊销的码。
- 策略变更必须通知受影响用户；至少提前 30 天（缩额/收费类变更）。

### 11.5 对账

每日对账检查：

- `code` 与 `code_reservation` 状态一致；
- `subject_quota.used/reserved` 与 `COUNT(*)` 一致；
- 号池高水位与已发码数量/范围一致；
- 无重复 `normalized_value`（唯一索引应已保证）；
- 无孤儿码（subject 不存在/已合并）；
- 无别名环/别名指向已退休码；
- 授权码无过期未清理、无 `used_count > max_uses`；
- outbox 无长期 `PENDING`；replica lag 正常；
- 号码 tombstone 未被复用；其他类型复用必须有 `code_namespace` 的 `RELEASED → LIVE` 记录和审计。

差异只报告 + 告警，不自动改生产；修复走审批脚本/管理 API，逐条审计。

### 11.6 审批与审计

- 双人审批：强制吊销、转移、回收、策略发布、黑名单删除、批量导入、应急池启用。
- 审批单包含：申请人、批准人（不同）、目标、原因、工单号、有效期；过期自动作废。
- break-glass：仅限安全事件；事后 24 小时内补审批 + 复盘；全部审计。
- 审计字段：actor、service、action、target、before/after、request_id、trace_id、ip_hash、approval_id、reason。
- 审计不可变：append-only + 哈希链 + WORM 归档；导出到 SIEM。

---

## 12. 安全

### 12.1 威胁模型（STRIDE 摘要）

| 威胁 | 场景 | 控制 |
|---|---|---|
| Spoofing | 用公开码冒充登录/授权 | 公开码不是凭据；JWT `sub` 用内部 ID；授权码 HMAC；mTLS |
| Tampering | 改码归属/状态 | 唯一约束、CAS、审计、双人审批 |
| Repudiation | 否认发号/吊销 | 不可变审计、审批单、请求 ID |
| Information disclosure | 解析泄露 PII/内部 ID | 最小投影、字段白名单、脱敏 |
| DoS | 解析/枚举打满 | CDN/WAF、限流、负缓存、批量上限、熔断 |
| Elevation | 管理员/服务越权 | 最小权限、独立 role、双人审批、break-glass |
| 枚举 | 顺序号码被扫 | 随机池、限流、验证码、异常检测、无列表接口 |
| 重放 | 授权码被重复使用 | 单次消费、原子更新、绑定 token、nonce |
| 竞态 | 同一码被两人抢到 | 唯一索引、CAS、配额原子更新 |
| 内部人 | 放行靓号/改归属 | 双人审批、审计、职责分离 |
| 供应链 | 依赖/镜像被投毒 | 签名镜像、SBOM、依赖锁定、最小基础镜像 |

### 12.2 授权码处理

- **高熵 + 键控哈希**：`token_id` 80 bit + `secret` 130 bit；按 `token_id` 查行，`HMAC-SHA256(pepper, secret)` 常量时间比较；不用 Argon2 处理高熵随机值（延迟高、收益低）。若未来支持用户自选低熵 PIN，再单独用 Argon2id。
- **常量时间比较**；未知 `token_id` 走 dummy HMAC；错误统一，不泄露存在性。
- **明文只出现一次**：签发响应；不写日志、不写事件、不写缓存、不写数据库、不进 URL。
- **前缀识别**：`im2a_ac_` 便于 secret scanning；CI/日志/仓库扫描该前缀。
- **短 TTL + 单次 + scope + target 绑定**；高风险操作要求步进认证（密码/WebAuthn）。
- **锁定**：按 credential + IP/设备 计数；避免攻击者用已知 token_id 对某凭据全局锁死；owner/管理员可解锁。
- **立即吊销**：状态在 Postgres；校验不缓存；吊销事件 1 秒内生效。
- **pepper 轮换**：KMS 版本化；旧 token 用旧版本校验；自然过期后删除旧版本。pepper 在进程内缓存（KMS 解密后短 TTL/内存保护），避免每次校验调用 KMS；轮换时热更新。
- **绑定 token 分离**：授权码只换一次 binding token；长期 token 有自己的哈希、scope、过期、吊销。

### 12.3 枚举防护

- 顺序号码是可用性与隐私的权衡；默认普通池可用顺序，但必须：单 IP/ASN 限流、批量接口鉴权、无“列出所有码”接口、异常扫描检测、必要时验证码。
- 隐私敏感等级/场景用 `RANDOM` 号池；明码默认随机。
- 可用性查询只返回 `TAKEN`，不返回 owner；高频查询触发挑战。
- 公开解析对不存在/吊销/不可见走同一错误路径和固定开销，避免明显的存在性时序差异；负缓存避免 DB 被打穿。
- 监控：单 IP 404 率、批量调用、ASN 分布、同一前缀扫描；触发 WAF/封禁/人工。

### 12.4 管理面

- 独立域名/端口；SSO + MFA（优先硬件密钥）；IP allowlist/VPN；不共享公开面 Cookie。
- 最小权限角色：`viewer`/`operator`/`approver`/`break-glass`；approver 不能是申请人。
- 所有破坏性操作双人审批；审批单有时效；过期作废。
- 管理 API 不接受公开码作为身份；管理员身份来自 SSO。
- 管理操作限流、审计、告警；异常批量操作自动冻结。

### 12.5 隐私与合规

- 数据最小化：码表不存邮箱/手机号/姓名；公开资料由 Profile 服务提供。
- IP 只存日盐哈希；原始 IP 仅安全事件依法保留。
- 号码像电话号码但不是电话号码：避免落入电信编号监管；法务评审号码展示/拨打语义。
- 数据保留：公开码 tombstone 永久；授权码哈希 30 天；审计 7 年；遵循 GDPR/CCPA/本地法规。
- 跨境：多区域部署时，码元数据与公开资料的就近存储；授权码/密钥不跨区复制，除非合规批准。

---

## 13. 可观测性与 SLO

### 13.1 指标

| 指标 | 类型 | 用途 |
|---|---|---|
| `code_issue_total{type,result}` | counter | 发号量/成功率 |
| `code_issue_conflict_total{type}` | counter | 冲突率 |
| `code_reservation_expired_total` | counter | 预留流失 |
| `code_pool_remaining{pool}` | gauge | 号池余量 |
| `code_resolve_total{type,result}` | counter | 解析量/404 |
| `code_resolve_latency` | histogram | p50/p95/p99 |
| `code_cache_hit_ratio` | gauge | 缓存效果 |
| `credential_issue_total{purpose}` | counter | 授权码签发 |
| `credential_verify_total{result}` | counter | 校验成功率/失败 |
| `credential_lockout_total` | counter | 暴力破解 |
| `code_revoke_total{reason}` | counter | 吊销 |
| `outbox_lag_seconds` | gauge | 事件延迟 |
| `reconciliation_diff_total` | gauge | 数据漂移 |

### 13.2 日志与追踪

- 结构化 JSON；字段：`ts`、`level`、`service`、`request_id`、`trace_id`、`actor_id`（内部）、`action`、`code_type`、`result`、`duration_ms`。
- 不记录：授权码明文、完整 token、Cookie、Authorization header、原始 IP、公开码 + 用户身份的组合（按隐私评审）。
- 追踪：Resolver → Redis → 只读副本；Registry → Postgres → outbox；Credential → Postgres/KMS；采样 1–5%，错误全采。
- 日志保留 30 天；审计 7 年；安全事件日志单独保留。

### 13.3 告警

- 号池余量 < 20% / < 10% / < 5%（分级）。
- 解析 p99 > 100ms（5 分钟）；错误率 > 1%。
- 发号冲突率 > 5%；发号失败率 > 1%。
- 授权码失败/锁定突增 3σ；同一 credential 失败 > 10。
- 吊销传播 > 5s；outbox lag > 60s；replica lag > 5s。
- 对账差异 > 0；号码 tombstone 复用尝试 > 0（严重）；其他类型复用缺少 namespace 记录（严重）。
- 管理面异常批量操作/非工作时间操作。

### 13.4 SLO

| 能力 | SLI | SLO |
|---|---|---|
| 公开解析 | 可用性 / 延迟 | 99.95% / p99 < 50ms（缓存命中），< 100ms（回源） |
| 发号/预留 | 成功率 / 延迟 | 99.9% / p99 < 200ms |
| 授权码校验 | 成功率 / 延迟 | 99.9% / p99 < 50ms |
| 吊销传播 | 公开 / 私密 | < 5s / < 1s |
| 码归属持久性 | RPO / RTO | 同区域 RPO 0（已提交事务）/ RTO 30min；跨区域 DR RPO ≤ 5min |
| 审计完整性 | 事件落库率 | 100%（outbox 最终一致） |

### 13.5 容量（起步估算）

- 1M 人、3M Agent、10M 码；解析峰值 5k QPS；发号 50/s；授权码校验 100/s。
- Postgres：8 vCPU/32GB 主 + 2 只读副本（8/32）；1TB NVMe；PgBouncer；PITR。
- Redis：3 分片 × 8GB + 副本；缓存 10M 热码约 2–4GB。
- 事件总线：Kafka 3 broker 或 NATS JetStream；7 天保留；DLQ。
- 压测基线：解析 10k QPS、发号 1k/s、授权码校验 1k/s；记录 p99、错误率、DB 连接、Redis 命中。

---

## 14. 迁移与上线

### 14.1 阶段

| 阶段 | 内容 | 退出条件 |
|---|---|---|
| P0 契约 | 码型、状态机、错误码、OpenAPI/AsyncAPI、威胁模型 | 评审通过；契约测试骨架 |
| P1 注册表 | `code` schema、发号器、自助领号、公开解析（shadow） | 影子读对比；无重复；回滚演练 |
| P2 凭据 | 授权码、绑定、吊销、步进认证 | 安全评审；重放/暴力测试通过 |
| P3 编码库 | 号池管理、黑名单、策略版本、对账、管理面 | 双人审批；对账零差异 |
| P4 边缘化 | 解析器独立部署、CDN/WAF、多副本 | 压测/SLO 达标；故障演练 |

### 14.2 回填

- 既有 `users.identifier` → 新用户码/别名；保留旧值为 `code_alias`，不破坏旧链接；不符合新语法的旧 identifier 只作别名/展示，不自动转正。
- 既有 `agentId` → 新 Agent 子码；内部 ID 不变；对外展示切换。
- 批量分配号码：按注册时间/地区分池；避免与历史保留号冲突；生成映射表。
- 回填必须幂等、可重跑、可 dry-run；差异报告；分批灰度。

### 14.3 双读/双写与切换

- 过渡期：公开解析先查新 Registry，未命中回退旧表；记录 shadow diff。
- 写：新 Registry 为主，旧表双写（或反向同步），以新表为准；旧表只读兜底。
- 切换：feature flag 按用户/流量灰度；保留旧路径至少一个发布周期。
- 回滚：关闭 flag；新表写入保留；旧表仍可服务；不回滚已发码（避免重号）。
- 迁移后：旧表保留只读 90 天；确认无流量后下线。

### 14.4 测试

- 契约测试：OpenAPI/AsyncAPI 与实现一致；错误码/分页/幂等/限流。
- 状态机属性测试：随机事件序列不产生非法状态；唯一约束不被绕过。
- 并发测试：同码抢占、预留过期 vs 确认、授权码双消费、配额超发。
- 故障注入：Redis 宕机、只读副本延迟、Postgres 主切换、outbox 阻塞、KMS 超时。
- 安全测试：枚举、暴力、重放、越权、注入、日志泄露、管理面 CSRF。
- 容量测试：解析 10k QPS、发号 1k/s、授权码 1k/s；长稳 24h。

---

## 15. 架构决策记录

| ID | 决策 | 理由 | 代价/约束 |
|---|---|---|---|
| ADR-01 | 公开寻址面与私密鉴权面硬分离（schema/role/key/API/network） | 防止公开码被当凭据；缩小泄露面 | 两套运维/监控；跨面调用需窄接口 |
| ADR-02 | v1 用模块化单体（Registry + Credential 同部署），Public Resolver 独立 | 降低分布式一致性成本；解析面独立扩展/防护 | 后续拆凭据服务需迁移 schema/网络 |
| ADR-03 | Postgres 唯一事实源；Redis 只做缓存/限流/缓冲 | 唯一约束保证不重号；Redis 不可靠 | 写路径受 DB 限制；需连接池/分区 |
| ADR-04 | IM2A 号码不因吊销/注销/释放回收；仅允许双方同意 + 双人审批的显式转移；其他公开码隔离后受限复用 | 防止冒充/误投递；地址稳定性 | 号码空间长期消耗；转移审批成本；需容量规划与隔离策略 |
| ADR-05 | 号段租约 + 本地缓冲；允许断号，不允许重号 | 高吞吐、无中心单点；崩溃安全 | 断号；需对账/告警 |
| ADR-06 | 授权码高熵 + token_id 查找 + HMAC 校验，不用 Argon2 | 高熵随机值无需慢哈希；低延迟；可做凭据级锁定 | 依赖 KMS/pepper；pepper 轮换复杂度 |
| ADR-07 | Agent 子码 owner 作用域（号码.slot），不做第二全球命名空间 | 归属清晰、解析简单、配额自然 | Agent 转移需换址/别名 |
| ADR-08 | 等级策略数据化、版本化、grandfathering；等级≠信任 | 可运营/可回滚；避免等级即特权 | 策略模拟/通知/合规成本 |
| ADR-09 | 明码默认作为可轮换公开别名（语义 B） | 稳定地址与可分享码解耦；泄露可轮换 | 多一个实体/生命周期；若产品选语义 A 可删 |
| ADR-10 | 号码顺序池 + 随机池并存；明码随机 | 兼顾可记性与抗枚举 | 两套分配逻辑；需池策略 |
| ADR-11 | 授权码 opaque，不用 JWT | 即时吊销、无 claims 泄露、短 TTL | 每次校验查库；需高可用 DB |
| ADR-12 | 公开解析最小投影，不返回内部 ID/等级/PII | 隐私与防枚举 | 客户端可能需要二次查 Profile |
| ADR-13 | 所有写操作幂等 + CAS/ETag | 移动端重试/并发安全 | 客户端需带 key；服务端存 24h |
| ADR-14 | outbox + 至少一次事件；消费者幂等 | 不丢事件；避免分布式事务 | 最终一致；需 DLQ/对账 |
| ADR-15 | 管理面双人审批 + 不可变审计 | 防内部人滥用 | 运营效率下降；需审批系统 |

---

## 16. 风险登记

| ID | 风险 | 概率 | 影响 | 缓解 | 残余 |
|---|---|---|---|---|---|
| R1 | 公开码被当凭据/授权主体（语义漂移） | 中 | 高 | 架构红线、独立 schema、契约测试、评审清单、渗透测试 | 中 |
| R2 | 顺序号码被枚举/爬取 | 高 | 中高 | 限流、随机池、批量鉴权、验证码、异常检测 | 中 |
| R3 | 回收/复用公开码导致冒充/误投递 | 中 | 高 | 号码不因吊销/注销/释放回收；显式转移审批+审计；其他码隔离期 + tombstone + 复核 | 中 |
| R4 | 发号器崩溃/租约导致重号或断号 | 低 | 中高 | 唯一索引、单行高水位、不重租、对账告警 | 低（断号） |
| R5 | Redis 缓存脏读/吊销延迟 | 中 | 中 | TTL、code version、吊销集合/epoch、事件失效、私密校验不缓存 | 低 |
| R6 | 授权码泄漏（日志/URL/埋点） | 中 | 高 | 前缀扫描、脱敏、POST-only、no-store、短 TTL、单次 | 低 |
| R7 | 并发抢占/配额超发 | 中 | 中 | 唯一约束、CAS、原子配额、并发测试 | 低 |
| R8 | 等级策略漂移/降级伤害用户 | 中 | 中 | 版本化、模拟、grandfathering、提前通知 | 中 |
| R9 | 混淆字符/仿冒码 | 中 | 中高 | 骨架、黑名单、人工复核、品牌投诉通道 | 中 |
| R10 | 多区域唯一性/双写 | 中 | 高 | 单写主、region 号池、fencing、切换演练 | 中 |
| R11 | 管理员/内部人滥用 | 低 | 高 | 双人审批、职责分离、审计、break-glass 复盘 | 中 |
| R12 | 支付与预留状态不一致 | 中 | 中高 | saga、幂等、补偿/退款、人工工单 | 中 |
| R13 | 迁移中断/回滚导致重号 | 中 | 高 | 双读/双写、别名、flag、不回滚已发码 | 中 |
| R14 | 号池耗尽 | 中 | 中高 | 容量看板、70/85/95% 告警、应急池、扩容流程 | 低 |
| R15 | 号码形似电话号码引发监管/法务 | 中 | 高 | 法务评审、避免拨打语义、明确产品定位 | 中 |
| R16 | 过早微服务化/运维复杂 | 中 | 中 | 模块化单体先行；按 QPS/合规拆分 | 低 |
| R17 | 时钟偏差导致 TTL/过期异常 | 低 | 中 | DB now()、NTP、偏差告警 | 低 |
| R18 | 解析面 DDoS | 中 | 高 | CDN/WAF、限流、缓存、熔断、降级名片页 | 中 |
| R19 | 隐私/数据保留不合规 | 中 | 高 | 最小化、哈希 IP、保留策略、DPIA、法务 | 中 |
| R20 | Agent 转移导致子码归属混乱 | 低 | 中 | 别名、tombstone、转移审批、通知 | 低 |
| R21 | 幂等重放/日志/事件泄露授权码或 binding token | 中 | 高 | 敏感响应不入幂等记录；只存 ID/摘要；日志脱敏；单次消费 | 低 |

---

## 17. 待确认问题

1. **用户码与 IM2A 号码的产品语义**：用户码是否就是“可选的用户名”，号码是否必须人人都有？本文假设“号码必有、用户码可选”。
2. **明码语义**：是独立可轮换别名（本文默认），还是号码的可见渲染？若为后者，可删除 `PUBLIC_ALIAS`。
3. **Agent 归属**：Agent 是否只属于个人/组织？是否存在跨 owner 共享 Agent？这决定子码转移规则。
4. **区域/租户**：是否全球单命名空间？是否有多租户隔离要求？本文假设单区域起步、region 元数据预留。
5. **靓号/付费**：是否售卖靓号？价格、退款、转让规则？本文按“有支付 saga”设计。
6. **公开目录/搜索**：是否允许按名字搜索用户/Agent？这会显著改变隐私与枚举风险。
7. **号码监管**：号码是否会被用于拨打/短信/紧急呼叫？若是，需重新评估编号计划与合规。
8. **既有 ID 迁移**：`users.identifier`、`agentId` 的兼容窗口多久？是否允许用户保留旧 ID 作为别名？
9. **组织命名空间**：T4 组织是否拥有 `org.handle` 子命名空间？本文只给配额，未做层级域名。
10. **保留/合规**：审计 7 年、授权码哈希 30 天是否符合目标市场法规？需法务确认。

---

## 18. 验收清单（架构评审）

- [ ] 公开解析响应不含 `subject_id`/`code_id`/owner/等级/PII。
- [ ] 公开解析器 DB role 对 `credential` schema 零权限；网络策略禁止直连。
- [ ] 授权码不出现在 URL、日志、事件、缓存、APM；CI secret scanning 覆盖 `im2a_ac_`。
- [ ] 公开码不能作为 Bearer token 通过任何鉴权中间件；JWT `sub` 只用内部 ID。
- [ ] 同码并发抢占只有 1 个成功；失败方 409 且无脏写。
- [ ] 预留过期与确认并发只有 1 个成功；支付成功不会拿到已过期码。
- [ ] 授权码双消费只有 1 个成功；失败统一 `CREDENTIAL_INVALID`。
- [ ] 号码不因吊销/注销/释放重新分配给另一 subject；显式转移必须双方同意 + 双人审批 + 审计；用户码/子码/明码复用必须有 `code_namespace`/隔离期/审计。
- [ ] 吊销 5 秒内公开解析生效、1 秒内私密校验生效。
- [ ] 号池余量/断号/冲突/对账差异有指标与告警。
- [ ] 管理破坏性操作 100% 双人审批 + 审计。
- [ ] 降级不删除既有码；grandfathering 生效；用户通知。
- [ ] 迁移可 dry-run、可重跑、可回滚；旧 ID 有别名。
- [ ] 压测达到解析 10k QPS、发号 1k/s、授权码 1k/s 的目标（或明确豁免）。
- [ ] 故障演练：Redis 宕机、只读副本延迟、Postgres 切换、KMS 超时、outbox 阻塞。
- [ ] 法务/隐私评审通过（号码监管、保留期、跨境）。

---

## 附录 A：公开投影字段与内部字段对照

| 公开字段 | 来源 | 备注 |
|---|---|---|
| `code` | `code.display_value` | 规范展示 |
| `code_type` | `code.code_type` | 四类公开码 |
| `display_name` | Profile | 公开资料 |
| `avatar_url` | Profile/CDN | 签名/短链 |
| `kind` | `code_subject.subject_type` | HUMAN/AGENT/ORG |
| `verified` | Profile | 认证徽章，非等级 |
| `status` | `code.status` | 公开面仅 ACTIVE；SUSPENDED/REVOKED 统一 404 |
| `accepting_messages` | 关系策略 | OPEN/REVIEW/CLOSED，粗粒度 |
| `agent` | Agent Registry | 仅 Agent 类型 |
| `profile_url` | 配置 | 公开名片 |
| `moved_to` | `code_alias` | 宽限期 |
| `updated_at` | `code.updated_at` | 粗粒度 |
| `cache_ttl` | Resolver | 客户端缓存建议 |

**以下内部字段不出现在公开解析/公开名片：** `subject_id`、`owner_subject_id`、`tier_at_issue`、`grandfathered`、`quota`、`trust_level`、`secret_hash`、`payment_ref`、`revoke_reason`、`ip_hash`。`code_id`/`credential_id` 只在所有者自助/管理面按权限返回；`token_id` 只作为令牌的一部分出现。

## 附录 B：事件清单

| 事件 | 触发 | 消费者 |
|---|---|---|
| `code.issued` | 号码/码记录创建 | 审计、配额、通知 |
| `code.reserved` | 预留创建 | 通知、风控 |
| `code.activated` | 确认/领号 | Profile、搜索、通知 |
| `code.suspended` | 挂失/风控 | 解析失效、通知 |
| `code.resumed` | 恢复 | 解析恢复、通知 |
| `code.revoked` | 吊销 | 解析失效、审计、法务 |
| `code.retired` | 注销/退休 | 解析 404、归档 |
| `code.released` | 子码/明码释放 | 号池、通知 |
| `code.alias.created` | 改名/转移 | 解析别名 |
| `code.transferred` | 转移完成 | Profile、审计、通知 |
| `credential.issued` | 授权码签发 | 审计、风控 |
| `credential.used` | 成功消费 | 审计、绑定 |
| `credential.revoked` | 吊销 | 审计、下游失效 |
| `pool.depleted` | 号池阈值 | 运维告警 |
| `reconciliation.diff` | 对账差异 | 运维/管理员 |

事件 payload 不含授权码明文、不含 PII；消费者按 `event_id` 幂等；失败重试 + DLQ。

---

*本文为架构评审稿，不含实现源码。所有额度、TTL、阈值均为建议默认值，最终以产品/法务/容量评审和 `policy_tier_version` 配置为准。*
