# IM2A ident-code 可靠性与数据安全复核

- 状态：Review（DBRE 复核稿，待架构/产品评审）
- 日期：2026-10-30
- 复核对象：`packages/ident-code`（内存 registry）、`packages/protocol/schemas/ident-code.schema.json`、`docs/design/IM2A-CODE-SERVICE-ARCHITECTURE.md`、`docs/design/IDENT-CODE-CONTRACT.md`、`docs/adr/0004-ident-code-format.md`
- 目标版本：v2.4.3 可落地修复项 + v2.4.4 数据库落地项
- 不在范围：完整实现代码、真实密钥/pepper 值、定价与法务结论

---

## 0. 结论

**当前 ident-code 是一个合格的编解码一致性参考实现，不是可上线的发号/凭据存储。** 14/14 测试全绿，但测试集只覆盖字符串形态与单进程内存语义；持久化、多实例、崩溃恢复、回收、对账、备份恢复 **一条都没有覆盖**。

复核发现 5 个 S0 级问题，其中 3 个是"不可重号"红线的直接击穿，1 个会把存储端的凭据校验器泄露给客户端，1 个是契约三处自相矛盾（号码 10 位 vs 12 位、授权码两种形状、号码首位规则）：

| # | S0 问题 | 实测证据 |
|---|---|---|
| S0-1 | 子码序列是**进程内存**，多实例必撞号、重启必重号；复用后新 Agent 拿到**完全相同的 public_uid**，旧记录被覆盖、无 tombstone | 两实例同 user 均发 `0000000001`，`public_uid` 相同；重启后序列从 1 重来 |
| S0-2 | 子码释放/删除后，仍可签发**并可校验通过**授权码 | `issuedOk=true`、`verify.ok=true`，而公开解析已 404 |
| S0-3 | 返回给客户端的 `credentialId` **就是服务端存储的 HMAC 校验器**，且吊销以它为主键 | `credentialIdEqualsHmacOfSecret=true`，长度 43 的 base64url |
| S0-4 | 号码长度/首位规则在四份契约里不一致；10% 已发号码以 `0` 开头 | 文档 §1.1/§4.2/§4.4 写 10 位；schema/ADR/code 写 12 位；实测 50/500 首位为 0 |
| S0-5 | 无任何持久化/恢复契约，却已对外承诺 RPO 0 / RTO 30min；文档无备份、无恢复演练、无吊销重放 | registry 无 dump/load/snapshot 任何一个方法；架构文档全文仅出现一次 "PITR"（容量行） |

**结论性判断：v2.4.3 可以且只应该落地"契约纠正 + 参考实现收敛 + 测试清单"；数据库 schema、发号器、备份恢复演练必须标 v2.4.4，并在此之前把 §13.4 的 RPO/RTO 从"承诺"改成"目标（未验证）"。** 把一个没有恢复证据的能力写成 SLO，是下一次事故的入场券。

---

## 1. 复核证据

### 1.1 实测（只读探针，不修改仓库）

```text
number_leading_zero_share           50/500 = 10.0% 号码首位为 0
released_agent_still_mints_credential  released=true, issue=ok, verify=ok, resolve=null
subcode_reuse_same_public_uid        first==second（完全相同）, agents.size=1（旧行被覆盖）
credential_id_is_stored_hmac         credentialId == HMAC-SHA256(pepper, secret), 43 字符
two_live_credentials_same_public_uid auth=2, authByPublic=1（指针只留最后一个）
two_nodes_same_user_subcode_collision nodeA==nodeB=="0000000001", public_uid 相同
restart_state_loss                   number: true→false, agent: true→false, 无持久化 API
restart_subcode_sequence_reset       before==after=="0000000001"
resolve_cost_ms_at_20k_users         byNumber 0.289ms, byUserCode 0.317ms（全表扫描）
user_code_accepts_non_luhn_number    userCode 接受, 同串 validateNumber=INVALID_NUMBER
auth_ttl_bounds                      60000 接受 / 1800000 接受 / 59999 与 1800001 拒绝
```

### 1.2 现有测试覆盖了什么、没覆盖什么

`node --test packages/ident-code/test/ident-code.test.mjs` → **14/14 通过**，覆盖：长度、日历日期、Luhn 存在性、解析拒绝授权码、单次使用、过期、吊销、scope/audience 必填、公开投影不含秘密。

未覆盖（= 本次清单要补的）：多实例并发、崩溃/重启、持久化、回收与隔离期、配额、幂等、对账、备份恢复、时钟偏差、只读副本延迟、连接耗尽。

### 1.3 关键代码位置

- 子码序列：`agentSeq` 每实例一个 Map（[index.mjs:157](packages/ident-code/src/index.mjs#L157)），发号在 [index.mjs:185](packages/ident-code/src/index.mjs#L185)
- 释放只改内存标记，不吊销凭据（[index.mjs:196](packages/ident-code/src/index.mjs#L196)）
- 签发只判 `agents.has()`，不判 `active`（[index.mjs:229](packages/ident-code/src/index.mjs#L229)）
- `credentialId: digest`（[index.mjs:255](packages/ident-code/src/index.mjs#L255)），`record.credentialId ?? digest`（[index.mjs:275](packages/ident-code/src/index.mjs#L275)）说明 record 上根本没有该字段
- 单指针覆盖：`authByPublic.set(publicUid, digest)`（[index.mjs:254](packages/ident-code/src/index.mjs#L254)）
- 吊销以哈希为键（[index.mjs:279](packages/ident-code/src/index.mjs#L279)）
- 全表扫描解析（[index.mjs:170](packages/ident-code/src/index.mjs#L170)、[index.mjs:220](packages/ident-code/src/index.mjs#L220)）
- `timingSafeEqual` 导入但从未使用（[index.mjs:1](packages/ident-code/src/index.mjs#L1)）

---

## 2. 发现与修复（按严重度）

### S0-1 「不可重号」已被击穿：地址可被复用且无 tombstone

**现象**：子码不是全局分配，而是每进程 `agentSeq` 自增；`releaseAgentSubcode` 后 `issueAgentSubcode` 在隔离期满时复用同一子码，而 `public_uid = 日期 + 用户号 + 子码`，于是**新 Agent 拿到与旧 Agent 完全相同的公开地址**；`agents` Map 以 public_uid 为键，第二次 `set` 直接覆盖旧行（实测 `agents.size === 1`）。

**后果**（按严重度）：
1. 任何两个实例（滚动发布、HPA 扩容、重启）对同一 owner 都会发出 `0000000001` → 同一地址被两个 Agent 同时占用，且**没有任何约束会拒绝**。这会直接违反架构文档 §2.2/§8.2 与 §18 验收项"号码不重发、其他码复用必须有 tombstone/审计/隔离期"。
2. 旧二维码/旧名片在隔离期满后指向新 Agent —— 不是 404，是**静默改指向**，比 404 更危险。
3. 旧记录被覆盖，审计无法回答"这个地址曾经属于谁"。

**修复（v2.4.3 可做）**：
- 子码分配抽象成**注入式 allocator**；默认内存实现必须在 README 与架构文档显式标注"单实例限定，多实例会重号"。
- 默认**停止跨 Agent 复用地址**：`agent_subcode` 复归只允许**同一 agent_id**（原 Agent 复归），新 Agent 一律取新 slot。代价是每个 owner 消耗 10^10 空间，实际零成本。
- 复用路径必须落一条 tombstone 记录（`previous_agent_id`、`released_at`、`reused_at`、审批/原因），至少内存版要保留不被覆盖的 `agentsHistory`。

**修复（v2.4.4，DB）**：见 §3.2 `agent_subcode_seq` + `agent_identity`，以及 §4 不变量 I-3。

---

### S0-2 释放/删除后的 Agent 仍能签发并通过授权码校验

**现象**：`issueAuthCode` 只检查 `this.agents.has(publicUid)`（[index.mjs:229](packages/ident-code/src/index.mjs#L229)），而释放只把 `active=false`（[index.mjs:199](packages/ident-code/src/index.mjs#L199)），行仍在 Map 里。实测：释放成功、签发成功、校验成功，而公开解析已是 `null`。

**后果**：这是"公开面已 404、私密面仍放行"的经典**双平面状态分裂**。攻击者只要在子码被吊销前拿到过一枚未过期凭据，或用已释放子码请求签发，就能继续以该 Agent 身份操作，直到 TTL 到期（最长 30 分钟），而运营侧看到的是"子码已释放，风险已消除"。文档 §IDENT-CODE-CONTRACT §4 明确写"Revoking the owner revokes all child Agent credentials"——实现没有。

**修复**：
- `issueAuthCode` / `verifyAuthCode` 都必须校验目标 Agent 的 `active`/状态（且校验与消费在同一事务内）。
- `releaseAgentSubcode` 必须**同事务**吊销该 Agent 全部 `ACTIVE` 凭据，并写吊销账本 + 事件（见 §5.3）。
- 新增三条测试：释放后签发被拒、释放后旧凭据校验失败、释放后公开解析 404（三者同时成立才算修好）。

---

### S0-3 `credentialId` 就是服务端 HMAC 校验器

**现象**：签发返回 `credentialId: digest`（HMAC-SHA256 的 base64url，43 字符），而 `digest` 同时是 `auth` Map 的键与吊销接口的入参。实测 `credentialId === fingerprintSecret(secret, pepper)`。

**后果**：
1. 客户端拿到的不是标识符，而是**服务端的验证器**。任何把 `credentialId` 写进日志/工单/前端埋点/APM 标签的路径，等于把校验器外泄；它虽然不能反推 secret，但（a）让"未知 token 走 dummy HMAC"的时序防护失去意义（拿到 digest 即可直接命中），（b）把不可枚举的凭据变成可枚举的对象 ID。
2. **pepper 无法轮换**：吊销与查找都以哈希为主键，轮换 pepper 等同于全表键变更。
3. 43 字符随机串做主键/索引，索引膨胀且不可读；与文档 §5.2 `credential` 表的 `credential_id uuid PK + token_id UNIQUE` 完全不符。
4. 同一 Agent 两枚凭据时 `authByPublic` 只留最后一个指针（实测 `auth=2, authByPublic=1`），未来任何"按 publicUid 吊销"的实现都会漏。

**修复（v2.4.3，不改外层契约）**：把 93 字符 opaque 段内部拆成 **`token_id`（16 字符）+ `secret`（77 字符）**，总长仍是 128，schema 的 `pattern` 完全不用改（`[A-Za-z0-9_-]{93}` 本就把整段声明为 opaque，客户端不得解析）。于是：
- `credential_id` 变成独立的随机 ID（uuid 或 16 字符随机），与任何哈希无关；
- `token_id` 是唯一索引键，校验路径 O(1) 取行后**用 `timingSafeEqual` 比较 `secret_hash`**（这才是 `timingSafeEqual` 该出现的位置）；
- `authByPublic` 这类"单指针"结构删除，改按 `(target_public_uid, status)` 查询。

**熵**：16 字符 base64url ≈ 96 bit，77 字符 ≈ 462 bit，均远超文档要求的 80+130 bit。注意 `token_id` 必须 CSPRNG 生成、**不得由 publicUid 派生**，否则会变成"按 Agent 猜凭据槽位"。

**附带的隐私问题（需产品决策）**：授权码明文前 35 字符就是 publicUid，即**一份泄露的凭据同时泄露"这个 Agent 属于谁、账号建于哪天"**（日期前缀），这与架构文档 §2.1"授权码不是公开码的附属字段"存在张力。若产品坚持 128 = 35 明码 + 93 密文（ADR 0004 已接受），则接受该风险并用短 TTL 兜底；若要彻底解耦，需改成 128 = 16 token_id + 112 随机（无 publicUid），这属于格式变更，**必须在六端实现解析器之前定**（当前除 ident-code 自身外无任何跨端解析器，改动成本此刻最低）。

---

### S0-4 契约三处不一致，且已经污染发号数据

| 项 | 架构文档 | schema / ADR / contract / code | 后果 |
|---|---|---|---|
| 号码长度 | 10 位（§1.1、§4.2 `[1-9]`+8+Luhn、§4.4 九位 payload / 9 亿空间） | 12 位（11 payload + Luhn，`ident-code.schema.json:34`、`index.mjs:8`、ADR §1） | 号池 `range_start/range_end`、分区键、容量模型差 100 倍；展示格式 `3-3-4` 对 12 位无定义 |
| 授权码形状 | `im2a_ac_` + 16 token_id + `_` + 26 secret（§4.2） | publicUid(35) + 93（schema:56、ADR） | 决定 DB 主键与索引，两套方案不兼容 |
| 号码首位 | `[1-9]` 开头（§4.2） | 生成器 `randomInt(0, 10**11)`（[index.mjs:118](packages/ident-code/src/index.mjs#L118)），schema 允许 `[0-9]` | **实测 10% 号码以 0 开头**：打印/口述不可辨识，`bigint` 存储丢前导零，与"不是电话号码但可印刷稳定地址"的产品定位冲突 |

另外两处内部矛盾：
- `code.normalized_value` 字段说明写"永不修改"（文档 §5.2 行 270），约束段却写"释放时改为 `released:<code_id>` 墓碑值"（行 499）。改写值会毁掉 404 查询、审计回放和唯一索引的第二道防线作用。
- 同一 12 位数字在"号码"命名空间必须 Luhn 合法（`validateNumber`），在"用户码"命名空间却允许非 Luhn（`buildUserCode` 只 `assertDigits`）。实测 `IM2A_20261030123456789013` 被 `validateUserCode` 接受、被 `validateNumber` 拒绝 —— 命名空间语义分裂。

**修复（v2.4.3，零数据迁移成本）**：
1. 统一为 **12 位、首位 `[1-9]`**（保留 schema 与 ADR，改文档三处；同时把"容量 9 亿"改为 `9×10^10`，去掉"3-3-4"或改成 4-4-4）。
2. 同一条首位+Luhn 规则贯穿**四个入口 + 生成器 +（未来）DB CHECK**：`validateNumber`、`validateUserCode`、`validatePublicUid`、`buildUserCode`、`buildPublicUid`、`generateNumberCandidate`。生成器改为拒绝采样（`randomInt(1, 10**11)` 后补零不成立，需对 `[1-9]` 首位做构造而非取模，避免模偏差）。
3. 授权码形状二选一：**v2.4.3 保留 35+93 并做 S0-3 的内部拆分**；把文档 §4.2 的 `im2a_ac_` 行删除或标注"备选方案 B，未采用"。
4. 删除墓碑值改写方案，改为"值不可变 + 状态机 + tombstone 表"（见 §3.2、§4 I-2）。

---

### S0-5 没有恢复契约，却已有 RPO/RTO 承诺

**现象**：`CodeRegistry` 没有任何持久化接口（实测 `dump/snapshot/save/load/restore` 均为 `undefined`）；架构文档 §13.4 写"同区域 RPO 0 / RTO 30min；跨区域 DR RPO ≤5min"，但全文没有备份策略、没有恢复验证、没有恢复演练清单；"PITR"只在容量行出现一次（[§13.5](docs/design/IM2A-CODE-SERVICE-ARCHITECTURE.md#L1173)）；§8.3 清扫器与 §11.5 对账都不含"恢复后重放"。

**后果**（这是本复核最重要的一条）：把备份恢复当成"有 PITR 就等于能恢复"，会在真正需要的那天暴露三件事：
1. **恢复会让已吊销凭据复活**：吊销是状态更新，PITR 到吊销前 → 被吊销 token 重新有效。
2. **恢复会让号池水位倒退 → 重号**：`code_pool.next_value` 回退后继续发号，会发出"在丢失窗口里已经发出去"的号码，**同一号码两个 owner**——这是不可逆的数据损坏，且违反本系统第一红线。
3. **从没测过 RTO**：`RTO 30min` 是估算，不是测量值。

**修复**：
- v2.4.3 立刻把 §13.4 改为"目标值（尚无恢复演练证据）"，并在 P1 阶段出口条件加"恢复演练通过 + RTO 实测"。
- v2.4.4 落地 §7 的备份/恢复设计与 §9 的恢复测试清单，其中"水位锚"与"吊销重放"是两条**必须**项。

---

### S1 级（影响正确性/可运维性，v2.4.3 大部分可做）

| ID | 问题 | 证据 | 修复 |
|---|---|---|---|
| S1-1 | **无幂等键**：发号重试即多占一个地址（客户端超时重试是最常见的重复来源） | `issueAgentSubcode` / `issueAuthCode` / `createUser` 均无 `idempotencyKey` 参数 | 三个写接口接受幂等键；同键返回原结果（凭据只返回元数据，不重放明文，与 §7.5 一致） |
| S1-2 | **无配额**：任意 subject 可无限发子码与凭据 | 类里没有任何 limit 检查 | 注入 `policy` 钩子（默认不限），DB 版用 `subject_quota` 原子扣减 |
| S1-3 | **无审计/无状态机**：`releaseAgentSubcode` 只 push 到内存数组，可配置 `agentQuarantineMs: 0` 直接跳过隔离期（测试就在这么用） | [index.mjs:180](packages/ident-code/src/index.mjs#L180)、[index.mjs:200](packages/ident-code/src/index.mjs#L200) | 释放必须带 reason/actor，写 tombstone + 事件；隔离期来自 policy 版本，不由调用方传入 |
| S1-4 | **解析 O(n) 扫描**：`findUserByNumber` 遍历全部用户，userCode 分支同样 | 实测 20k 用户 0.29ms/次；线性外推 1M ≈ 14.5ms、10M ≈ 145ms，且阻塞事件循环 | 建 `byNumber`/`byUserCode` 索引 Map（内存版）+ DB 唯一索引；文档 §5.3 的索引必须落到 schema |
| S1-5 | **凭据无失败计数/锁定**（文档 §9.4/§12.2 要求 `attempt_count`/`locked_until`/按 credential+IP 计数） | record 无这些字段 | 加字段 + 内存计数；DB 版见 §3.3 |
| S1-6 | **无 `max_uses`/`purpose`/`target`/`pepper_version`**：`verify` 直接把 `used=true`，与文档"通常 1 次但可配置"和 pepper 轮换能力不符 | record 字段清单 [index.mjs:241](packages/ident-code/src/index.mjs#L241) | 补齐字段；`verify` 改 `used_count+1 < max_uses` 判定 |
| S1-7 | **时钟源是应用时钟**（文档 §6.7 要求 DB `now()`；测试用注入时钟是好事，但生产必须 SQL 判定） | `this.now()` 贯穿过期判定 | 内存版保留注入；DB 版所有 TTL 判定用 `expires_at > now()`，应用时钟只用于日志 |
| S1-8 | TTL 上限 30min 与文档 §10.2（T4 60min）不一致 | `ttlMs > 30*60*1000` 抛 `INVALID_TTL`（实测 1800000 接受、1800001 拒绝） | 二选一：改代码或改文档；建议 TTL 上限来自 policy 而非硬编码 |
| S1-9 | `scope` 重复项、`audience` 长度未校验（schema 有 `uniqueItems`/`maxLength`，代码没有） | [index.mjs:230](packages/ident-code/src/index.mjs#L230) | 与 schema 对齐，避免"schema 通过、运行时不通过"的双标准 |
| S1-10 | `numbers` 与 `users` 两份同构 Map 双写 | [index.mjs:165](packages/ident-code/src/index.mjs#L165) | 合并为单一存储 + 索引 |

### S2 级（记录在案，可不改）

- secret 实际熵 93×6 = 558 bit（`randomBytes(70)` 的 base64url 94 字符被切到 93，丢最后 2 bit），远超需求，无需改。
- 授权码明文含 publicUid 带来的归属泄露（见 S0-3 附带项）。
- `agents`/`releasedAgents` 无界增长：单实例参考实现可接受，DB 版必须分页/归档。

---

## 3. 目标数据模型（Postgres，v2.4.4）

### 3.1 Schema 与角色（在文档 §5.1 基础上补三点）

| Schema | 角色 | 复核对文档的补充 |
|---|---|---|
| `code` | `code_rw` / `code_ro` | 增加 `code_sequence`（号码占用）与 `code_ledger`（发号账本）；Resolver 只读视图 `code_public_view` |
| `credential` | `cred_rw` / `cred_ro` | 增加 `credential_revocation`（append-only 吊销账本）；Resolver 角色零权限 |
| `ops` | `ops_rw` | 新增：`restore_drill`、`reconciliation_run`、`watermark_anchor`（水位锚） |

合规要求高时 `credential` 独立集群；即使同集群也必须独立 database/schema/role/KMS key（沿用文档结论）。

### 3.2 code 平面

**`code_sequence`（号码永久占用表，insert-only）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `number` | `char(12)` PK | 号码本身即是占用证明；**永不删除、永不更新** |
| `code_id` | uuid | 指向 `code` 行 |
| `assigned_at` | timestamptz | |

> 这是"不可重号"的**最强约束**：无需依赖状态机自觉，任何重复发号都会主键冲突。文档原方案把墓碑写进 `code.normalized_value`，一旦改写成功，唯一索引就不再能拦住重发——这是必须改掉的设计。

**`code`**：保留文档字段，但修正两点：
- `value text`（原 `normalized_value`）**不可变**，加 `CHECK (value = lower(value))` 或按码型正则 CHECK；释放只改状态，不改值。
- 唯一性用**部分唯一索引**：`UNIQUE (code_type, value) WHERE status IN ('RESERVED','ACTIVE','SUSPENDED','QUARANTINED')`，配合 `code_namespace` 承担永久占用。

**`code_namespace`**：`namespace_key text PK`、`status`（`LIVE`/`RELEASED`/`PERMANENT`）、`quarantine_until`、`version bigint`。
**复用获取必须是条件更新**（这是防双 LIVE 的关键）：

```sql
-- 影响 0 行 = 已被别人抢到/未到隔离期，调用方返回冲突而不是重试覆盖
UPDATE code_namespace
   SET status = 'LIVE', current_owner_kind = 'CODE', current_owner_id = $1,
       version = version + 1, updated_at = now()
 WHERE namespace_key = $2 AND status = 'RELEASED'
   AND quarantine_until <= now() AND version = $3
RETURNING version;
```

**`agent_subcode_seq`（替代内存 `agentSeq`）**：`owner_subject_id uuid PK`、`next_slot bigint`、`version bigint`。
单语句原子取号：

```sql
UPDATE agent_subcode_seq SET next_slot = next_slot + 1, version = version + 1
 WHERE owner_subject_id = $1 RETURNING next_slot;
```

**`agent_identity`（子码绑定，含 tombstone 语义）**：`public_uid text PK`、`agent_id uuid UNIQUE`、`owner_subject_id uuid`、`subcode char(10)`、`status`、`quarantine_until`、`released_at`、`previous_agent_id uuid`。
约束：`UNIQUE (owner_subject_id, subcode)`；`agent_id` 唯一保证一个 Agent 只占一个地址；复归只允许 `previous_agent_id = agent_id`（同 Agent 复归），跨 Agent 复用由应用层拒绝 + 对账兜底。

**`code_pool` / `code_lease`**：

```sql
-- 租约区间绝不重叠（btree_gist）
ALTER TABLE code_lease ADD CONSTRAINT lease_no_overlap
  EXCLUDE USING gist (pool_id WITH =, int8range(range_start, range_end, '[]') WITH &&);
CHECK (range_start <= range_end);
UNIQUE (pool_id, range_start);
-- 水位只能前进
CHECK (next_value >= 1);
```

**`code_ledger`（发号不可变账本）**：`number char(12) PK`、`code_id`、`lease_id`、`node_id`、`issued_at`。
用途：崩溃/恢复后重算水位、证明"这个号码确实发出过"、对账的独立事实源。

### 3.3 credential 平面

**`credential`**（独立 schema）：

| 字段 | 类型 | 约束/说明 |
|---|---|---|
| `credential_id` | uuid PK | 对外标识，与任何哈希无关 |
| `token_id` | `char(16)` UNIQUE | 随机 96 bit，非秘密，仅用于 O(1) 定位 |
| `secret_hash` | `bytea` | HMAC-SHA256(pepper_v, secret)，常量时间比较 |
| `pepper_version` | int | 轮换用；短 TTL 使其可自然淘汰 |
| `public_uid` / `subject_id` / `agent_id` | text/uuid | 绑定目标；`public_uid` 只在此 schema 内出现 |
| `purpose` / `scopes jsonb` / `audience` | | 与文档 §5.2 对齐 |
| `max_uses int` / `used_count int` | | `CHECK (used_count <= max_uses)`，`CHECK (max_uses >= 1)` |
| `status` | enum | `ACTIVE/USED/EXPIRED/REVOKED/LOCKED` |
| `expires_at` / `revoked_at` / `created_at` | timestamptz | `CHECK (expires_at > created_at)` |
| `attempt_count int` / `locked_until` | | 防暴力；按 credential+ip_hash 计数，不做全局锁死 |

索引：`UNIQUE(token_id)`；`(expires_at) WHERE status='ACTIVE'`（清扫）；`(public_uid, status)`（按 Agent 批量吊销）；`fillfactor=85`（状态更新频繁，减少膨胀）。

**`credential_revocation`（append-only 吊销账本）**：`credential_id PK`、`public_uid`、`revoked_at`、`reason`、`actor`、`approval_id`。
**这张表的写入必须同步提交（synchronous commit），并且单独复制到跨区对象存储。** 原因见 §7.2：PITR 会让吊销丢失，而"丢失吊销"等于"已撤销的凭据重新可用"。

**`credential_use`**：`use_id`、`credential_id`、`result`、`ip_hash`（日盐）、`created_at`；按月分区、保留 90 天。

### 3.4 ops 与审计

`idempotency_record(route, key) PK, request_hash, status, response_ref, expires_at` —— **只存 ID/摘要，绝不存授权码明文**；
`restore_drill(drill_id, started_at, finished_at, measured_rto_seconds, backup_lsn, checks_passed, diff_count)`；
`watermark_anchor(captured_at, max_number, source)`；
`outbox`、`audit`（哈希链，WORM 归档）沿用文档。

### 3.5 容量与索引（复核补充）

- `code_sequence` 与 `code` 同为 100M 级；`code` 按 `code_type` 分区阈值 50M（文档 §5.6 已给），`code_sequence` 只增不改，按号码区间分区以避免单表 vacuum 压力。
- `credential` 建议**按 `created_at` 月分区**：短 TTL 使旧分区几乎全为终态，可整分区归档/删除，避免大表 DELETE 与膨胀。
- **不要**在 `code` 上做 `ORDER BY updated_at` 深分页；一律 cursor 分页（文档 §7.1 已要求）。
- 连接层（文档只有一句"PgBouncer transaction mode"）：PgBouncer transaction 模式 + 每服务独立池；Resolver 池 ≤ 副本 `max_connections` 60%；告警线 70%/85%，硬限留 2× 峰值余量；`statement_timeout` 5s、`lock_timeout` 2s、`idle_in_transaction_session_timeout` 10s；健康检查不得占用业务池。

---

## 4. 约束与不变量（不变量 → 强制手段 → 违反时行为）

| ID | 不变量 | 强制手段（按强度排序） | 违反时行为 |
|---|---|---|---|
| I-1 | 号码一旦发出永不重发 | `code_sequence.number` PK + insert-only；`code_ledger` PK；`code_namespace.status='PERMANENT'` 且禁止 `→RELEASED`（触发器） | 主键冲突 = 发号失败（**宁可不发号，也不重号**）；对账差异升级 S0 |
| I-2 | 公开码值不可变 | `CHECK`/触发器禁 `UPDATE OF value`；释放改状态；改名走 `code_alias` | 写失败，返回 500 并告警 |
| I-3 | 子码在 owner 内唯一、一个 Agent 一个地址 | `UNIQUE(owner_subject_id, subcode)`、`UNIQUE(agent_id)`、`agent_identity.public_uid` PK | 409 + 重新取号（不得复用同一 slot） |
| I-4 | 号段租约不重叠 | `EXCLUDE USING gist`；`UNIQUE(pool_id, range_start)` | 租约申请失败 → 停止该池发号（不做部分成功） |
| I-5 | 号池水位单调 | 单语句 `UPDATE ... RETURNING`；`CHECK(next_value>=1)`；恢复时用 `code_ledger` 校准 | 水位回退视为 P0 事件，暂停发号 |
| I-6 | 一主体一个 ACTIVE 号码 | 部分唯一索引 `UNIQUE(subject_id) WHERE code_type='IM2A_NUMBER' AND status IN ('RESERVED','ACTIVE','SUSPENDED')` | 409 QUOTA_EXCEEDED（文档 §7.3 语义） |
| I-7 | 凭据单次/限量使用 | `CHECK(used_count<=max_uses)` + 原子 UPDATE 条件 | 拒绝，统一 `CREDENTIAL_INVALID` |
| I-8 | 吊销不可复活 | `credential_revocation` append-only + 恢复后重放 | 恢复流程未重放 → 禁止开写 |
| I-9 | 配额不超发 | 原子 `UPDATE ... WHERE used < limit`；夜间 COUNT 校正（只报告） | 409，整个事务回滚 |
| I-10 | 幂等键唯一且同键同体 | `idempotency_record` PK + `request_hash` 比对 | 同键异体 → 409 IDEMPOTENCY_CONFLICT |

---

## 5. 事务边界（每个写操作：事务内容 / 隔离级别 / 失败语义）

所有写操作：`READ COMMITTED` 起步（单行/唯一约束已足够串行化），只有跨行不变量（配额 + 码 + 计数）才需要 `REPEATABLE READ` 或显式行锁。原则是**用约束串行化，而不是用长事务串行化**。

| 操作 | 事务内容（原子） | 失败语义 / 可重试性 |
|---|---|---|
| **发号（顺序池）** | ① `SELECT ... FOR UPDATE` 取 block（或单语句 UPDATE RETURNING）② INSERT `code_lease` ③ INSERT `code` ④ INSERT `code_ledger` ⑤ UPDATE `subject_quota` ⑥ INSERT `outbox` ⑦ COMMIT | 任一步失败全部回滚；**已从 buffer 取出的值视为烧毁**（断号），不回池 |
| **发号（随机池）** | INSERT `code_sequence` + `code` + ledger + quota + outbox 一个事务；唯一冲突 → 重试新候选（≤5 次，指数退避） | 冲突是正常路径；连续冲突 → 换池并告警 |
| **发子码** | ① `UPDATE agent_subcode_seq ... RETURNING` ② INSERT `agent_identity` ③ quota ④ outbox ⑤ COMMIT | 唯一冲突 → 409 并**重新取号**，禁止复用刚取的 slot |
| **释放子码** | ① UPDATE `agent_identity`（→QUARANTINED，设 `quarantine_until`）② **同事务** `UPDATE credential SET status='REVOKED' WHERE public_uid=$1 AND status='ACTIVE'` ③ INSERT `credential_revocation` ④ INSERT `outbox` + audit ⑤ COMMIT | 缺 ②③ 就复现 S0-2；不允许"先释放后异步吊销" |
| **预留** | INSERT `code`(RESERVED) + `code_reservation` + `code_namespace` 占用 + quota.reserved+1 + outbox | 冲突 → 409 CODE_TAKEN（不暴露占用者） |
| **确认（CAS）** | 单语句 `UPDATE code SET status='ACTIVE', activated_at=now(), version=version+1 WHERE code_id=$1 AND status='RESERVED' AND expires_at>now() AND version=$2`；同事务 quota（reserved−1/used+1）+ outbox | 影响 0 行 → 409 RESERVATION_EXPIRED / 412 |
| **签发票据** | ① 幂等检查 ② 步进认证/限流（§7.9：5/h、20/天）③ INSERT `credential`（token_id 唯一）④ outbox（**不含明文**）⑤ COMMIT | 同幂等键重试 → 409 CREDENTIAL_ALREADY_ISSUED（不重放明文） |
| **校验票据** | ① 单语句原子消费（见下）② 同事务 INSERT `credential_use(OK)` + 可选 `binding` ③ COMMIT | 影响 0 行 → 拒绝；**失败审计必须独立事务**（否则 rollback 会吞掉暴力破解证据） |
| **吊销票据** | 单语句 UPDATE status + INSERT `credential_revocation` + audit + outbox | 幂等（已吊销返回成功但不再记账本） |
| **清扫器** | `SELECT ... ORDER BY expires_at LIMIT n FOR UPDATE SKIP LOCKED` → 逐条 CAS → COMMIT；用 DB `now()` | 可重复执行；多实例安全；超时/失败不阻塞其他批 |

原子消费（单语句即可串行化，不需要 `SELECT FOR UPDATE`）：

```sql
UPDATE credential
   SET used_count = used_count + 1,
       status = CASE WHEN used_count + 1 >= max_uses THEN 'USED' ELSE 'ACTIVE' END,
       last_used_at = now()
 WHERE credential_id = (SELECT credential_id FROM credential
                         WHERE token_id = $1 AND status = 'ACTIVE'
                           AND used_count < max_uses AND expires_at > now())
RETURNING credential_id, subject_id, scopes, audience, public_uid, expires_at;
```

> 关键：`secret_hash` 的比较在应用层用 `timingSafeEqual` 完成（取行后），**不要**用 `WHERE secret_hash = $1` 做查找键（不可常量时间、不可轮换 pepper、破坏索引选择性）。

**迁移/DDL 边界**：所有 DDL 在独立 role 上执行并设 `lock_timeout=2s`；加列先 nullable（`ADD COLUMN ... DEFAULT` 在新版本 PG 是元数据操作，但仍需确认目标版本行为）；大表索引一律 `CREATE INDEX CONCURRENTLY`（失败会留 invalid 索引，需先 `DROP INDEX CONCURRENTLY` 再重试）；`NOT NULL` 用 `CHECK ... NOT VALID` + `VALIDATE CONSTRAINT`；禁止 `ALTER TYPE`/`SET NOT NULL` 直接打在 10M 级热表上。

---

## 6. 崩溃恢复、断号与"不可重号"

### 6.1 单节点崩溃（允许断号，不允许重号）

1. 本地 buffer 未发出的值 → **永久空洞**，不回收（文档 §6.2 已定，但对账要能解释空洞，见 §8）。
2. `code_lease` 由清扫器置 `ABANDONED`，**不重新租给其他节点**。
3. 节点重启：清空本地 buffer + 申请新 `lease_id`；**旧 `lease_id` 的写入必须被拒绝**（fencing）。实现方式：写入 `code_ledger.lease_id` 并在应用层比对当前 lease；或让 `code_lease` 行带 `status`，发号事务里 `JOIN code_lease WHERE lease_id=$1 AND status='ACTIVE'`。
4. 分区恢复后的旧主/旧节点：必须确认 lease 未被回收才能继续写，否则双发。

### 6.2 PITR 后的水位修复（文档完全缺失，必须补）

`code_pool.next_value` 是**唯一不能靠 PITR 恢复的状态**：恢复到 T-10min 会让水位倒退，继续发号就会重发已发出的号码。

```text
watermark = max( restored_pool.next_value,
                 SELECT max(number) FROM code_ledger,
                 max_number in watermark_anchor   -- 外置锚，见下 )
```

`watermark_anchor`：每分钟把 `max(number)`（及 `code_ledger` 最大 `issued_at`）追加写入对象存储（版本化 + 对象锁）。**原因**：如果 ledger 本身也被 PITR 回退，数据库内就再也找不到"曾经发到哪"，只能靠库外锚点。这是本设计里唯一需要外部依赖的可靠性机制，也是防止"恢复即重号"的最后一道闸。

恢复流程强制顺序：**只读挂载 → 校准水位 → 对账 → 重放吊销 → 抽样验证 → 才允许开写**。

### 6.3 重启后的凭据

短 TTL（≤30min）让"恢复后凭据状态不确定"这件事变小：建议**默认策略 = 恢复后把 `expires_at > now()` 的 ACTIVE 凭据全部置 REVOKED**，让用户重新签发。代价可接受（重新走一次绑定），收益是彻底消除"吊销丢失导致凭据复活"。精确方案（重放 `credential_revocation`）作为 P2 优化。

---

## 7. 备份 / PITR / 回滚

### 7.1 分层备份与分级 RPO/RTO

| 平面 | 数据 | RPO | RTO | 依据 |
|---|---|---|---|---|
| code（号码/子码/别名归属） | 不可逆身份事实 | **0（已提交事务）**，跨区 ≤ 1min | ≤ 30min | 重号/冒充不可逆，必须同步副本 + 连续 WAL |
| credential（凭据） | 可重新签发 | ≤ 5min | ≤ 30min | 丢失=让用户重签；**但必须防"吊销复活"** |
| audit | 合规 | 0 | 不适用（只读） | WORM + 跨区 |

备份栈：每日物理 base backup + 连续 WAL 归档（PITR 到秒）+ 跨区对象存储（版本化 + 对象锁/WORM）+ 每月逻辑备份（单表/跨版本恢复用）。
复制拓扑要守住 PG 的粒度事实：**`synchronous_standby_names` 是整个集群级参数，没有"按表同步"**。因此：
- 若 code 与 credential 同集群（文档 §3.2 的 v1 形态）→ 全库同步提交（代价是每次写等一次副本 ACK，发号 QPS 会受副本 RTT 约束，需实测）；
- 若按合规要求分集群 → code 集群同步、credential 集群可异步，但 `credential_revocation` 必须同步提交并**跨区双写**（这是唯一允许"绕开异步复制"的账本，因为它承担"吊销不被恢复抹掉"的责任）。

### 7.2 恢复点的"损失语义"（决定能不能开写）

| 恢复点回退 | code 平面后果 | credential 平面后果 | 必做补偿 |
|---|---|---|---|
| 1 分钟 | 水位回退 → **可能重号**；新增地址 404 | 吊销丢失 → 凭据复活 | 校准水位；重放吊销/30min 全吊销 |
| 1 小时 | 同上 + 大量新地址 404 | 同上（且已自然过期者无影响） | 同上 + 通知受影响用户 |
| 到基础备份 | 大量地址丢失（不可接受） | 凭据全丢（可重发） | 视为灾难事件，双人审批 |

### 7.3 自动化恢复验证（没有这条，前面全是纸面）

```text
每 6 小时（与发号/凭据写入隔离的独立实例）：
  1. 取最近 base backup + WAL，恢复到 now()-5min 的一次性实例
  2. 校验：pg_checksums / amcheck（索引）、行数基线、关键表 checksum
  3. 跑全套对账 SQL（§8），差异必须为 0
  4. 校准水位（§6.2），跑 100 次发号 → 与 ledger 交集必须为 0
  5. 重放吊销账本 → 抽样 100 个已吊销 token，校验必须全部失败
  6. 抽样解析 1000 个号码 → 命中率 100%
  7. 记录 measured_rto 到 ops.restore_drill
告警：恢复失败、任一校验不过、measured_rto > 目标、最近一次成功 drill > 7 天
```

### 7.4 回滚

- **发布回滚 ≠ 数据回滚**。迁移按 expand-contract，每一步独立可回退；已发号码、已发凭据**永不回滚**（文档 §14.3 已定，需进 checklist）。
- 格式/契约变更走新协议版本 + 双读窗口（旧解析器继续解析旧码），禁止原地改语义。
- 数据错误只允许"向前修复"（compensating 写入 + 审计）；PITR 只用于灾难恢复，且必须执行 §6.2/§6.3。
- 降级策略：主库不可用 → **停止发号（fail closed）**，公开解析走只读副本 + 缓存，凭据校验**不缓存、失败即拒**。宁可发不出号，也不能重号。

---

## 8. 对账（把文档 §11.5 的 10 条变成可执行）

| # | 检查 | 期望 | 阈值/处置 |
|---|---|---|---|
| R-01 | `code_sequence` 与 `code`(IM2A_NUMBER, live) 双向差集 | 0 | >0 → S0 值班事件，暂停该池发号 |
| R-02 | `pool.next_value - 1 >= max(code_ledger.number)` | 恒真 | 违反 = 水位倒退，立即停写 |
| R-03 | 空洞数 = `(next_value-1) - count(ledger)`，与已 ABANDONED lease 覆盖的范围吻合 | 可解释 | 不可解释的空洞 → 调查（可能是双发掩盖） |
| R-04 | `code_lease` 区间重叠 | 0（exclusion 已保证，兜底） | >0 → 立即隔离该池 |
| R-05 | 同一 `(owner, subcode)` 对应 2 个 `agent_id` | 0 | >0 → 地址抢用，通知安全 |
| R-06 | `QUARANTINED` 子码仍存在 `ACTIVE` 凭据 | 0 | **>0 = S0-2 未修**，立即批量吊销 |
| R-07 | `subject_quota.used/reserved` vs `COUNT(*)` | 差 0 | >0 只报告 + 告警，修复走审批脚本 |
| R-08 | `used_count > max_uses` | 0 | >0 → 逻辑 bug，记录并修复 |
| R-09 | `ACTIVE` 且 `expires_at < now()-1h`（清扫器失效） | 0 | >0 → 清扫器故障告警 |
| R-10 | `credential_revocation` 与 `credential.status='REVOKED'` 差集 | 0 | >0 → 吊销未落账本，禁止下次恢复 |
| R-11 | `outbox` PENDING > 5min 数量；attempts > 10 | 0 | >0 → DLQ + 告警 |
| R-12 | 别名环 / 别名指向 RETIRED（最多 1 跳） | 0 | >0 → 解析可能死循环 |
| R-13 | Redis `res:ver:{code_id}` 与 PG `version` 抽样差 | 0（允许 TTL 内滞后） | 系统偏差 → 缓存失效链路故障 |
| R-14 | 副本延迟 / WAL 归档延迟 | <5s / <60s | 超阈 → 告警；归档断流 = 恢复能力归零 |
| R-15 | 上述全套在**恢复出来的副本**上重跑 | 0 | 恢复演练的判定标准 |

处置原则：只报告 + 告警，不自动改生产；修复脚本必须 dry-run、双人审批、逐条审计；号码相关差异一律升级为值班事件。

---

## 9. 恢复测试清单（可执行）

### A. 不可重号（P0，CI 每次 + 每周演练）

| # | 步骤 | 期望证据 |
|---|---|---|
| A-1 | 2 个实例 + 共享 PG，100 并发 × 1000 次发号 | 重号 0、唯一冲突被正确重试、无 500 |
| A-2 | 提交后立刻杀客户端，用同 `Idempotency-Key` 重试 | 返回同一 `code_id`，`code_sequence` 只多 1 行 |
| A-3 | `SIGKILL` 发号进程后重启 | 本地 buffer 作废、旧 lease 置 ABANDONED、不从旧 buffer 继续 |
| A-4 | **PITR 到 10 分钟前 → 校准水位 → 发 1000 个号** | 与 `code_ledger` 交集 = 0（文档完全没有这一条） |
| A-5 | 两实例同时重启后各发 10 个子码 | 子码无重复、`public_uid` 无重复（当前实现必然重复） |
| A-6 | 释放子码 → 立即重新分配 | 新 Agent **不得**拿到旧 `public_uid`；同 Agent 复归允许且留 tombstone |

### B. 凭据（P0）

| # | 步骤 | 期望证据 |
|---|---|---|
| B-1 | 两个并发 verify 同一 token | 恰好 1 个成功，另一个 `CREDENTIAL_INVALID` |
| B-2 | verify 成功后下游抛错 | 明确选择：不回滚（已烧毁）或回滚；两种都要有测试锁定 |
| B-3 | 释放/吊销子码后 verify 旧 token | 全部 `CREDENTIAL_INVALID`（**当前失败**） |
| B-4 | 释放子码后请求签发 | 拒绝（**当前成功，必须修**） |
| B-5 | 签名返回体中 `credentialId` | ≠ 任何服务端哈希；日志/事件中不含明文与哈希 |
| B-6 | 未知 token_id / 错误 secret / 过期 / 已用 / 已吊销 | 对外统一 `CREDENTIAL_INVALID`，内部审计区分 |
| B-7 | 同一 credential 连续失败 10 次 | 锁定生效，且**不影响**其他来源（按 credential+ip 计数） |
| B-8 | pepper 轮换 v1→v2 | 旧 token 仍可校验，v1 在最大 TTL 后停用 |
| B-9 | PITR 到吊销前 + 重放账本（或 30min 全吊销） | 被吊销 token 仍 401 |

### C. 备份/恢复/HA（P1，需 DB）

| # | 步骤 | 期望证据 |
|---|---|---|
| C-1 | 每日自动 restore + 全套对账 + 抽样解析 + RTO 记录 | 失败即发布阻塞级告警 |
| C-2 | 季度跨区恢复演练 | 与 `watermark_anchor` 比对 RPO ≤1min；RTO 实测 |
| C-3 | 主库 failover（sync replica 提升） | 已提交事务 0 丢失；旧主 fencing 后不能写 |
| C-4 | 只读副本延迟注入 30s | 读己之写策略生效（回主库或等待），解析不回脏数据 |
| C-5 | Redis 全丢 | 解析回源正常、凭据校验正常、发号正常 |
| C-6 | WAL 归档中断 10min | 告警触发（恢复能力受损必须可见） |

### D. 变更安全（P1）

| # | 步骤 | 期望证据 |
|---|---|---|
| D-1 | 在 10M 行 `code` 上做加列/加索引/加约束 | 业务锁等待 <1s，无阻塞告警；迁移可重入 |
| D-2 | 迁移中途 kill | 无半成品（invalid 索引被发现并清理），可重跑 |
| D-3 | 关闭 feature flag 回滚 | 旧路径可服务，已发号码/凭据不回滚 |

---

## 10. v2.4.3 可落地修复项（严格限定：不引入 DB 服务）

### R-0（发布前必须，改文档 + schema 描述 + 参考实现小改）

| ID | 修复 | 验收 |
|---|---|---|
| R-0-1 | 号码统一 12 位、首位 `[1-9]`、Luhn；生成器拒绝采样；同一规则贯穿 4 个校验入口 + 2 个构造函数；文档三处改正、容量改 `9×10^10`、删/改 `3-3-4` | 生成 10000 次 0 个首位 0；`validateNumber/buildUserCode/validatePublicUid` 对首位 0 全部拒绝 |
| R-0-2 | 93 字符 opaque 内部拆 `token_id(16)+secret(77)`，总长 128 不变，schema `pattern` 不变；`credentialId` 改为独立随机 ID；`timingSafeEqual` 用在哈希比较 | `credentialId` ≠ HMAC；签发返回体与日志无哈希；token 只按 `token_id` 定位 |
| R-0-3 | 释放/删除联动吊销；签发与校验都校验 Agent 仍 active | 三条新测试（B-3/B-4 与公开解析 404 同时成立） |
| R-0-4 | 子码分配抽象为可注入 allocator；默认实现**禁止跨 agent_id 复用地址**；README/架构文档标注"单实例限定" | 两实例模拟断言"不得产生相同 `public_uid`"通过 |
| R-0-5 | 三个写接口加 `idempotencyKey`；同键重放返回原结果，凭据不重放明文 | 同键重试 → 同一 subcode/credentialId，行数不变 |
| R-0-6 | 架构文档 §13.4 改为"目标值（未验证）"，P1 阶段出口条件加"恢复演练通过 + RTO 实测" | 文档 diff |
| R-0-7 | CHANGELOG/README 明确"ident-code 是参考实现，无持久化、无生产发号"，避免下游把 14/14 当生产就绪 | 文档 diff |

### R-1（v2.4.3 可做，工作量小）

配额钩子（默认不限，但接口存在）· 失败计数/锁定字段 · `purpose`/`max_uses`/`pepper_version`/`revoked_reason` · O(1) 索引 Map（byNumber/byUserCode）· `numbers` 双 Map 合并 · 移除双写 · resolve/parse 共用归一化 · `scope` 去重与 `audience` 长度对齐 schema · TTL 上限改由 policy 提供（与 §10.2 对齐）· 每条发现转成一条回归测试（14 → ≥25）。

### R-2（v2.4.4，需 DB）

§3 schema + §4 约束 + §5 事务边界 + §6 水位/吊销重放 + §7 备份与自动恢复验证 + §8 对账 + §9 A/B/C/D 清单。

---

## 11. 优先级（按"不修就会造成不可逆数据损坏"排序）

| ID | 项 | 严重度 | 不可逆性 | 目标版本 | 成本 |
|---|---|---|---|---|---|
| S0-1 / R-0-4 | 子码分配无全局序列、地址复用无 tombstone | S0 | 高（冒充+审计不可回放） | v2.4.3（接口/默认行为）+ v2.4.4（DB） | 低/中 |
| S0-5 / R-0-6/7 / §6.2 / §7 | 恢复契约缺失、水位回退重号、吊销复活 | S0 | 高（重号不可逆） | v2.4.3（文档收敛）+ v2.4.4（锚点/重放/演练） | 中 |
| S0-2 / R-0-3 | 释放后仍可签发/校验 | S0 | 中高（越权窗口） | v2.4.3 | 低 |
| S0-3 / R-0-2 | credentialId = 存储哈希；无 token 索引 | S0 | 中（凭证泄露面） | v2.4.3 | 低 |
| S0-4 / R-0-1 | 号码长度/首位契约矛盾（10% 号码首位 0） | S0 | 高（改格式需六端返工） | v2.4.3 | 低 |
| S1-1 / R-0-5 | 无幂等键 → 重试多占地址 | S1 | 中 | v2.4.3 | 低 |
| S1-3 / §4 I-2 | 墓碑值改写 vs 值不可变 | S1 | 中（审计/404 不可靠） | v2.4.4 | 低 |
| S1-1..S1-10 | 配额/审计/扫描/锁定/时钟/字段缺项 | S1 | 低 | v2.4.3（部分）/v2.4.4 | 低 |
| §8 | 对账 15 项（当前 0 项自动化） | S1 | 中（漂移不可见） | v2.4.4 | 中 |
| §5 / §6.3 | 事务边界与恢复后凭据策略 | S1 | 中高 | v2.4.4 | 中 |

**v2.4.3 的定义必须写清**：本版只交付"契约与参考实现的正确性"，不交付"可恢复的生产发号服务"。否则发布材料与实际能力之间的差距，就是下一次事故的剧本。

---

## 12. 待评审决策（阻塞项）

| # | 决策 | 选项 | 建议 |
|---|---|---|---|
| D-1 | 号码 12 位 vs 10 位 | 改文档保 12 位 / 改代码回 10 位 | **保 12 位 + 首位非 0**（改文档成本最低，容量大 100 倍） |
| D-2 | 授权码是否必须 128 字符 | 是（35+93，接受归属泄露）/ 否（`im2a_ac_` 短格式，隐私更好） | 是 + 内部 token_id 拆分；把 §4.2 的短格式标为未采用 |
| D-3 | 子码跨 Agent 复用 | 永不复用 / 隔离期满可跨 Agent 复用 | **永不复用**（成本几乎为零，收益是地址永不改指向） |
| D-4 | 恢复后凭据处理 | 30min 窗口全吊销（简单）/ 重放吊销账本（精确） | 先全吊销，账本重放作为 P2 |
| D-5 | 是否接受外置水位锚（对象存储） | 接受 / 不接受（则 RTO>30min 与重号风险二选一） | **接受**（这是唯一能防"恢复即重号"的机制） |
| D-6 | RPO/RTO 承诺是否先降级为"目标值" | 是 / 否 | 是（无演练证据不应写进 SLO） |

---

## 附录 A：建议新增的可靠性指标与守卫（文档 §13.1 缺）

| 指标 | 采集 | 守卫/告警 |
|---|---|---|
| `code_duplicate_attempt_total` | 发号主键冲突计数 | >0 且持续 → S0 事件 |
| `pool_watermark_lag` | `next_value - 1 - max(ledger.number)` | <0 → 立即停写 |
| `lease_conflict_total` | 租约/排他冲突 | >0 → 隔离池 |
| `pg_connections_used_ratio` | 每池/每服务 | 70%/85% 告警，95% 熔断新连接 |
| `wal_archive_lag_seconds` | 归档断流 | >60s 告警（恢复能力归零） |
| `replication_lag_seconds` | 副本 | >5s 告警；读己之写超阈回主库 |
| `backup_age_seconds` | 最近成功备份 | >26h 告警 |
| `restore_drill_last_success` | `ops.restore_drill` | >7 天未成功 → 阻塞发布 |
| `credential_verify_fail_total{reason}` | 校验失败分类 | 突增 3σ → 攻击/故障 |
| `reconciliation_diff_total{check}` | §8 各项 | >0 → 按严重度分派 |

## 附录 B：本次复核未覆盖（需后续专项）

- 公开解析面的缓存一致性/吊销传播实测（无 Resolver 实现）
- KMS/pepper 实际托管形态（当前为字符串入参）
- 支付 saga 与预留的一致性（无实现）
- 跨区域唯一性与号池 region 隔离（仅设计）
- 隐私保留期（授权码哈希 30 天、审计 7 年）的自动化实现

---

*本稿不含任何真实密钥、pepper 或凭证；示例值均为占位符。所有实测数据来自对 `packages/ident-code` 的只读探针与现有测试，未修改仓库源码。*
