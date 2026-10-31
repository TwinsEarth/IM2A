# IM2A v2.5.4 → v3.0.3 · 支撑 Agent 社会与自进化的实时协作底座

| 项 | 值 |
|---|---|
| 设计对象 | IM2A 实时协作底座，版本区间 **v2.5.4 – v3.0.3** |
| 代码基线 | 平台 `E:\GPT\im2a-v2.3.45`（平台 **2.5.3**，dev 已部署、**非生产**，HEAD `3c4c3fb`）· Go `E:\GPT\im2a-work\im2agent`（MCP `0.3.0`，13 tools） |
| 灵魂三要素 | **自主目标** · **经验资产归属** · **可验证信用**（本文只负责让这三件事在实时层上"可判型、可归属、可对账"） |
| 路线（本版重排） | 2.5.4–2.5.13 预研 · 2.6 自主目标 · 2.7 技能归属 · 2.8 信用 · 2.9 技能节点与经济 · 3.0 宪法治理（**50 能力版 + 4 收口版**，§9.1 给算术） |
| 起点能力 | per-session capability + SSE 事件过滤（v2.5.3 安全补丁）· D/P/C 三通道契约（ADR 0006，Accepted/运行时未就绪）· trajectory-record 1.0（草案）· 13 个 MCP 工具 |
| 硬约束 | 每 10 版过一次九门禁 · 安全边界只进不退 · 每版只有一个增量 · 不新增 MCP 方法/A2A 扩展 |
| 不变协议基线 | A2A `1.0`(`v1.0.1`) · MCP `2026-07-28` · Agent Plugins `1.0` · `dsh-session-ref 1.0` · `ident-code 1.0` |
| 状态 | **DESIGN-READY / RUNTIME-NOT-READY**：语义可冻结；前 10 版必须先补齐"可重放 + 可见性按主体 + 幂等 + 背压"，否则 presence、目标、归属、信用全部建在错误的定序基座上 |
| 凭证 | 本文不含任何凭证、密钥、令牌值；只引用环境变量名与文件位置。**本文不含实现代码**，协议一律用字段表、判定规则与状态机表达 |

---

## 0. 结论摘要

### 0.1 事实（本次逐条取证，可信）

1. **安全边界是"会话级"，不是"主体级"。** 事件过滤与 capability 已生效（`internal/harness/bus.go:303-321`；`internal/api/harness.go:24-39,279-281`），但 `Session` 结构只有 `ID/Harness/Agent/Display/ConnectedAt/tokenHash`（`bus.go:37-44`），**没有 principal**。与 `docs/security/SSE-SESSION-ISOLATION.md:132-140` 自述的残余风险一致。
2. **主体其实拿得到，只是被丢掉了（本次新发现 N0）。** `/v1/harness/` 挂在统一 `wrap` 下（`internal/kernel/routes/routes_harness.go:8`），而 `wrap = auth.Authenticate(...)`（`internal/kernel/routes/routes.go:59-67`）；该中间件在 **JWT 路径**上会把 `sub` 注入 context（`internal/auth/identity.go:74-79`），只有静态 `GATEWAY_TOKEN` 路径不注入身份（`:70-73`）。而 `sessionCapability`（`api/harness.go:24-39`）**从不调用** `auth.UserIDFromContext`。→ "主体绑定"不是等上游网关改造，而是**接线 + 为静态令牌路径定义 `unbound` 语义**；但"一个共享 MCP 桥进程如何代表多个终端用户"仍是真问题（§3.1 ID-4、待决 D-2）。
3. **"可重放"不成立，四个独立缺陷叠加。** ①消息 ID 是总线全局自增（`bus.go:246-247`）；②SSE 只写 `data:`、不写 `id:`（`api/harness.go:247`），`Last-Event-ID` 无从生效；③快照与订阅**非原子**——历史重放在 `:259-265`，`Subscribe()` 在 `:267`，两次调用之间发布的消息永久丢失且客户端无法自愈；④历史是内存环形 500 条（`bus.go:22,248-251`），重启清零。
4. **可见性被绑在临时标识上（G11）。** `History`/`EventVisibleTo` 都走 `messageMatches`（`bus.go:284-298`）：`to` 等于我的会话 id，或 `harness:`/`agent:` 命中我的 harness/agent 名。会话 id 是临时的（takeover、token 轮换、401 后重注册都会换 `hs_*`），换 id 之后**发给旧 id 的 D 消息对新会话永久不可见**；MCP 桥正是这样重注册的（`cmd/im2a-mcp/main.go:238-242` 清空 `b.sess` → `ensureSession` 无 token 新建，`:168-194`）。
5. **`to = agent:<name>` 是未经授权的寻址键（本次新发现 N1）。** `agent:` 按**名字**匹配（`bus.go:294-296`）、`harness:` 按**类型**匹配（`:291-293`），而默认 agent 名是字面量 `"agent"`（`bus.go:108-110`）；任何持网关令牌者可为任意 harness 注册任意 agent 名（`bus.go:95-138` 只查 harness 是否在注册表、agent 非空且 ≤128 字节）。→ 两个不同主体的默认会话**天然互相可见**；这是与 G11 完全不同类的越权面，且不能靠"改可见性键"顺带修掉。
6. **presence 的空转代价已经付了。** `Unregister` 向全局 fanout 一条 presence 快照（`bus.go:175`），而 `EventVisibleTo` 对 presence 恒返回 false（`:316-320`）——**每个订阅者都收到、每个订阅者都丢弃**。量级是 `O(订阅者 × 会话数 × 单条快照)`：200 会话 × 200 订阅者 × ~200B ≈ 8 MB/次注销，且 `Presence()` 每次调用都复制并排序（`:181-198`）。
7. **要建的地基有一半已在别的包里，缺的是接线。** `internal/msgseq` 已实现按分片的单调序号 + 启动播种 + 进程单例（`msgseq.go:59-69,123-130,156-165,200-227`）；`store.PGMessage.SaveMessage` 已是 `ON CONFLICT (message_id) DO NOTHING`（`store/message_pg.go:34-39`）；`audit` 已有 append-only `Entry/Action/Sink`（`audit/log.go:33-46`，**无哈希链**）；`metrics` 已有 counter/duration + `/metrics`（`metrics.go:22,38,87`）；`stream` 已有带 ping/pong、按 topic 分发、可挂 mirror 的 WS Hub（`ws.go:67-127`）。**唯一不能直接复用为日志的是 `eventbus`**：它的 PG 写是"起 goroutine best-effort 落审计表、失败只打日志"（`eventbus.go:86-97`），无顺序、无重试、无游标 —— 见 N7。
8. **公开声明必须先有测量。** 真实上限是 `MaxSessions=200`（**全局硬编码**）、`MaxHistory=500`、每订阅者缓冲 128（`bus.go:18-27`）。ADR 0006 §6 / 规则 B1 明确：没有 `outboundQueueBytes`、presence 合并频率与实测 p95，**不写并发数字**。
9. **契约与实现、契约与契约之间已经错位三处。** ①`realtime-session-profile.schema.json:64-115` 的 `resumeHeader/retentionSeqs` 与实现不符（实现无 `id:`、无保留窗口）；②`trajectory-record 1.0` **没有** `conversationRef/seqRange/soulTriad/goalId/assetRefs` 字段，且 `additionalProperties:false`（`docs/contracts/trajectory-record.schema.json:7,8-23`），而轨迹文档把边界定义在正文里（`IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md:93`）→ **"轨迹切分"不可校验**；③该契约的角色枚举只有 `system/human/gpt/tool/gate/approval`（`:157`），**没有"另一个 Agent"角色** → 多 Agent 协作轨迹会被投影成 `human` 或 `gpt`，归因从结构上错误（N8）。这三处直接决定 2.6–2.9 的目标/归属/信用是否可算。

### 0.2 判断

- **v3.0.3 的协作对象仍是"日志 + 不可变工件"，不是共享文档。** 继续不引入 CRDT/OT（ADR 0006 D2/D14 不变）。任务板、目标进度、成员在线都是 **D 日志的只读投影（O 面）**，不是可并发改写的共享对象。引入 CRDT 的闸门写在 §4.7（三条条件之一成立才需新 ADR），不是"以后再说"。
- **轨迹不是第四条通道。** 它是 D 日志的只读投影，写进独立证据平面；P/C/O 永不进轨迹 —— 否则等于给"训练集"开一条绕过同意与出口护栏的旁路。
- **治理干预必须双写：D 留永久记录，C 只做投递加速。** 丢光 P 与 C，干预的事实、理由、依据规则仍在 D 日志里可重建（EV-2/R1 不被破坏），"干预不可审计"这个最危险的口子从结构上被堵。
- **自动化的方向只能收敛风险，不能扩张权限。** 自动降级/熔断/限流/回滚/隔离允许；自动晋级/扩权/开新端点一律禁止（§6.6 治理公理）。
- **三要素对底座的要求是三条可判定的断言，不是三句愿景**：目标必须是**有主、有界、可中止**的对象（否则"自主"等于"失控"）；归属必须**进 D 事件与轨迹的 provenance**（否则资产无法追责）；信用必须**只由被签名的结果派生、只从 D 日志重算**（否则账本记错人比没有账本更危险）。presence 一律不得承载这三者的事实（§3.9）。

---

## 0.5 评审结论：对 Part 1 的核对、修正与新发现

> 本节是"评审"部分。Part 1（原稿 §0–§3）的事实基础基本成立，但有 6 处引用/表述需修正（M1–M6）、4 处跨文档冲突需 owner 裁定（X1–X4）、10 项新发现需落规则（N0–N9），其余缺口在 Part 2 补齐（§4–§10）。**未修正的项已直接落到正文相应章节**，避免"评审意见与正文两张皮"。

### 0.5.1 核实通过（C1–C8）

| # | 原判断 | 复核证据 | 结论 |
|---|---|---|---|
| C1 | 事件过滤 + capability 已生效 | `bus.go:303-321`、`api/harness.go:24-39,279-281` | 成立 |
| C2 | capability 不含 principal | `bus.go:37-44` 的 `Session` 字段集 | 成立 |
| C3 | 消息 ID 全局自增 | `bus.go:246-247` | 成立 |
| C4 | SSE 无 `id:` 行 | `api/harness.go:242-252` 只 `fmt.Fprintf(w, "data: %s\n\n")` | 成立 |
| C5 | 快照与订阅非原子 | `api/harness.go:259-265` 与 `:267` 之间无栅栏 | 成立（且客户端无 `id:` 无法自愈） |
| C6 | 历史为内存环形 500 | `bus.go:22,248-251` | 成立（引用行号见 M1） |
| C7 | presence 空转扇出 | `bus.go:175` + `:316-320` | 成立（量级见 M4） |
| C8 | 原语齐备未接线 | `msgseq`/`store`/`audit`/`metrics`/`stream` | 成立（`eventbus` 例外，见 N7） |

### 0.5.2 需修正（M1–M6，已在正文改正）

| # | 原表述 | 问题 | 修正 |
|---|---|---|---|
| M1 | "内存环形 500 条（`bus.go:20,248-251`）" | `bus.go:20` 是 `MaxMessageBytes=8000`；`MaxHistory=500` 在 `:22` | 正文一律写 `bus.go:18-27` 常量块 + 具体行 |
| M2 | 引用 `harness.go:24-39` 与 `harness.go:242-252` | 两个同名文件（`internal/harness/harness.go` 与 `internal/api/harness.go`），易误读 | 正文统一写 `api/harness.go:…` / `harness/bus.go:…` |
| M3 | "消息 ID 全局自增（`bus.go:49,246-247`）" | `:49` 是 `Text` 字段，`ID` 在 `:48` | 改为 `bus.go:236-247` |
| M4 | "presence 白付扇出成本" | 结论对，但没给量级 | 补 `O(订阅者×会话数×快照)` 与 8MB/次的估算（§0.1-6、§8.1） |
| M5 | SL 演进列的版本号（2.6.5 / 2.6.0 / 2.7.8 / 2.9.4) | 与本次路线主题（2.7=技能归属、2.8=信用）不对应，且同一能力出现三个不同版本号（X2） | 全部按 §9 重编号（SL1→2.5.6、SL2→2.5.8、SL3→2.5.8、SL5→3.0.0…） |
| M6 | P0-4 类表述"主体不可鉴权，须等 2.9.4" | 事实上 JWT 路径已能产出 `user_id`（N0），只是没接线 | 改为"接线 + 定义 unbound"，提前到 W0（2.5.6） |

### 0.5.3 需裁定的冲突（X1–X4，阻塞 §9）

| # | 冲突 | 各方主张 | 本文处置 |
|---|---|---|---|
| **X1** | 版本算术不自洽 | ①"50 小版本"；②区间 2.5.4–3.0.3 实际含 **54** 个号；③灵魂总架构 W1 写"2.5.4–2.6.3"，但表里只列 2.5.4–2.5.9 + 2.6.0–2.6.3（**静默跳过 2.5.10–2.5.13**）；④本次路线要求"2.5.4–2.5.13 预研" | 按 §9.1 显式算术：**50 能力版（W0–W4 各 10）+ 4 收口版（3.0.0–3.0.3）**，5 个能力门禁 + 1 个终认证。若 owner 坚持"50 含 3.0.x"，则 2.5.10–2.5.13 必须公告为"内部里程碑、不打 tag"。**这是待决 D-1，不裁定就不发 2.5.4。** |
| **X2** | 主体绑定的落地版本三处不一致 | 本文 Part 1 SL1=2.6.5、SL2=2.6.0；灵魂总架构 P0-4=2.9.4 | 提前到 **2.5.6**（W0）。理由见 N0：能力已在，晚做一天，W1–W4 的账本就多一天记在 `unbound` 上 |
| **X3** | 灵魂总架构 W1 的内容（soul.json/triad/漂移）在新路线里没有窗口 | 新路线 W1=自主目标、W5=宪法治理 | 拆分安置：`soulTriad` 只作**字段落点**进 W0（2.5.12，为归因服务）；漂移检测与漂移矩阵进 **W5 3.0.0**（它是宪制工具，不是目标工具） |
| **X4** | EVO-5 自进化闭环（OBSERVE→…→MONITOR、shadow/canary/promote）在新路线里没有窗口 | 灵魂总架构 W4=自进化闭环 | 拆到 W3（eval runner、用例地板、evaluation identity 冻结）与 W4（节点上线即灰度：shadow/canary/monitor 挂在节点发布流程上）；冻结/回滚演练进 W5。**待决 D-3。** |

### 0.5.4 本次新发现（N0–N9，均已在正文落规则）

| # | 发现 | 证据 | 影响 | 落点 |
|---|---|---|---|---|
| **N0** | 主体在 JWT 路径可得但被丢弃 | `kernel/routes/routes.go:59-67`、`auth/identity.go:70-79`、`api/harness.go:24-39` | 主体绑定成本被高估；`unbound` 语义必须先定义 | §3.1 ID-1/ID-4、2.5.6 |
| **N1** | `to=agent:<name>` 按名字寻址且名字可自封 | `bus.go:108-110,291-296,95-138` | 同名跨主体互见；寻址≠授权被写进了代码 | P0-R3、2.5.8 |
| **N2** | 每个事件每条连接都要取一次总线全局锁 | `bus.go:307`（`EventVisibleTo` → `Session`） | presence 10Hz×成员数时是全局锁热点，容量模型必须计入 | §8.1 |
| **N3** | 慢消费者断开与正常断开不可区分 | `api/harness.go:247-251` 写失败即 return，bus 无"因慢而断"标记 | `outbound_drop` 与 `session_offline` 无法解释，告警会误判 | §7.2、2.5.10 |
| **N4** | 历史接口静默截断，无 `hasMore` | `api/harness.go:206-215` 只回 `messages/count`，`limit` 硬上限 200（`bus.go:262-264`） | 客户端无法判断"是否还要拉"，是"静默截断"类缺陷（违反 SI-4 精神） | P0-R5、2.5.9 |
| **N5** | 401 只清会话不清能力令牌 | `main.go:238-242` 清 `b.sess`，`b.sessToken` 仍保留 | 重注册窗口仍发旧能力头 → 401 风暴期的无效请求；`since=lastID` 对新会话必然查空（G11 具体表现） | 2.5.9 |
| **N6** | 三处"积压"容量语义互不一致 | `main.go:39`(`maxInbox=200` 截断)、`bus.go:22`(`MaxHistory=500` 丢弃)、`INTERFACES.md:914`(`capacityPerAgent=1000` 拒绝) | 同一个用户会看到三种溢出行为，且只有一种有错误码 | §8.1、2.5.10 |
| **N7** | `eventbus` 不能当 D 日志 | `eventbus.go:78-104`：内存数组 + 异步 goroutine 写 PG，失败只 `log.Printf` | 拿它当 backplane 会丢事件且不可重放；只能喂审计/投影 | §3.8、§8.4 F6 |
| **N8** | 轨迹契约没有"另一个 Agent"角色 | `trajectory-record.schema.json:157`（6 值枚举） | 多 Agent 轨迹归因错误 → 信用与归属同时失效 | §5.4、P0-R9 |
| **N9** | 轨迹契约缺边界字段，边界只存在于散文 | `trajectory-record.schema.json:7,8-23` vs `TRAJECTORY-RL-BOUNDARY.md:93` | `seqRange` 不可校验 → 轨迹切分无法测试 | §5.2、2.7.3 |

---

## 1. 证据基线（现状取证）

| # | 判断 | 证据 | 能证明 | 不能证明 |
|---|---|---|---|---|
| E1 | 会话是进程内、全局 200、无 TTL | `bus.go:24,66-79,129-132` | 无心跳、无 sweep、DELETE 前永不离线 | 生产实际并发 |
| E2 | 消息 ID 全局自增 | `bus.go:236-247` | `since=` 只能当全局水位 | 跨会话可比性 |
| E3 | 无 `clientMsgId`、无去重 | `bus.go:216-255` | 重试＝重复入队 | 网关侧是否有补偿 |
| E4 | SSE 无 `id:` 行 | `api/harness.go:242-252` | `Last-Event-ID` 无法生效 | 各客户端是否支持 |
| E5 | 快照与订阅非原子 | `api/harness.go:256-267` | 重放窗口存在丢消息间隙 | 间隙实际命中率 |
| E6 | 可见性按会话 id/名字判定 | `bus.go:284-321` | 轮换/接管后旧消息不可见（G11）；同名互见（N1） | 实际发生频次 |
| E7 | presence 全量广播后被全员丢弃 | `bus.go:175,316-320` | 白付扇出成本 + 一条时序侧信道 | 具体带宽占比 |
| E8 | presence 快照自行裁剪为"只有我" | `api/harness.go:254-258` | 无跨会话枚举 | 多 Agent 场景是否够用 |
| E9 | MCP 桥重连固定 3s、无抖动 | `main.go:208,230,243,250` | 部署即同频重连 | 生产客户端退避 |
| E10 | 桥的收件箱与游标在进程内 | `main.go:100-110,278-287` | 进程退出即丢 in-flight（符合 profile `resumeScope:durable-only`） | 各 Harness 进程寿命 |
| E11 | 序号原语已具备但未接线 | `msgseq.go:59-69,200-227` | 只缺"把 harness 的 id 换成会话 seq" | 迁移期双写成本 |
| E12 | 幂等写已具备 | `store/message_pg.go:34-39` | 去重可用现有唯一键 | "返回首次结果"需读回 |
| E13 | 审计只有追加，没有链 | `audit/log.go:33-46` | 无法证明"未被改写" | 生产 WORM 落地 |
| E14 | profile 常量与实现不符 | `realtime-session-profile.schema.json:64-115` vs `api/harness.go:233,242-252` | 契约先行、实现滞后 | 何时对齐 |
| E15 | 安全边界为会话级 | `SSE-SESSION-ISOLATION.md:132-140` | 多租户/多 Agent 不可声明 | 生产网关是否另有主体层 |
| **E16** | **主体在 JWT 路径可注入** | `auth/identity.go:59-82`、`kernel/routes/routes.go:59-67` | 绑定只需接线 | 共享桥如何代表多用户（D-2） |
| **E17** | **agent 名可自封且用于寻址** | `bus.go:95-138,291-296` | 同名互见；寻址即授权 | 是否已被生产客户端利用 |
| **E18** | **每个事件两条连接内各取全局锁** | `bus.go:303-321,345-361` | 锁竞争随"连接×事件率"线性增长 | 实测 p95（未压测） |
| **E19** | **历史无 `hasMore`、`limit`≤200** | `api/harness.go:206-215`、`bus.go:262-264` | 客户端不能判定截断 | 实际少读比例 |
| **E20** | **`eventbus` 是 best-effort 异步写** | `eventbus.go:78-104` | 不能作为有序日志/backplane | 何时改成同步写 |
| **E21** | **轨迹契约缺边界字段与 peer-agent 角色** | `trajectory-record.schema.json:7,8-23,157` | 轨迹切分与多 Agent 归因不可校验 | 迁移成本 |
| **E22** | **审计动作枚举无治理类动作** | `audit/log.go:18-30` | 干预/目标/资产/信用事件无处落 | 扩展是否破坏现有消费方 |

> 取证方法：读 `internal/harness/*.go`、`internal/api/harness.go`、`cmd/im2a-mcp/main.go`、`internal/{auth,msgseq,store,audit,metrics,eventbus,stream}`、`internal/kernel/routes/*.go`，对照 `docs/adr/0006`、`docs/security/SSE-SESSION-ISOLATION.md`、`docs/design/TENCENT-V2.5.3-DEPLOYMENT.md`、`docs/contracts/trajectory-record.schema.json`、`agent-im/contracts/INTERFACES.md`。**未运行任何负载测试**，§8 的天花板全部是预算上限而非实测值。

---

## 2. 安全底线台账（SL1–SL16，任何版本不得回退）

这是整个区间的**脊梁**：每个版本的验收第一项都是"SL 全绿"。任一版把任一项改红即视为安全回退，**版本作废、回滚，不允许"下个版本补"**。

| # | 底线 | 当前实现位置 | 回归检查（每版必跑） | 演进落点 |
|---|---|---|---|---|
| SL1 | 流式端点必须携带有效会话能力，缺失→401 | `api/harness.go:229-232` | 无 token/错 token/过期 token 三例均 401 | **2.5.6 收紧为主体绑定** |
| SL2 | 每条事件写出前必须过可见性判定，禁止全局广播 | `api/harness.go:279-281` | 双会话交叉：A 收不到 B→C | 2.5.8 判定键改 `(universe, principal, agent, conversation)` |
| SL3 | presence 快照只含调用者可见成员 | `api/harness.go:254-258` | A 快照不含 B 任何字段 | 2.5.8 扩为"同团队成员 + 目标参与者"的最小字段集，**不得**回到全量枚举 |
| SL4 | 会话能力只以 SHA-256 存储，永不出现在 presence/whoami/日志 | `bus.go:42,140-154` | 全响应体 + 日志扫描 0 命中 | 不变 |
| SL5 | 查询参数 `session_token` 回退**只允许存量** | `api/harness.go:29-32` | 新端点 grep 0 命中 | 2.9.x 起新端点仅头部；3.0.0 起回退改为默认关的开关 |
| SL6 | P/C 通道结构上不可承载正文 | `realtime-message.schema.json` `$defs.presence/control` | 负例（presence 带 text / control 带 string arg）必须被拒 | C 枚举扩展须同时提交 SL6 复审（§6.3） |
| SL7 | 不新增 MCP 方法、不新增 A2A 扩展 | `realtime-session-profile.schema.json`（`noNewMcpMethods`/`noNewA2aExtensions`） | profile 校验 + `tools/list` 差异审查 | 新能力只能落在 IM2A 自有工具与现有 HTTP 语义 |
| SL8 | 任何新增字段若能承载自由文本，即视为安全变更，需专项复审 | `agent-gateway.mjs:361-387`（护栏只看 `payload.content`） | 字段清单 diff + 复审记录 | 复审通过才可加；默认加**枚举/数值/引用**字段 |
| SL9 | 重注册轮换 token；无能力的注册必须新建会话 | `bus.go:95-138` | 无 token 重注册得到新 `hs_` | 不变（但 2.5.8 要求"新会话继承主体可见性"） |
| SL10 | 撤销即断连；离线期间的撤销不得因"上次连着"而复活 | 待建（`forceOffline` + 恢复重验） | 撤销后 5s 内 `REVOKED`；离线撤销后 resume 被拒 | 2.5.9 |
| SL11 | 会话枚举端点只返回调用者自身 | `api/harness.go:111-123` | 双会话互查失败 | 2.5.8 扩为"同团队成员清单"时须单独复审 |
| SL12 | deny-list（鉴权码/令牌/手机/邮箱/身份证/wxid/设备/IP/UA/原始 session-ref）不进日志、证据包、轨迹 | `TRAJECTORY-RL-BOUNDARY.md:241-255` | 哨兵串 + 正则扫描 0 命中 | 不变 |
| **SL13** | **寻址键不得作为授权键**：`to=agent:<name>` / `harness:<id>` / `broadcast` 一律只是路由提示，可见性由成员表裁决 | 待建（现为 `bus.go:284-298`，**违反**） | 同名两个会话互发、互看均为 0 命中 | **2.5.8** |
| **SL14** | **`unbound` 会话三条禁止**：不进团队、不被委托、不进轨迹/信用 | 待建（`auth/identity.go:70-73` 静态令牌路径） | unbound 会话加入团队/被委托/产生轨迹事件均被拒 | **2.5.6** |
| **SL15** | **治理动作必须双写 D**：任何干预/目标/资产/信用事件在 D 日志有永久记录，C 只做加速 | 待建 | 丢弃全部 C 后，干预事实与依据仍可从 D 重建（100%） | 2.6.x 起 |
| **SL16** | **容量声明必须先测量**：任何并发/人数/延迟数字必须附 `outboundQueueBytes`、presence 合并频率、p95 出处 | ADR 0006 §6 / 规则 B1 | 文档扫描：无出处的数字 → 发布阻断 | 全区间 |

**SL 的检查机制（不是口号）**：`SL-REGRESSION` 测试族（§9.8）在 CI 必跑；每个版本 PR 必须贴该族输出；九门禁时 SL 族作为 `Compat` 门子项与独立证据项 `security-floor.json` 一起归档。**SL 红灯 = 版本不可发布**，与性能、功能无关。

---

## 3. 事件 / 身份 / 会话模型

### 3.1 六层身份 + 一个定序域（禁止互用）

> 计数口径：**标识主体/权限的六层** = 宇宙、团队、Agent、Principal、会话、委托链；**`cnv_*` 不是身份**，它是 `seq` 的定序域（下表第 7 行），单独列出是为了强调"它只能定序，不能承载身份"。

现有模型只有两层（`harness`+`agent` 路由键、`hs_` 会话），装不下"社会"与"宇宙"。补齐为六层，每层一句话说清"它能干什么、绝对不能干什么"。

| 层 | 标识 | 生成者 | 生命周期 | 用途 | **禁止用途** |
|---|---|---|---|---|---|
| 宇宙/租户 | `uni_*` | 平台 | 长期 | 配额域、策略版本域、隔离域、指标聚合域 | 禁止跨宇宙复用 `conversationId`；禁止跨宇宙委托 |
| 团队 | `team_*` | 平台 | 长期 | 成员表、任务板、编队（formation）、presence 订阅域 | 禁止当权限载体（权限在成员表，不在团队 id 上） |
| Agent（持久身份） | `agt_*` | 平台（对齐 `ident-code` 子码） | 长期 | 审计主体、委托主体、轨迹里的伪名、限流内侧键、**寻址键** | 禁止作为 bearer；禁止跨 principal 复用；禁止由客户端自封（N1） |
| Principal（人） | `prn_*`（`ident-code` L0–L4 主体） | `ident-code 1.0` / JWT `sub` | 长期 | 权限主体、限流外侧键、委托链根、同意主体 | **禁止由客户端自封**，只能由网关在鉴权后注入 |
| 会话（连接） | `hs_*` | 总线 | 连接期 / 接管期 | 传输亲和、presence 主体标识、SSE 绑定 | **禁止做恢复游标**；禁止作为持久可见性键（G11） |
| 委托链 | `del_*` | 网关 | 可撤销 | 权限衰减、级联撤销 | 禁止更换 principal；禁止跨组织签发本平台 token |
| 会话域（定序） | `cnv_*` | 总线/团队层 | 长期 | `seq` 单调域、重放单位、轨迹切分单位 | 禁止跨会话比较 `seq` |

**规则 ID-1（主体不可自封，但可得）**：`principalId` **只能**来自认证中间件注入的主体断言（`auth.UserIDFromContext`，`auth/identity.go:19-22,74-79`），**不得**出现在 `POST /v1/harness/sessions` 的客户端可控字段里被直接信任。JWT 路径已具备该断言（N0）；静态 `GATEWAY_TOKEN` 路径**不注入身份**，其会话一律 `unbound`。

**规则 ID-2（会话是临时件）**：任何持久语义（可见性、游标、审计归属、轨迹归属、信用归属）**不得**以 `hs_*` 为键。会话只回答"这条连接现在是谁"。

**规则 ID-3（迁移期 `unbound`）**：v2.5.4–2.5.7 期间旧客户端只发 `harness/agent`，服务端把 `principalId` 标为 `unbound`。`unbound` 的会话受 SL14 三条限制（不进团队、不被委托、不进轨迹/信用），**不允许**静默升级成某个 principal，也不允许"按 harness+agent 猜一个"。

**规则 ID-4（共享桥的属于谁）**：一个以静态令牌运行的 MCP 桥进程可能服务多个终端用户（腾讯 dev 主机即此形态）。该桥注册的会话默认 `unbound`；若需代表某用户，唯一合法路径是**由该用户自己的凭证换取**（JWT 透传，或经网关签发的 `act-as` 委托链），且该路径必须走 §3.7 的委托衰减（SI-2）。**禁止**用环境变量 `IM2A_AGENT_NAME` 之类声明主体（现有 `main.go:12,78-80` 正是如此）。

**规则 ID-5（agent 名只是软路由）**：`agent:<name>` 在迁移期仅作为**团队成员内**的软路由提示；落地后（2.5.8）一律解析为 `agt_*` 再判权。名字相等 ≠ 有权收（N1/SL13）。

### 3.2 状态判型：三条通道 + 一个观测面（不是第四条通道）

| 类 | 名称 | 是否定序 | 是否持久 | 是否审计 | 是否计未读 | 可载正文 | 丢失后果 |
|---|---|---|---|---|---|---|---|
| **D** | durable | 是（`cnv_* + seq`） | 是 | 是 | 是 | **唯一允许** | 事故 |
| **P** | presence | 否 | 否（TTL） | 否 | 否 | 否（结构上不可） | 无感 |
| **C** | control | 否 | 否（`stored=false`） | 否（**治理动作用 D 双写**） | 否 | 否（封闭枚举 + 数值参数） | 慢一点 |
| **O** | observation | 否 | 派生，可重建 | 摘要入审计链 | 否 | 否（只含判定与计数） | 可重算 |

**规则 EV-1（O 面不是通道）**：O 面是**服务端派生的只读投影**（评估判定、任务板视图、轨迹抽取结果、指标快照、摘要比对结果）。它不上线、不投递、不发 `seq`、不进 D 日志、不计未读。它存在的唯一理由是把"观测"与"通信"结构上分开。

**规则 EV-2（R1 不变量）**：丢弃全部 P 与 C 后，系统必须仅凭 D 日志 + 游标重放到完全一致的状态；丢弃全部 O 后，必须能由 D + 轮次生命周期 + 目标/资产/信用事件重算出同样的 O。**任何只存在于 P/C/O 里的"事实"都是设计缺陷。**

**规则 EV-3（判型先于实现）**：新增任何一种状态，先填 §3.2 的五列（定序/持久/审计/未读/正文），再决定走哪条通道。判型错误是本领域首要事故源，比选型错误更贵。

### 3.3 事件信封 E1.0 字段表（增量、向后兼容）

以 `harness.Message`（`bus.go:47-54`）为最小集，按需扩展。**原则：只加字段，不改既有语义；客户端必须忽略未知字段。**

| 字段 | 状态 | 来源/规则 | 说明 |
|---|---|---|---|
| `conversationId` | **新增（必需）** | `cnv_*` | `seq` 的唯一比较域；缺失时按 `(universe, goal)` 派生并标 `derived:true` |
| `seq` | **新增（必需）** | `msgseq`（§3.4） | 服务端分配；客户端**不得**提交；从 1 起、严格递增 |
| `messageId` | 新增（`msg_*`） | 一 `clientMsgId` ↔ 一 `messageId`（永久） | 跨节点去重键（与 `origin` 联合） |
| `clientMsgId` | **新增（客户端 UUID）** | 幂等键；键空间 `principalId:clientMsgId` | 重试必须复用同值 |
| `channelClass` | 新增 | `D`/`P`/`C` | 决定持久化、审计、背压、溢出策略 |
| `kind` | 既有语义 | `TEXT/AGENT_TURN/APPROVAL_CARD/INTENT`；`CMD` 仅 C | 扩展 `kind` 需 SL8 复审 |
| `from`/`to` | 既有 | `from` = 发送方标识；`to` = `broadcast`/`harness:`/`agent:`/会话 | **`to` 降级为路由提示**，可见性不由它决定（SL13） |
| `fromAgent`/`toAgent`（2.5.8+） | **新增** | 稳定 `agt_*` | 审计与轨迹用；会话换 id 不影响归属 |
| `principalRef` | **新增（2.5.6+）** | 主体断言派生的 `prn_*` 或 `unbound` | 限流外侧键、信用归属键 |
| `goalId`/`taskId` | **新增（2.6.0+）** | 目标/任务对象 | 轨迹切分（§5.2）与信用证据（§6.8）的锚点 |
| `turnId`/`outcome` | **新增（AGENT_TURN）** | 轮次生命周期 | 轨迹 `gate`/`approval` 事件来源 |
| `causationId` | **新增** | 指向上游 `messageId`/`interventionId` | 治理与自进化的因果链 |
| `policyVersion` | **新增** | 宇宙级策略版本 | 干预与晋升归属；缺失即无法回答"谁按哪条规则做的" |
| `soulTriad`（2.5.12+） | **新增（可选）** | `H(agentSoul ‖ harnessProfile ‖ constitutionVersion)` | 归因三要素之一；缺失的记录不得进 eval/晋升证据 |
| `assetRefs`（2.7.0+） | **新增（可选）** | `agt_*` 所有者 + `lineage` | 归属链；见 §5.4 |
| `at` | 既有 | 服务端时钟 | **仅展示**；不参与定序 |
| `payload` | 既有 | D 才允许 `content`（≤8000B） | 治理记录用**结构化子对象**，无自由文本 |
| `traceId`/`bizType`/`bizId`/`trustZone`/`agentContext` | 既有 | 不改语义 | — |
| `origin` | **新增** | 节点 id | 跨节点扇出"至多一次" |
| `meta` | 既有 | 自由对象，**不进审计** | 过渡期扩展口袋；**禁止**放正文（SL8） |

**规则 EV-4（schema 版本与扩展口袋）**：`realtime-message.schema.json` 的 D 变体是 `additionalProperties:false`，与"只加字段"的演进冲突。因此 **2.5.4 冻结规则**：新增字段一律通过 **schema 次版本（`1.0`→`1.1`）** 发布，兼容窗口内**同时接受两版**；窗口外旧版本文档拒绝。`meta` 不承担协议字段职责。

### 3.4 定序：per-conversation seq（G1 的正式修法）

**结论**：不发明新分配器，**接线 `internal/msgseq`**。

- 分配键：`msgseq` 的 `uint64 userID` ← 稳定映射表 `conversation_ord(cnv_*, uint64)`（FNV-1a 64 位 + 冲突检测；冲突换盐重算，**不得**接受碰撞）。
- 保证：同一 `conversationId` 内**从 1 起严格递增、无空洞**；跨会话完全独立。
- 重启：照搬 `SeedFromDB` 语义（`msgseq.go:200-227`）——**按会话取 `MAX(seq)` 播种**；取全局 MAX 会让所有会话一起跳到最大值，制造巨大空洞。
- 唯一约束：`(conversation_id, seq)` 唯一；重复插入必须**显式失败**（沿用 msgseq 迁移 037 的思路：把撞号从静默错序变成可见错误）。
- 分配器不是瓶颈也不是真相：`HotShardIDs`（`msgseq.go:93-105`）给出拆片信号；真相在 D 日志的唯一索引上。

**规则 SEQ-1（seq 只由权威分配）**：客户端时间戳、客户端序号、网关重试序号都不得参与定序。`at` 只做展示，时钟偏移不改变收敛结果。

**规则 SEQ-2（authority 唯一写者）**：单个 `conversationId` 在任一时刻只有一个授权写者（authority），持 `fencingEpoch`。日志写入必须校验 epoch，**过期 epoch 的写入被拒绝**——否则分区期间两个 authority 各自写自己的 seq 序列进同一会话，收敛彻底失效（§8.4 F7）。

**规则 SEQ-3（水位与保留窗口）**：`head` = 该会话最大 seq；`floor = head - retentionSeqs`（默认 10000，**需 owner 签字**，D-4）。`seq` 空洞（`seq_gap_count > 0`）**即事故**，不是告警。

### 3.5 会话状态机与接管

```text
                   注册(鉴权通过)                        接管(generation 更大)
  CONNECTING ───────────────▶ ONLINE ◀──────────────────────────┐
       │                        │                                │
       │ 鉴权失败                │ drain 信号                      │ resume(seq 合法)
       ▼                        ▼                                │
    (拒绝，不建会话)          DRAINING ──关闭──▶ OFFLINE ──────────┘
                                │                  │
                                └──── 撤销 ────────┴──▶ REVOKED（终态）
                                       心跳超时 90s ──▶ OFFLINE
```

| 状态 | 语义 | 允许的入口 | 允许的出口 |
|---|---|---|---|
| `CONNECTING` | 已收到注册请求，未完成鉴权 | 注册 | `ONLINE` / 拒绝（无会话残留） |
| `ONLINE` | 可收发 D；可收 P/C | 鉴权通过、resume | `DRAINING` / `OFFLINE` / `REVOKED` |
| `DRAINING` | 摘流中：**拒绝新 POST**（503 + `Retry-After`），在途请求允许完成 | 部署信号 | `OFFLINE` |
| `OFFLINE` | 连接已断，会话保留游标与订阅集至 TTL | 心跳超时、连接关闭 | `ONLINE`（resume）/ `REVOKED` |
| `REVOKED` | **终态**，仅撤销可达 | token 撤销 / 委托级联 / principal 撤销 | **无**（只能重新授权建新会话） |

**规则 SES-1（takeover 取代 `ALREADY_ONLINE`）**：同一 `(principal, agent)` 且凭据有效的新连接接管旧连接：旧 transport 以 `reason=superseded` 关闭；新连接继承**游标与订阅集**，**不继承**未确认出站队列（由客户端用同一 `clientMsgId` 重放）。不同 principal/agent 的冲突在鉴权阶段即失败，不返回 `ALREADY_ONLINE`。

**规则 SES-2（generation 而非时间戳）**：接管用 **agent 级单调 `generation`** 判定先后。时间戳会让"两台设备时钟差 3 秒"变成"新连接被旧连接的关闭事件误杀"。旧连接收到关闭信号时必须带自己的 generation，**generation 落后才服从**。

**规则 SES-3（心跳、超时、TTL 三数分离）**：心跳 30s（客户端）、服务端超时 90s（判 `OFFLINE`）、presence TTL 60s（判"不可见"）。**三者不得合并**：超时是连接事实，TTL 是展示事实，混用会导致"网络抖动 = 用户消失"或"幽灵在线"。注意现有 `stream.Hub` 用 60s 读超时/54s ping（`ws.go:84-87`），与本表不同 —— 迁移时**必须显式映射**，不允许两套数字并存。

### 3.6 恢复算法（唯一实现，九方共用语义）

```text
resume(principalId, agentId, conversationId, lastAppliedSeq):
  1 传输重置：新会话（正常）或 SSE 带 Last-Event-ID 重连
  2 重验身份：主体断言 + capability + 委托链 verify() + 团队成员关系
  3 取游标：以服务端 ack 位移为准；客户端上报值仅用于校验
  4 判定：
       floor ≤ lastAppliedSeq ≤ head -> REPLAY (lastAppliedSeq, min(+maxBatch, head)]
       lastAppliedSeq <  floor       -> RESYNC_REQUIRED（发快照，不发日志）
       lastAppliedSeq >  head        -> CURSOR_INVALID（拒绝静默修复）
  5 按 seq 顺序应用；按 messageId 去重（双保险）
  6 未确认上行：同 clientMsgId 重发；服务端命中去重 -> 返回首次结果
```

**规则 RES-1（三态必须可区分）**：`RESYNC_REQUIRED` 是降级路径，必须单独计数（`resync_required_rate`），**不许**伪装成一次成功重放。`CURSOR_INVALID` 是客户端状态被污染的信号，**不许**静默全量重发。

**规则 RES-2（快照与订阅的原子性，修 E5）**：正确顺序是 **先订阅、再读快照、按 `snapshotSeq` 栅栏丢弃重复**；当前实现是反的（`api/harness.go:256-267`）。读快照时返回 `snapshotSeq`，随后把订阅缓冲里 `seq ≤ snapshotSeq` 的事件丢弃。

**规则 RES-3（快照必须来自 D 日志，不是内存环形区）**：仅修原子性不够——内存历史 500 条重启清零（E1/E6），所以 `RESYNC_REQUIRED` 与冷启动的 `since=0` 都必须从**持久日志**取快照。内存环形区只能当"最近窗口缓存"，不能当真相。

**规则 RES-4（capability 轮换不得改可见性，修 G11）**：可见性判定键必须是 `(universeId, principalId, agentId, conversationId)` 与显式成员关系；`to` 只是路由提示。轮换、接管、重注册均不得改变"哪些 D 事件对我可见"。

**规则 RES-5（分页必须显式，修 N4）**：重放/历史响应必须带 `hasMore` 与 `nextSeq`；`RESYNC_REQUIRED` 必须带 `snapshotSeq`。**静默截断一律视为缺陷**——200 条上限（`bus.go:262-264`）可以保留，但必须在响应里说出来。

**规则 RES-6（401 后不得沿用旧能力，修 N5）**：客户端在收到 401 时必须同时清空**会话 id 与能力令牌**，并以同一 `(principal, agent)` 重新注册；重新注册后必须用 `conversationId + seq` 重新取游标，**不得**沿用旧会话的 `lastID`（否则新会话必然查空，见 G11）。

### 3.7 幂等：键、TTL、返回首次结果

| 操作 | 幂等键 | 重复时行为 |
|---|---|---|
| 发 D 消息 | `principalId:clientMsgId` | 命中 → **返回首次结果**（含 `messageId`/`seq`），不二次入队 |
| 上行重试 | 同上 | 同上 |
| 目标/任务状态迁移 | `taskId/goalId + expectedVersion + clientMsgId` | 版本冲突 → `TASK_VERSION_CONFLICT`（不静默覆盖） |
| 干预 | `interventionId` | 命中 → 返回首次受理结果，不重复下发 C |
| 资产提案/stage | `proposalId + baseSha` | 冲突 → `CANDIDATE_DRIFT` |
| 委托签发 | `idempotencyKey` | 返回同一 `del_*` |
| 计量（经济） | `callId` | 返回首次计量结果，不重复计费 |
| scoped grant 消耗 | `grantId + scope` | 单次消耗 |

**规则 IDM-1（TTL ≥ 最大离线窗口）**：去重 TTL 取 24h（`86400000`），覆盖"离线 1 小时 + 200 条积压"验收并留余量。

**规则 IDM-2（缓存结果，不只缓存键）**：只记键不记结果，会让重试者拿到"重复"却拿不到 `seq`，从而**无法推进游标——反而制造空洞**。这是幂等实现里最常见的错误，必须在实现评审里作为反例固定下来。

**规则 IDM-3（键空间隔离）**：`clientMsgId` 永不跨 principal 复用；`principalId:clientMsgId` 天然阻止跨用户撞键。

### 3.8 状态与既有代码的映射（迁移期不改语义的落点）

| 目标状态 | 现有落点 | 迁移方式 | 迁移期风险 |
|---|---|---|---|
| `conversationId` + `seq` | `bus.go:236-247` 的 `nextID`；`msgseq` | 双写一版（`id` 与 `seq` 并存），下版删 `id` 语义 | 旧客户端只认 `id`，需一版内提供 `id↔seq` 查询 |
| 去重 | `store.PGMessage` `ON CONFLICT(message_id)` | 先按 `clientMsgId` 建唯一键，再回填 | `messageId` 与 `clientMsgId` 映射需持久 |
| 幂等回读 | `LoadMessage`（`message_pg.go:67+`） | 冲突后按 `message_id` 读回首次结果 | 竞态下读回可能早于提交可见 |
| 审计链 | `audit.Entry` | 加 `prevHash`/`hash`/`checkpoint`，不改 `Action` 语义（须扩枚举，见 E22） | 历史条目无链，需一次性锚定 |
| 指标 | `metrics.IncCounter/ObserveDuration` | 直接复用，统一前缀 `im2a_rt_` | 现有指标无前缀规范 |
| 多实例扇出 | `stream.Hub`（ping/pong + topic + mirror）**或** `eventbus` | 用 Hub 当传输、用 D 日志当真相；**禁止**用 `eventbus` 当 backplane（N7：best-effort 异步、无顺序、失败只打日志） | 与"总线不持久化"冻结条款的边界见 §8.4 F6 |

> **迁移铁律**：每一版只迁移**一个**语义；`id` 与 `seq` 并存的那一版必须能被 v2.5.3 客户端读成"和以前一样"。**不允许**在同一版里既改 seq 又改可见性键——出问题时无法定位。

### 3.9 灵魂三要素对底座的硬需求（本区间的验收锚点）

| 要素 | 底座必须提供什么 | 由此导出的规则 | 反向禁止 |
|---|---|---|---|
| **自主目标** | 目标是有主、有界、可中止的对象；执行权用**租约**表达；进度以**里程碑 D 事件**表达 | 目标状态机 + `fencingEpoch`（§4.4）；预算/期限/轮次三重上限；到期 100% 中止并返回已得最优结果 | 禁止用 presence 表达"正在干活"之外的任何目标事实；禁止无上限的自主循环 |
| **经验资产归属** | 每条有产出的 D 事件携带 `provenance`（owner `agt_*`、`lineage`、`contentHash`）；轨迹以 `(conversationId, seqRange, goalId)` 可切分 | 归属校验器（SI-1）：写入者 ≠ owner 即拦；资产事件只走 D（2.7.0–2.7.9） | 禁止把轨迹归 Agent 所有；禁止 `to`/`presence` 承载归属字段 |
| **可验证信用** | 信用事件只由**被签名的结果**派生，只从 D 日志 + 轮次生命周期**重算**；账本可对账 | 证据签名 + 盲评 + 领域隔离 + 90/180 天衰减；`credit_ledger_mismatch = 0` 即事故 | 禁止自述证据、禁止分数进提示词、禁止用 presence 计费或计分 |

**规则 XR-1（三要素单向依赖）**：目标 → 归属 → 信用，前者不成立则后者无意义。因此版本顺序不可交换：目标对象（W1）必须先于资产事件（W2）先于信用事件（W3）。把信用提前到归属之前，等于给"无法追溯来源的产出"打分。

---

## 4. Presence 与任务协作协议

### 4.1 Presence 对象与可见域

Presence 回答**唯一**一个问题："此刻谁在，且他在做什么动作"。它**不**回答权限、已读、进度事实、信用、目标归属。

| 字段 | 1.0 已有 | 1.1 新增 | 取值约束 | 说明 |
|---|---|---|---|---|
| `conversationId` | ✔ | — | `cnv_*` | 订阅域 |
| `channelClass` | ✔ | — | `"P"` | 判型锚 |
| `memberId` | ✔ | — | 会话 id 或成员 id | 1.0 兼容字段 |
| `agentRef` | — | ✔ | `agt_*` | 稳定主体；会话换 id 不影响 |
| `displayRef` | — | ✔ | 成员表键（**不是展示名**） | UI 按成员表解析昵称；P 里没有自由文本（SL6/SL8） |
| `state` | — | ✔ | `online/idle/busy/typing/away` | 封闭枚举 |
| `cursor`/`selection`/`viewport` | ✔ | — | 数值 | 只允许数值，禁文本（`realtime-message.schema.json:95-107`） |
| `typing` | ✔ | — | 布尔 | — |
| `goalId`/`turnId` | — | ✔ | 引用 | "在哪个目标/轮次上"——自主目标的唯一 presence 表达 |
| `queueDepth` | — | ✔ | 分桶 `0/1-4/5-19/20+` | 分桶而非精确值：精确值会变成侧信道与排产暗示 |
| `leaseHeld` | — | ✔ | 布尔 | 是否持有任务租约（§4.5） |
| `ttlMs` | ✔ | — | ≤60000 | 单帧自愈上限 |
| `origin` | ✔ | — | 节点 id | 跨节点去重（`id+origin`） |
| `at` | ✔ | — | 服务端时钟 | 仅展示 |

**规则 P-1（可见域）**：presence 的可见成员集 = **团队成员 ∩ 目标参与者**；订阅时判权一次（`realtime-session-profile.schema.json:223-227`），成员被移除时**主动推一次 `revoked` 并关闭该订阅**，把窗口收敛到一次往返，而不是等 TTL 自然过期。

**规则 P-2（存在 ≠ 权限）**：收到某成员的 presence 不产生任何操作权；无 presence 也不撤销任何权限。**presence 里的字段永远不得成为授权判定的输入**。

**规则 P-3（空转禁止，修 E7）**：会话注册/注销**不得**向全局 fanout presence 快照。presence 变更只投递给"已订阅该会话可见域"的连接；无订阅者时直接丢弃（与 `INTERFACES.md:1418` 总线语义一致）。判据：`presence_fanout_bytes` 在无订阅者时为 0。

### 4.2 合并、频率与三数分离

| 参数 | 值 | 依据 | 违反后果 |
|---|---|---|---|
| 客户端心跳 | 30s | SES-3 | 与超时合并 → 抖动即掉线 |
| 服务端超时判 `OFFLINE` | 90s | SES-3 | 太短 → 移动网抖动误判；太长 → 幽灵在线 |
| presence TTL | 60s | profile `presence.ttlMs` | 同 90s 则"断线仍有残影" |
| 客户端渲染新鲜度 | 30s | 本文 | 超过即淡出，不显示"在线" |
| 合并频率上限 | 10Hz / `(conversation, member)` | profile `presence.coalesceHz` / ADR 0006 D9 | 50Hz 光标 = 白烧扇出 |
| 溢出策略 | 合并保留最后一帧（`coalesce-last`） | profile `backpressure.overflow.presence` | 丢最旧会让光标"跳回" |

**规则 P-4（字段组级 LWW）**：合并单位是字段组（`cursor`/`selection`/`viewport`/`typing`/`state`），不是整帧；同一字段组按 `(at, origin)` 决胜，**不跨字段组比较**。理由：光标与"正在输入"是不同事件源，整帧合并会让光标吞掉 typing。

**规则 P-5（有界）**：每连接 presence 待发队列受 `outboundQueueEntries/Bytes` 双限约束，**永不阻塞 D 通道**（D 的溢出策略是 `drop-push-resync`，P 的是 `coalesce-last`，见 §6.1）。

### 4.3 Presence 快照（订阅时刻）

```text
subscribe(conversationId, principalId, agentId):
  1 判权（团队成员 ∩ 目标参与者）→ 失败 403
  2 先订阅（拿到 live 缓冲）
  3 再读快照：{ snapshotSeq, members: [{memberId, agentRef, displayRef, state, cursor?, typing?, goalId?, at}] }
  4 把缓冲中 seq ≤ snapshotSeq 的事件丢弃（只有 D 有 seq；P 无 seq → 按 at 栅栏 + 去重键）
  5 只下发可见成员的最小字段集；不得回落到全量枚举（SL3/SL11）
```

**规则 P-6（快照不等于授权缓存）**：快照只证明"订阅时刻"的成员集；成员变动必须靠 §4.1 的 `revoked` 主动推送收敛，**不得**靠客户端定时轮询对齐。

### 4.4 目标（Goal）对象——自主目标的载体

| 字段 | 必填 | 约束 | 说明 |
|---|---|---|---|
| `goalId` | ✔ | `gol_*` | 稳定标识；不得复用 |
| `ownerAgentRef` | ✔ | `agt_*` | **归属**：谁提出并负责 |
| `principalRef` | ✔ | `prn_*` / `unbound` | 谁授权；`unbound` 目标不得进入信用 |
| `teamRef` | ✔ | `team_*` | 可见域与配额域 |
| `conversationId` | ✔ | `cnv_goal_<goalId>` 派生 | 目标的定序域与轨迹切分域 |
| `successCriteria` | ✔ | 结构化（枚举 + 阈值 + 引用），**无自由文本** | 可判定的完成条件 |
| `evidenceRequirement` | ✔ | 枚举 `none/self/peer/human` | 决定"谁能证明完成" |
| `budget` | ✔ | `tokens/toolCalls/wallClockMs` 三者取先到 | 超限即中止（不是降级） |
| `deadline` | ✔ | 服务端时钟 | 到期即 `EXPIRED` |
| `maxRounds` | ✔ | 正整数 | 防无限循环 |
| `state` | ✔ | 见状态机 | — |
| `policyVersion` | ✔ | 宇宙策略版本 | 干预与晋升归属 |
| `version` | ✔ | 单调整数 | CAS 用 |

```text
PROPOSED ──accept──▶ ACTIVE ──pause──▶ PAUSED ──resume──▶ ACTIVE
                        │  │
                        │  └──blocked──▶ BLOCKED ──escalate──▶ ACTIVE（人或 Arbiter 解锁）
                        ├──budget/deadline/rounds 到顶──▶ ABORTED（返回已得最优结果）
                        ├──evidence 满足──▶ DONE
                        └──revoke/级联撤销──▶ ABORTED
PAUSED/BLOCKED ──超期──▶ EXPIRED
```

**规则 G-1（目标四不变量）**：①有主（`ownerAgentRef` 非空且为 `agt_*`）；②有界（三者预算 + 期限 + 轮次全非空）；③可中止（任何状态可在 ≤5s 内 `ABORTED`）；④可判定（`successCriteria` 与 `evidenceRequirement` 必须能让**第三方**判定，不能是"模型自认为完成"）。

**规则 G-2（目标状态只在 D 上变）**：`PROPOSED/ACTIVE/…` 的每一次迁移都是 D 事件（`kind=INTENT` 或 `AGENT_TURN` 的派生事件），带 `expectedVersion`；冲突 → `GOAL_VERSION_CONFLICT`。**禁止**用 presence 或 C 通道迁移目标状态（EV-2）。

**规则 G-3（中止不吞结果）**：任何因预算/期限/轮次中止的目标，必须落一条带 `partialResultRef` 的 D 事件；"中止但什么都没留下"视为实现缺陷。这与灵魂总架构 §4.4"中止 + 保留最优"一致。

### 4.5 任务（Task）与租约（Lease）——协作的执行面

| 对象 | 关键字段 | 说明 |
|---|---|---|
| Task | `taskId, goalId, assigneeRef, state, version, deps[], dueAt` | `deps` 无环（提交时校验） |
| Lease | `taskId, agentRef, generation, expiresAt, renewalSeq` | **执行权凭证**；不是权限凭证 |
| Assignment | `taskId, fromAgentRef, toAgentRef, reason(enum), evidenceRefs[]` | 指派/认领/移交都记一条 |
| Progress | `taskId, milestoneRef, percentBucket, at` | 里程碑 D；百分比只做分桶 |
| Artifact | `artifactId, taskId, contentHash, ownerAgentRef, lineage` | 不可变；归属见 §5.4 |

```text
OPEN ──claim/assign──▶ CLAIMED ──start──▶ IN_PROGRESS ──submit──▶ REVIEW ──accept──▶ DONE
  │                        │                    │                    │
  └──cancel──▶ CANCELLED    └──lease 过期──▶ OPEN   └──fail──▶ FAILED  └──reject──▶ IN_PROGRESS
```

**规则 T-1（CAS + 单一写者）**：每次迁移携带 `expectedVersion` 与 `clientMsgId`；服务端仲裁，冲突返回 `TASK_VERSION_CONFLICT` 并**附当前版本与持有者**（不静默覆盖、不静默重试）。

**规则 T-2（租约即执行权）**：同一 `taskId` 任一时刻至多一个有效租约（`(taskId) → (agentRef, generation)` 唯一）。续租失败（generation 落后或已过期）→ 该 agent **立即失去执行权**，其后续写入被拒 `LEASE_LOST`；过期租约可被他人 `claim`，旧 agent 的迟到写入必须被 fencing 拒绝（与 SEQ-2 同一机制）。

**规则 T-3（进度双表达，事实只有一份）**：里程碑进度是 D（低频，默认 ≤1/min，超出按 `milestoneRef` 合并）；"正在处理"是 P（`state=busy` + `leaseHeld=true`）。**P 丢失只影响观感，不影响任何判定**。

**规则 T-4（移交必须显式）**：`handoff` 必须带 `reason` 枚举（`capacity/blocked/expertise/revoked/expired`）+ `evidenceRefs`；**禁止**静默改 assignee。

**规则 T-5（任务板是投影）**：看板/甘特/燃尽图都是 O 面投影，**不是**可并发改写的共享对象。拖拽 = 提交一条迁移事件，服务端仲裁；因此**不需要 CRDT**（§4.7）。

**规则 T-6（依赖与级联）**：`deps` 提交时校验无环（`DEPENDENCY_CYCLE`）；`goal → ABORTED` 时子任务统一转 `CANCELLED` 并释放租约（一条 D 事件批量表达，带 `causationId`）。

**规则 T-7（完成的证据门槛）**：`REVIEW → DONE` 必须满足 `goal.evidenceRequirement`；不满足只能停在 `REVIEW` 或转 `BLOCKED`，错误码 `EVIDENCE_REQUIRED`。**"自称完成"不是完成**（这也是 SI-4 的实时版本）。

### 4.6 冲突与仲裁

| 冲突 | 检出 | 仲裁 | 不许 |
|---|---|---|---|
| 两 agent 抢同一任务 | 租约唯一约束 | 先到者得；后者 `LEASE_HELD_BY_OTHER` | 后写覆盖 |
| 两 agent 抢同一目标 | 目标 `version` CAS | 先提交者 ACTIVE；后者 `GOAL_VERSION_CONFLICT` + 转 PROPOSED 待 Arbiter | 双 authority 各写一份 |
| 指派与被指派者意愿冲突 | handoff 事件 | 被指派方可 `decline`（带理由），回到 OPEN | 静默拒绝 |
| 目标完成判定分歧 | 证据引用不一致 | 按 `evidenceRequirement` 升到人（approval ticket） | 模型互评决定 |
| 任务依赖成环 | 提交时图校验 | 拒绝 | 运行时才发现 |

**规则 C-1（仲裁结论必须落 D）**：任何仲裁结果都是一条带 `arbiterRef` + `ruleRef` 的 D 事件；**没有 D 记录的仲裁等于没发生**。

### 4.7 何时才需要 CRDT（明确闸门）

同时满足下列**任一**条件，才允许提"引入 CRDT/OT"的 ADR；否则一律用"日志 + CAS + 投影"：

1. 多个主体需要对**同一份富文本**做字符级并发编辑，且用户期望"两边的字都保留"。
2. 需要**长时间离线编辑后再合并**语义结构（不是消息队列补发）。
3. 出现**高频（>1Hz/人）的并发结构化拖拽排序**，且服务端仲裁的往返延迟不可接受。

**允许的轻量例外**：列表排序若真出现，用**分数索引（fractional indexing）+ 服务端决胜**，它是"可合并的表示"，不是 CRDT 运行时，不需新 ADR，但需在 2.9.x 的看板场景里单独评审。

### 4.8 验收（§4）

| ID | 断言 | 方法 |
|---|---|---|
| AC-P1 | 50Hz 推光标 10s → 对端合并后 ≤10Hz，最后位置胜出 | 压测脚本 + 抓包计数 |
| AC-P2 | 丢弃全部 P 帧后，任务板与目标状态 100% 一致 | 故障注入（P 全丢） |
| AC-P3 | 成员被踢出后 ≤1 个往返停止收到其 presence | 双会话交叉测试 |
| AC-P4 | presence 无自由文本字段（schema 负例 + 运行期扫描） | CI 负例 |
| AC-T1 | 两 agent 并发 claim 同一任务：恰一个成功，另一个拿 `LEASE_HELD_BY_OTHER` | 并发 100 轮 |
| AC-T2 | 租约过期后旧 agent 写入 100% 被 `LEASE_LOST` 拒绝 | fencing 负控 |
| AC-T3 | 目标超预算 100% `ABORTED` 且带 `partialResultRef` | 预算探针 |
| AC-T4 | 任务迁移乱序/重放 1000 次后状态唯一 | 幂等 fuzz |
| AC-T5 | 依赖成环提交 100% 被拒 | 负控 |
| AC-T6 | 任务板投影与 D 日志重算结果逐字段一致 | 对账脚本 |

---

## 5. 轨迹采集与审计

### 5.1 三层与边界

```text
D 日志（事实层，append-only，per-conversation seq）
   │  只读投影（不写回、不改 D）
   ▼
轨迹记录（证据层，JSONL，独立平面，独立保留期）
   │  摘要 + 哈希 + 签名（不复制正文）
   ▼
审计链（可验证层，hash chain + Ed25519 + checkpoint）
```

**规则 TR-1（单向）**：轨迹与审计都是 D 的**下游**。任何"轨迹反写 D"的路径都是缺陷。

**规则 TR-2（P/C/O 永不进轨迹）**：presence、control、O 面判定一律不落轨迹。**唯一例外**是 O 面的**判定结果摘要**可以进审计（不是进轨迹正文）。

### 5.2 轨迹边界（把散文变成字段）

现有契约把边界写在正文（`TRAJECTORY-RL-BOUNDARY.md:93`），但 `trajectory-record 1.0` 没有对应字段（N9），因此边界**不可校验**。2.7.3 起必须落成字段：

| 字段 | 规则 |
|---|---|
| `conversationRef` | `cnv_*`（或 `org_*` 伪名），必填 |
| `seqRange` | `{from, to}`，闭区间，必填；`from ≤ to` |
| `goalRef` | `gol_*` 伪名，必填（不再允许"一次会话里跨多个目标"） |
| `gapSeqs` | 切分处缺失的 seq 列表，必填（可为空） |
| `boundaryReason` | 枚举：`goal_closed / session_rotated / gap_exceeded / retention_floor / manual` |

**规则 TR-3（切分）**：同一 `goalRef` + 同一 `conversationRef` 内 seq 连续（缺口 ≤5）才算一个轨迹；缺口 >5、目标变更、会话域变更（`cnv_*` 换）必须切分。**跨设备重连不切分**（重连只换 `hs_*`，不换 `cnv_*`）。

**规则 TR-4（不可重建的记录必须标 incomplete）**：若采集期间 D 日志已越过保留窗口（`seq < floor`），该轨迹必须标 `terminal.status=partial` + `boundaryReason=retention_floor`，**不得**伪装成完整轨迹进入 train。

### 5.3 采集器（只读、主体身份、断点续跑）

| 约束 | 规则 | 理由 |
|---|---|---|
| 身份 | 采集器必须以**主体自己的**凭证读（或持显式 `grant`），禁止用服务令牌跨主体扫 | 沿用隐私评审 F-02/F-03/F-04 的失败面 |
| 只读 | 只允许 `history/replay` 读路径，禁止调用任何写工具 | 采集不得改变被采对象 |
| 断点 | 采集作业有 `(conversationRef, lastSeq)` 游标，可续跑；续跑成功 ≠ 数据完整 | Hermes `checkpoint.json` 的教训 |
| 幂等 | 同一 `trajectoryId` 重复生成必须内容一致（`contentHash` 相同），否则拒绝写 | 防"同一段经历两个版本" |
| 声明 | 作业必须记录 `datasetRef`（名称+版本+manifest 哈希）与 `collectorVersion` | 回答"这个权重用了谁的数据" |

### 5.4 记录字段增量（相对 1.0）与角色枚举缺口

| 字段 | 状态 | 说明 |
|---|---|---|
| `conversationRef`/`seqRange`/`goalRef`/`gapSeqs`/`boundaryReason` | **新增（1.1）** | §5.2 |
| `soulTriad` | **新增（1.1）** | 归因三要素；缺失 → 该记录不得作为晋升证据 |
| `assetRefs[]` | **新增（1.1）** | `{artifactId, ownerAgentRef, lineageDepth, contentHash}`；归属链 |
| `creditEvidence[]` | **新增（1.1）** | `{evidenceRef, evaluatorRef, weight, domain}`；信用事件的对外可证部分 |
| `policyVersion` | **新增（1.1）** | 干预与晋升归属 |
| `conversations[].from` | **扩枚举（1.1）** | 现只有 `system/human/gpt/tool/gate/approval`（N8）→ 必须加 **`peer_agent`**（带 `agentRef`），否则多 Agent 协作会被标成 `human`/`gpt` |
| `labels.maskedRoles` | **改语义（1.1）** | 由"恰好 5 个"改为"**除助手角色外的全部角色**"，否则新增角色会让掩码静默失效 |

**规则 TR-5（角色不得压平）**：`peer_agent`、`gate`、`approval`、`tool` 四类角色**永不进入 loss**；任何"把 gate/approval 压成文本"的实现等于教模型自批（沿用 `TRAJECTORY-RL-BOUNDARY.md:222-226`）。

**规则 TR-6（schema 演进同 EV-4）**：1.0 → 1.1 只加字段；兼容窗口内两版同收；窗口外旧版拒绝。`additionalProperties:false` 保持不变（结构性拒绝 deny-list，而不是事后扫描）。

### 5.5 审计链（从"追加"到"可证明未改写"）

现有 `audit.Entry` 只有 `At/MessageID/AgentID/UserID/Action/Decision/Detail`（`audit/log.go:33-41`），`FileSink` 是 `O_APPEND` JSONL（`:49-73`），**没有链、没有签名**（E13）——因此"审计"目前只能证明"写过"，不能证明"没被改过"。

| 层 | 机制 | 验证 | 失败处置 |
|---|---|---|---|
| L1 链 | `prevHash` + `hash = H(canonical(entry) ‖ prevHash)` | `verify()` 返回 `chain.ok/brokenAt` | `CHAIN_BROKEN` → 该段审计不可信，告警 P0 |
| L2 签名 | `signature = ed25519(identity, hash)` | 逐条验签 | `SIGNATURE_INVALID` → 阻塞发布 |
| L3 锚 | `checkpoint()` = `{seq, hash, alg, signature, at}` 定期外发/落对象存储 | 与链尾比对 | 缺失锚点 → 只可声明"内部一致"，不可声明"不可篡改" |

**规则 AU-1（动作枚举必须扩容，但只加不改）**：需新增 `GOAL_CREATED/GOAL_STATE/GOAL_ABORTED`、`TASK_TRANSITION/LEASE_GRANTED/LEASE_LOST`、`INTERVENTION_*`、`ASSET_PROPOSED/STAGED/PROMOTED/ROLLED_BACK`、`CREDIT_ISSUED/CREDIT_REVOKED`、`SESSION_SUPERSEDED/SESSION_REVOKED`。`Action` 是 `string` 类型（`audit/log.go:16`），扩容不破坏编译，但**消费方必须白名单未知动作而不是崩溃**（契约变更需写进 changelog）。

**规则 AU-2（审计不存正文）**：审计条目只存 `messageId/traceId/hash/判定/计数/引用`，正文留在 D 与轨迹。这样审计可长期保留，且不把个人信息二次扩散（SL12）。

**规则 AU-3（治理动作双写）**：任何干预（§6）都写一条 D 事件 + 一条审计条目；两者用 `causationId` 互链，**不得只写一边**（SL15）。

### 5.6 对账（"收敛"必须可验证）

| 对账项 | 周期 | 判据 | 不一致时 |
|---|---|---|---|
| 客户端状态摘要 `digest(conversationId, lastAppliedSeq, 可见状态摘要)` | 分钟级抽样 | 服务端重算一致率 100% | `state_digest_mismatch > 0` = 分歧事故（P0） |
| 轨迹 `contentHash` vs D 段重算 | 每数据集冻结时 | 逐条一致 | 该数据集冻结失败 |
| `seqRange` 覆盖 vs D 日志 | 每数据集 | 无缺口、无重叠 | 记录 `incomplete` |
| 信用账本重算 vs 在线 | 小时级 | 逐 `(agentRef, domain)` 一致 | `credit_ledger_mismatch = 0` 是红线 |
| 审计链 vs checkpoint | 每次门禁 | `verify().ok` | 阻断发布 |

**规则 RC-1（抽样必须随机且可复现）**：抽样键 = `H(salt ‖ conversationId)` 的桶，salt 每日轮换并记录；**不得**按"看起来重要的会话"抽样（会系统性漏掉冷会话的 bug）。

### 5.7 保留与删除

| 对象 | 保留 | 删除 | 演练 |
|---|---|---|---|
| D 日志 | `retentionSeqs` + 时间双条件（D-4 签字） | 过期分区删除 | 每季一次 |
| 轨迹 | `retention.retainUntil` | `deleteAfter` **必须可执行**；删除后 `contentHash` 留档在审计侧 | 每季一次（含残余扫描） |
| 审计 | 长期（只存哈希与判定） | legal hold 例外 | 不可删，只能加锚 |

**规则 RT-1（删除不等于遗忘）**：撤回同意后 30 天内必须"停止使用该 checkpoint 对外服务 + 重建数据集 + 重训或回退"（`TRAJECTORY-RL-BOUNDARY.md:350-364`）。实时底座的责任是：**保证 `subjectRef` 可稳定定位到 `conversationRef/seqRange` 集合**，否则撤回无法执行。

### 5.8 禁止（写下来是为了拒绝）

| 拒绝 | 理由 |
|---|---|
| 轨迹归 Agent 所有 | 会被当个人资本交易，污染信用 |
| 轨迹进 P 通道 | P 无审计、无 TTL 保证，等于绕过同意 |
| 把 O 面判定当事实写入轨迹正文 | O 可重算，写进去就变成了不可重算的"事实" |
| 采集器用服务令牌跨主体读 | 把越权读复制进训练集 |
| 用 `hs_*` 作为轨迹归属键 | 会话是临时件（ID-2） |
| 只截断不标记（`truncated` 缺省 false） | 静默截断（SI-4） |

---

## 6. 实时评估与干预

### 6.1 三类评估与冻结身份

| 类 | 何时 | 输入 | 输出 | 落点 |
|---|---|---|---|---|
| **在线门控判定** | 每次工具调用 | 工具名 + tier + `plannerClaimsApproved` | `ALLOW/REQUIRE_APPROVAL/DENY` | 审计 + 轨迹 `gate` 角色 |
| **轮次后评审** | 每轮结束 | 轨迹段 + 目标 `successCriteria` | 评分 + 失败归因（8 类） | 信用证据（D） |
| **周期回放** | 离线/日级 | 冻结 eval 集 | 回归报告 | evalRun 记录 |

**规则 E-1（评估身份五元组冻结）**：一次比较必须同时固定 `evaluatorVersion`、`promptSetHash`、`decoding`、`checkpointDigest`、`gatewayVersion`；任一变化 → 不是同一次比较（沿用 `TRAJECTORY-RL-BOUNDARY.md:408-411`）。

**规则 E-2（双评或人工抽检）**：单一 Evaluator 或单一模型评分不得作为晋升证据；需双评或 ≥10% 人工抽查（灵魂总架构 §4.5 反模式 5）。

### 6.2 评估者与四权分离

| 权 | 谁能持 | 与谁不得同一主体 |
|---|---|---|
| `propose` | Producer | `stage` / `evaluate` / `approve` |
| `stage` | Curator（独立身份） | `propose` / `approve` |
| `evaluate` | Evaluator（盲评） | `propose` / 被评产出的参与者 |
| `approve` | Operator（L6 人） | 任何前三者 |

**规则 E-3（自评禁止）**：评估者的 `agentRef` 不得出现在被评轨迹的 `assetRefs.ownerAgentRef` 或 `conversationRef` 参与者中；违反 → 该评估作废并记违约（这是 v2.5.3 缺口 A 在评估面的对应物）。

### 6.3 干预类型（封闭枚举，扩展需 SL6 复审）

| 干预 | 作用域 | 触发（示例） | 权限层 | D 记录要点 | C 加速 | 可回滚 | 默认有效期 |
|---|---|---|---|---|---|---|---|
| `PAUSE_TURN` | `turnId` | 连续工具失败 ≥3 | 自动（规则） | `reason,ruleRef,evidence` | ✔ | ✔（resume） | 15min |
| `RESUME_TURN` | `turnId` | 人工/规则解除 | 人/Arbiter | `by,note` | ✔ | — | — |
| `INTERRUPT` | `agentRef` | 预算超限/越权 | 自动或人 | `ruleRef,threshold` | ✔ | ✔ | 即时 |
| `REASSIGN_GOAL` | `goalId` | 租约连续过期/持有者被停 | 人/Arbiter | `from,to,reason` | ✔ | ✔ | — |
| `SUSPEND_AGENT` | `agentRef` | 制裁/违规 | 人（L6） | `ruleId,threshold,evidence,expiresAt` | ✔ | ✔（自动解除） | ≤30d |
| `FREEZE_EVOLUTION` | 宇宙/全局 | 漂移/回归 | 人 + 门禁 | `scope,reason` | ✔ | ✔ | 手动解除 |
| `THROTTLE` | `principal/agent/node` | 速率异常 | 自动 | `limit,window` | ✔ | ✔ | 15min |
| `ROLLBACK_SKILL_POINTER` | `skillId` | 回归 | 自动或人 | `from,to,expectedSha` | ✔ | ✔ | — |
| `QUARANTINE_ARTIFACT` | `artifactId` | 污染/未授权 | 人 | `reason,evidence` | ✔ | ✔ | 手动 |
| `FORCE_OFFLINE` | `hs_*`/`agentRef` | 撤销/接管 | 自动 | `cause` | ✔ | 否（终态） | — |

**规则 IV-1（无 ruleId 不干预）**：自动干预必须带 `ruleId + threshold + evidenceRef`；人工干预必须带 `by + note`。缺失 → 拒绝执行（不是"记录后执行"）。

**规则 IV-2（干预不得自触发）**：被干预对象不能是干预的发起者；Agent 只能**请求**干预（走 approval），不能执行干预。

### 6.4 双写协议（D 是事实，C 是加速）

```text
intervene(request):
  1 鉴权：发起者 ∈ 授权层（自动规则 / Arbiter / L6）
  2 判重：interventionId 命中 → 返回首次受理结果（IDM）
  3 写 D：kind=INTENT, payload={interventionId, type, scope, reason, ruleRef, threshold,
                                   evidenceRefs[], policyVersion, expiresAt, reversible}
  4 写审计：Action=INTERVENTION_*, causationId=interventionId
  5 尝试 C：cmd={type, interventionId, args{数值}}  → 至多一次，失败不回滚
  6 返回：{interventionId, dSeq, cDelivered: bool}
```

| 通道 | 必须携带 | **禁止**携带 |
|---|---|---|
| D | 理由、规则、阈值、证据引用、`policyVersion`、`expiresAt` | 无 |
| C | `interventionId`、封闭枚举 `type`、数值参数（`schema` 只允许 `integer/boolean`，`realtime-message.schema.json:129-134`） | 任何理由文本、证据正文、目标描述 |

**规则 IV-3（丢光 C 仍可重建）**：C 只是延迟优化。判据：C 通道 100% 丢弃时，被干预方在 ≤1 次 D 拉取内仍知道"我被干预了、依据哪条规则、什么时候到期"。

**规则 IV-4（C 不得先于 D 生效）**：允许"C 先到、D 后到"（网络乱序），但**不允许**"只有 C 没有 D"。若 C 已投递而 D 写失败 → 必须回滚该 C 的效果（按 `interventionId` 撤销），并告警。

### 6.5 幂等、超时与自动解除

| 项 | 规则 |
|---|---|
| 幂等 | `interventionId` 为键；重放返回首次结果（含 `dSeq`） |
| 超时 | 每个干预必须有 `expiresAt`；到期自动解除并落一条 `INTERVENTION_EXPIRED` D 事件 |
| 永久禁令 | **不存在永久干预**；永久放逐/永久冻结需人工复核并显式续期（灵魂总架构 R9） |
| 级联 | `SUSPEND_AGENT` 必须同时 `forceOffline` 其全部会话（SL10），未消费消息留在邮箱 |
| 撤销 | 干预撤销本身也是一条 D 事件（不删历史） |

### 6.6 自动化边界（治理公理）

| 允许自动（收敛风险） | 禁止自动（扩张权限/改变制度） |
|---|---|
| 限流、暂停轮次、熔断、回滚技能指针、隔离工件、强制下线、冻结进化 | 晋级（promote）、扩权（scope 提升）、信任等级提升、修改阈值/预算、开新端点、解除制裁、改变宪法 |

**规则 IV-5（自动化的单向性）**：自动动作只能让系统"更保守"。任何"自动变得更激进"的路径都是设计缺陷，必须在评审中拒绝。

### 6.7 干预的可见性

| 角色 | 看到什么 |
|---|---|
| 被干预方 | 完整 D 事件（类型/规则/阈值/证据引用/到期时间） |
| 同团队成员 | 聚合计数（"本轮 N 次限流"），**不含**个人判定细节 |
| 旁观者/公众 | 仅聚合指标 |
| 审计方 | 全量（含 C 投递结果） |

**规则 IV-6（不得广播羞辱）**：presence 与公共频道**不得**出现"某 agent 被暂停"的事实；只允许聚合指标。这与 SL6 同源：一旦允许，presence 就变成了制裁公告板。

### 6.8 干预与信用

**规则 IV-7（干预是证据，不是判决）**：干预可进入信用证据，但**必须由评估方签名**，且作为**负向证据**而非直接扣分；扣分规则在信用账本（W3）里统一裁决。干预本身不直接改声誉——否则"谁掌握干预权谁掌握声誉"。

**规则 IV-8（申诉通道）**：任何 `SUSPEND_AGENT`/`QUARANTINE_ARTIFACT` 必须给出 `ruleId + threshold + 证据引用`，且可 `Appeal`（复用 `prison.Engine.Appeal`）；无依据的制裁会让 Agent 行为退化为随机试探。

### 6.9 验收（§6）

| ID | 断言 | 方法 |
|---|---|---|
| AC-I1 | 无 `ruleId` 的自动干预 100% 被拒 | 负控 20 次 |
| AC-I2 | C 全丢后，被干预方仍能重建干预事实与到期时间 | 故障注入 |
| AC-I3 | `interventionId` 重放 100 次只产生 1 条 D、1 次效果 | 幂等 fuzz |
| AC-I4 | 到期 100% 自动解除并落 D 事件 | 时间旅行测试 |
| AC-I5 | Agent 自触发干预 100% 被拒 | 越权负控 |
| AC-I6 | 自动晋升/扩权路径 0 条（静态扫描 + 运行时钩子） | 代码审计 |
| AC-I7 | `SUSPEND_AGENT` 后 5s 内全部会话 `REVOKED` 且邮箱保留下未消费消息 | 端到端 |
| AC-I8 | 干预详情不出现在 presence/公共频道（0 命中） | 响应体扫描 |

---

## 7. 可观测性

### 7.1 四个观测面

| 面 | 回答 | 载体 | 不得混入 |
|---|---|---|---|
| 指标 | 系统健康与容量 | `internal/metrics`（`metrics.go:22,38,87`） | 主体级明细（高基数 + 隐私） |
| 审计 | 谁在何时做了什么、依据什么 | 审计链（§5.5） | 正文 |
| 轨迹 | 行为样本与归因 | 证据平面（§5） | P/C/O |
| 日志 | 排障 | 结构化日志 | deny-list（SL12） |

### 7.2 指标全集（前缀 `im2a_rt_`）

| 指标 | 类型 | 标签（封闭枚举） | 判定 |
|---|---|---|---|
| `sessions_online` | gauge | `universe,team` | 容量 |
| `session_register_total` | counter | `outcome{ok,rejected},unbound{true,false}` | `unbound` 占比应随 W0 收敛到 0（除共享桥） |
| `session_superseded_total` | counter | `reason` | 与网络质量相关，**非错误** |
| `session_offline_total` | counter | `reason{heartbeat,sse_error,client_closed,drain,slow_consumer}` | N3：必须能区分 `slow_consumer` |
| `resume_total` | counter | `outcome{replay,resync,cursor_invalid,denied}` | `resume_success_rate < 99%` 告警 |
| `resync_required_total` | counter | — | `resync_required_rate > 1%` 告警 |
| `seq_gap_count` | counter | `conversation_class` | **>0 即事故** |
| `dedupe_hit_total` | counter | `op{send,intervene,task}` | 突增 = 重试风暴 |
| `outbound_drop_total` | counter | `channel{D,P,C}` | D>0 需复盘；P 丢弃应为常态 |
| `outbound_queue_bytes` | histogram | `channel` | 双限触发率 |
| `op_apply_latency_ms` | histogram | `channel` | p95 <150ms（同区域） |
| `fanout_latency_ms` | histogram | `team_size_bucket` | 扇出尾延迟 |
| `presence_coalesced_hz` | histogram | `team_size_bucket` | >10Hz 告警 |
| `presence_fanout_bytes` | counter | — | 无订阅者时必须为 0（P-3） |
| `sse_write_error_total` | counter | `kind{broken_pipe,timeout,slow}` | 与 N3 对齐 |
| `history_page_total` | counter | `has_more{true,false}` | 静默截断的探测器（N4） |
| `goal_state_total` | counter | `state` | 目标积压/流失 |
| `goal_aborted_total` | counter | `reason{budget,deadline,rounds,revoked,conflict}` | 中止原因分布 |
| `lease_conflict_total` | counter | — | 抢任务频率 |
| `task_transition_total` | counter | `from,to` | 状态机热区 |
| `intervention_total` | counter | `type,auto{true,false}` | 自动干预比例 |
| `intervention_expired_total` | counter | `type` | 自动解除是否生效 |
| `credit_event_total` | counter | `domain_bucket` | 信用流速 |
| `credit_ledger_mismatch` | gauge | — | **>0 即事故** |
| `asset_ownership_violation` | counter | — | **>0 即事故**（SI-1） |
| `state_digest_mismatch` | counter | — | **>0 即分歧事故** |
| `trajectory_gap_total` | counter | `boundary_reason` | 切分质量 |
| `policy_stale_sessions` | gauge | — | >0 表示有会话在过期政策上运行 |
| `degraded_total` | counter | `reason`（枚举） | SI-4：任何降级必须显式 |

### 7.3 基数与隐私

**规则 O-1（标签白名单）**：标签只能取自本表的封闭枚举；**禁止** `sessionId/agentId/principalId/conversationId` 直接做标签。需要下钻时走"日志 + trace"，不进指标。

**规则 O-2（每宇宙预算）**：单宇宙指标序列数上限（建议 5k），超限按 `team` 聚合；否则一个恶意团队可以打爆 Prometheus。

**规则 O-3（不得把 Presence 做成指标标签）**：`presence` 的状态只能聚合为计数，不得按成员打标签。

### 7.4 日志与追踪

- `traceId` 全链透传（信封已有，`INTERFACES.md:366`）；新增 `causationId` 串起"干预 → 目标 → 任务 → 产物"。
- 日志**禁止**出现 deny-list（SL12），包括：鉴权码、任何 Bearer/API key、`wxid/open_id`、手机/邮箱/身份证、`deviceId/client_ip/user_agent`、`users.identifier` 明文、原始 `dsh-session-ref`、文件绝对路径与内网 URL。
- 日志采样可调，**审计不可采样**（审计是证据）。

### 7.5 告警分级

| 级别 | 指标 | 处置 |
|---|---|---|
| P0（立即 page） | `seq_gap_count>0`、`state_digest_mismatch>0`、`credit_ledger_mismatch>0`、`asset_ownership_violation>0`、审计 `CHAIN_BROKEN`、越权读探针命中 | 停止发布/回滚 |
| P1（15min） | `resume_success_rate<99%`、`resync_required_rate>1%`、`outbound_drop_total{D}>0`、`op_apply_latency p95>150ms`、`presence_coalesced_hz>10` | 值班处理 |
| P2（日） | `goal_aborted_total` 结构变化、`intervention_total{auto}` 比例异常、`trajectory_gap_total` 上升 | 周报复盘 |

**规则 O-4（"必须能红"）**：每个 P0 指标必须有一个**已知会触发它的演练脚本**；从未红过的告警等于没有告警。

### 7.6 健康检查与 drain 语义

| 端点 | 语义 | 注意 |
|---|---|---|
| `/healthz`（liveness） | 进程活着 | 不查依赖；避免依赖抖动导致重启风暴 |
| `/readyz`（readiness） | 可接收新工作 | DB 不可用时应 **not ready**（fail-closed），并停止分配 seq |
| `serverState:"draining"` | 摘流中 | 必须出现在心跳/接收类工具结果里，客户端据此**带抖动**重连 |

**规则 O-5（drain ≠ unhealthy）**：`DRAINING` 期间 readiness 返回 not-ready（让 LB 摘流），但 liveness 必须仍绿；把 drain 报成 unhealthy 会触发"重启 → 更长的停机"。

---

## 8. 容量与故障模型

### 8.1 公式（先给口径，不给承诺数字）

| 量 | 公式 | 当前值/说明 |
|---|---|---|
| 每连接内存 | `base + outboundQueueEntries × avgPushBytes + 缓冲` | 双限 256 条 / 1MiB（ADR 0006 D9 选定值，进 profile `const` 前需签字，D-4） |
| 扇出带宽 | `在线成员数 × 事件率 × avgBytes` | presence 合并后 = `成员数 × 10 × ~200B` |
| 单会话 seq 吞吐 | 受 `msgseq` 单分片锁限制：`64 分片 × 每分片串行` | 拆片信号 `HotShardIDs`（`msgseq.go:93-105`） |
| 事件可见性成本 | `连接数 × 事件率 × 2 次全局锁` | N2：`EventVisibleTo` 每事件取锁（`bus.go:307`） |
| 历史读成本 | `O(内存条数)` 线性扫描 + 过滤（`bus.go:269-280`） | 500 条 × 每请求；D 落库后应改索引查询 |
| 存储 | `payload + 索引 + 审计哈希` × 保留窗口 | `retentionSeqs` 决定重放窗口与磁盘 |
| presence 空转 | `订阅者 × 会话数 × 快照字节` | 现为 200×200×~200B ≈ 8MB/次注销（P-3 修掉前） |

**规则 CP-1（全局硬编码必须消失）**：`MaxSessions=200`（`bus.go:24`）是**进程级**上限，必须改为 `(universe, team)` 配额 + 全局护栏；否则**一个热团队就能打死整个进程**，这是当前最现实的容量故障。

### 8.2 目标形态与必须测量的量

| 场景 | 目标形态 | 必须实测 | 不能声明 |
|---|---|---|---|
| 1 个热会话 × N 在线 | 单进程 SSE + 合并 presence | 扇出 p95、内存峰值、`outbound_drop{P}` | "支持 N 人同房间"（无 N 的出处） |
| M 个冷会话 × 2 人 | 分片（按 team/universe 路由） | 连接建立成本、会话表内存 | "支持 M 万会话" |
| 多节点 | 单写者 per conversation + backplane 只做通知 | 跨节点投递延迟、重放对账 | "跨机房实时" |
| 长会话 | D 落库 + `retentionSeqs` | 重放吞吐、`RESYNC_REQUIRED` 率 | "永不丢消息" |

### 8.3 故障模型（F1–F12）

| # | 故障 | 触发 | 表现 | 检测 | 必须成立的不变量 | 处置 |
|---|---|---|---|---|---|---|
| F1 | 单节点崩溃 | 进程死/主机重启 | 内存历史丢失、连接全断 | 心跳缺失 + 重启计数 | 客户端 `RESYNC_REQUIRED` 后状态一致，**无静默缺口** | 从 D 日志重放/快照 |
| F2 | 滚动部署 | 正常发布 | 同频重连（惊群） | `session_register_total` 尖峰 | 重连 ≤5s、`retryAfterMs` 带抖动、零丢失 | drain + 全抖动退避 |
| F3 | 客户端长离线 | 笔记本合盖 | 超出保留窗口 | `resync_required_rate` | 降级为快照而非"假装重放" | 快照 + 显式 `resync` |
| F4 | 慢消费者 | 不读 SSE | 服务端内存涨 | `outbound_queue_bytes` + `slow_consumer` | 内存有界；D 转 `resyncRequired`，**不静默丢** | 断链 + 重放 |
| F5 | 热会话扇出打满 | 全员大会 | CPU/带宽饱和 | `fanout_latency` p95 | 降级为"合并 + 降频"，不丢 D | 限流 + 合并 |
| F6 | 无 D 日志的 backplane 丢失 | 多节点分区 | 事件丢、无重放 | 对账 `digest` 不一致 | **backplane 只做通知，真相在 D**（N7：`eventbus` 不可当真相） | 客户端按游标补 |
| F7 | 双 authority | 分区后两侧都写 | **seq 断裂/重号** | `seq_gap_count>0` | 过期 `fencingEpoch` 写入必被拒（SEQ-2） | fencing + 强制单写者 |
| F8 | 主体断言伪造 | 客户端自带 `principalId` | 账本记错人 | 越权探针 | 断言只能来自中间件（ID-1） | 401/拒绝 |
| F9 | 存储不可用 | PG down | 无法分配 seq | readiness | **停止分配 seq**（fail-closed），不分配错误 seq | 只读降级 + 503 |
| F10 | presence 洪泛 | 50Hz 光标/恶意客户端 | 扇出过载 | `presence_coalesced_hz` | 合并到 ≤10Hz，**不影响 D** | 限频 + 丢弃 |
| F11 | 撤销风暴 | 级联撤销一棵大树 | 瞬时断连风暴 | `session_offline_total{reason}` | 撤销 ≤5s 生效；未消费消息留邮箱 | 分批 + 抖动 |
| F12 | 时钟偏移/时区 | 客户端时钟乱 | TTL/租约误判 | 租约异常 | 定序不用客户端时钟（SEQ-1）；TTL/租约用**服务端单调时钟** | 服务端时间唯一权威 |

### 8.4 负载与"必败"测试（CI 必跑，不靠演示日）

| 测试 | 断言 |
|---|---|
| 杀连接于发送中（200 次） | 恰好一条消息；`dedupe_hit=1`；无 `seq_gap` |
| 离线 1h + 200 条积压 | 全部重放、无空洞、顺序一致 |
| 两客户端同时改同一任务字段 | 一个成功一个 `TASK_VERSION_CONFLICT`；不静默覆盖 |
| 部署中滚动重启 | 零丢失；重连 ≤5s；`session_superseded` 分散（无同频尖峰） |
| 慢消费者灌 2000 条 | 内存 ≤1MiB/连接；D 转 resync；补发后一致 |
| **过载必须失败** | 超配额时**拒绝**（`CAPACITY_EXCEEDED`）而不是降级/丢弃；降级 = 测试失败 |

---

## 9. 版本切片（54 个交付点）与验收标准

### 9.1 算术与门禁节奏（先解决 X1）

```text
W0 预研      2.5.4  – 2.5.13   10 版   → N1 @2.5.13
W1 自主目标  2.6.0  – 2.6.9    10 版   → N2 @2.6.9
W2 技能归属  2.7.0  – 2.7.9    10 版   → N3 @2.7.9
W3 可验证信用 2.8.0 – 2.8.9    10 版   → N4 @2.8.9
W4 节点与经济 2.9.0  – 2.9.9    10 版   → N5 @2.9.9
W5 宪法治理  3.0.0  – 3.0.3     4 版   → 终认证 N6 @3.0.3
                                  ─────
                        50 能力版 + 4 收口版 = 54 个交付点
```

- **"50 小版本"= W0–W4**（能力线）；**3.0.0–3.0.3 是宪制收口与终认证**，不计入 50。这一拆分同时满足"每 10 版一次九门禁"（N1–N5）与"区间终点 3.0.3"。**待决 D-1。**
- 大版本落点：`2.6.0 / 2.7.0 / 2.8.0 / 2.9.0`（四个"大"）+ `3.0.0`（"超大"）。与"大版本落在窗口首版"配套的是：**门禁在该窗口第 10 版**，届时 `.0` 已有 10 个数据点——比灵魂总架构"`.0` + 3 版"的方案多 6 个数据点（X3 的修正理由）。
- **与灵魂总架构的对齐**：本文替换其 §5.2 的窗口边界（原方案跨 minor 线且静默跳过 2.5.10–2.5.13）；其 W1 的 soul 契约内容按 X3 拆分安置：`soulTriad` 字段落点 → W0 2.5.12；漂移矩阵 → W5 3.0.0；EVO-5 闭环 → W3/W4（X4，待决 D-3）。

### 9.2 W0 预研（2.5.4–2.5.13）：把错误的地基换掉

> 主题不变量：**不能被重放的事实不是事实，不能被归属的连接不是主体。**

| 版本 | 交付物（单一增量） | 验收（可执行断言） | 回滚点 |
|---|---|---|---|
| **2.5.4** | 排序域：`conversationId` + per-conversation `seq`（接线 `msgseq`，`conversation_ord` 映射表），`id` 与 `seq` 双写 | 同会话从 1 严格递增、跨会话独立；重启按会话播种（取全局 MAX 即失败）；`(cnv,seq)` 唯一约束拒绝重复；v2.5.3 客户端读 `id` 行为不变 | 上一 tag（双写可逆） |
| **2.5.5** | SSE 可恢复：写 `id:`、`Last-Event-ID`、先订阅后快照 + `snapshotSeq` 栅栏（RES-2） | 断流 30s 重连零重复零缺口；快照/订阅竞争 1000 次零丢失；`id:` 行存在（RT-07） | 上一 tag |
| **2.5.6** | 主体断言接线：读 `auth.UserIDFromContext`，`Session` 增 `principalRef`；无断言 → `unbound`（ID-1/ID-3/SL14） | 伪造 `principalId` 被拒；JWT 会话 `principalRef` 正确；`unbound` 会话加入团队/被委托/进轨迹均被拒（20 次负控） | 上一 tag |
| **2.5.7** | 幂等：`clientMsgId` + 去重（键 `principal:clientMsgId`，TTL 24h，**返回首次结果**） | 同键双发一条且返回同一 `messageId/seq`；24h 后重放仍命中；跨 principal 撞键=两条（RT-11/12/13） | 上一 tag |
| **2.5.8** | 可见性按主体：`EventVisibleTo`/`History` 改 `(universe,principal,agent,conversation)` + 成员表；`to` 降级为路由提示（SL2/SL13/RES-4） | 接管/轮换/重注册后历史可见性不变（G11 回归）；A 收不到 B→C；同名两会话互发 0 命中（N1） | 上一 tag |
| **2.5.9** | 会话状态机 + takeover + 心跳/TTL + 撤销断连 + 分页 `hasMore`（SES-1/2/3、SL10、RES-5/6） | 断网 30s 内重连成功、旧链 `superseded`；撤销 5s 内 `REVOKED`；401 后旧能力被清；历史响应必带 `hasMore/nextSeq` | 上一 tag |
| **2.5.10** | 背压分通道：双限（entries/bytes）、D `drop-push-resync`、P `coalesce-last`、C `drop-oldest`；`dropped` 分通道分连接；`slow_consumer` 可辨识（N3/N6） | 慢消费者内存 ≤1MiB；D 必转 `resyncRequired` 且补发后一致；P 合并 ≤10Hz；`session_offline_total{reason=slow_consumer}` 可查（RT-14/15/16） | 上一 tag |
| **2.5.11** | 持久日志与快照：D 落 PG（`messages` + `(conversation_id,seq)` 唯一），历史/快照从库读；`retentionSeqs`/`maxBatch`（SEQ-3/RES-3） | 重启后重放一致；离线 1h/200 条无空洞；`<floor` 返回 `RESYNC_REQUIRED` 且带 `snapshotSeq`；`>head` 返回 `CURSOR_INVALID`（RT-08/09） | 数据迁移可逆脚本 |
| **2.5.12** | 观测面 O + 收敛抽样 + `soulTriad` 字段落点：`state_digest`、`seq_gap`、`resume_success_rate`、`dedupe_hit_rate`、`presence_coalesced_hz`、`outbound_drop{channel}`、`session_superseded`；O 面只派生不投递 | 指标全在 `/metrics` 且带白名单标签；digest 抽样比对可运行；`seq_gap>0` 与 `digest_mismatch>0` 有告警且演练可红；`unbound` 占比可见 | 上一 tag |
| **2.5.13** | **【N1 九门禁】** 预研收口：契约 schema 1.1 冻结（D/P/C + profile）、SL1–SL16 全绿、5 个原语（`msgseq`/`store`/`audit`/`metrics`/`stream`）全部接线、收敛抽样可运行、`evidences/rt-*` 落齐 | 九方 9 维 + 16 条 SL 全绿；**1 个负控必须红**（故意不修 G11 → 门禁红）；RT-00..RT-21、RT-23、RT-24 全部 PASS 或"带缺陷单的 FAIL"，RT-22 允许 `CAPABILITY_MISSING`（D-10），**不允许 `UNVERIFIED` 残留** | 2.5.12 |

### 9.3 W1 自主目标（2.6.0–2.6.9）：目标是有主、有界、可中止的对象

> 主题不变量：**不能被归因的"自主"不是自主，是漂移。**

| 版本 | 交付物 | 验收 | 回滚点 |
|---|---|---|---|
| **2.6.0** | **【大版本】Goal Contract 1.0**：`gol_*` 对象 + 状态机 + 四不变量（G-1）+ `cnv_goal_*` 派生 | 目标必须带 owner/预算/期限/轮次/成功判据；缺一即拒；状态迁移全走 D；`GOAL_VERSION_CONFLICT` 可复现 | 2.5.13 |
| 2.6.1 | 目标×轮次绑定：`turnId/goalId` 落到 `AGENT_TURN`；轨迹边界 = `(goalId, conversationId, seqRange)` | 一次轨迹不含两个 goal；跨重连不切分；`boundaryReason` 可判定（AC 与 §5.2 一致） | 上一 tag |
| 2.6.2 | 目标租约：`(goalId → agentRef, generation, expiresAt)`，续租即 D 事件 | 双写被 fencing 拒绝（`LEASE_LOST`）；过期目标可被接管；续租失败 ≤1 心跳内可见 | 上一 tag |
| 2.6.3 | 进度：里程碑 D（≤1/min，超出按 `milestoneRef` 合并）+ `queueDepth` 分桶进 P | P 全丢后进度视图仍由 D 重建（AC-P2）；进度不产生未读 | 上一 tag |
| 2.6.4 | 目标冲突与仲裁：CAS + 仲裁工单（`approval.Ticket`），仲裁结论落 D | 两 agent 抢同一目标 100 次 → 恰一个 ACTIVE，另一个 PROPOSED；仲裁无 D 记录即失败 | 上一 tag |
| 2.6.5 | 终止条件执行：预算/期限/轮次到顶 → `ABORTED` + `partialResultRef`（G-3） | 三类上限各自 100% 中止且带部分结果；中止后无残留租约 | 上一 tag |
| 2.6.6 | 升级路径：`BLOCKED → escalate → 人类决策`，超时默认拒绝（fail-closed） | 超时未决 → `EXPIRED`；任何自动解锁路径 0 条 | 上一 tag |
| 2.6.7 | 目标与委托：子目标 scope 收窄（SI-2）、父撤销级联、未消费消息留邮箱 | 子目标 scope 超父 100% 拒（`SCOPE_ESCALATION`）；父撤销 5s 内子目标 ABORTED | 上一 tag |
| 2.6.8 | 目标审计与回放：`GOAL_*` 动作入审计链；目标时间线可由 D 重建 | 任取一目标可重建完整时间线；审计链 `verify().ok` | 上一 tag |
| **2.6.9** | **【N2 九门禁】** 自主目标门禁 | AC-T1..T6 + AC-P2 + AC-I4 全绿；**2 个负控必须红**（无租约双写、超预算不自止） | 2.6.8 |

### 9.4 W2 技能归属（2.7.0–2.7.9）：经验资产有主、有链、可回滚

> 主题不变量：**所有权不授予写穿权。**

| 版本 | 交付物 | 验收 | 回滚点 |
|---|---|---|---|
| **2.7.0** | **【大版本】Asset Provenance 1.0**：`assetRefs{artifactId,ownerAgentRef,lineage,contentHash}` 进 D 事件与轨迹 | 每条资产事件带 owner 与 lineage；缺任一即拒；`contentHash` 可复算 | 2.6.9 |
| 2.7.1 | 资产事件流：`propose/stage/eval/promote/archive` 全部 D 事件 + `causationId`；P/C 0 承载 | 资产事件在 P/C 中 0 命中（响应体扫描）；`causationId` 可串起全链 | 上一 tag |
| 2.7.2 | 归属校验器（SI-1）：`writer ≠ owner` 的产物写入拦截（运行时 + CI 双点） | 20 次越权写入 100% 拒；检查器**不可配置**（无 env 开关） | 上一 tag |
| 2.7.3 | 轨迹契约 1.1：`conversationRef/seqRange/goalRef/gapSeqs/soulTriad/assetRefs` + `peer_agent` 角色 + `maskedRoles` 语义修正（N8/N9） | 旧记录投影后通过 1.1；多 Agent 轨迹不再出现 `human/gpt` 误标；31+ 既有契约测试仍绿 | 迁移可逆脚本 |
| 2.7.4 | 记忆三权字段化（use/benefit/dispose）+ 跨代理读审计 | 未带 grant 的跨读 0/500 成功（AC-SO8） | 上一 tag |
| 2.7.5 | 导出收窄：整用户 → `scope + 目的 + 审批` | 整用户级导出路径 0 条；越权导出 20 次全拒（AC-SO9） | 上一 tag |
| 2.7.6 | fork 与 lineage 完整性：id 全局唯一、fork 必声明 base | id 抢占 20 次全拒；lineage 断裂 0（AC-SO10） | 上一 tag |
| 2.7.7 | 资产回放：由 D 重建"谁在何时改了什么"的时间线 | 任取资产可重建时间线；与 `contentHash` 对账一致 | 上一 tag |
| 2.7.8 | 团队资产可见域：成员表 + 归属决定可见性，接管/轮换不改 | 换会话后可见性不变；跨团队读 0/500 | 上一 tag |
| **2.7.9** | **【N3 九门禁】** 技能归属门禁 | AC-SO8/9/10 + 归属负控全绿；**3 个负控必须红**；eval ≥50 例/agent | 2.7.8 |

### 9.5 W3 可验证信用（2.8.0–2.8.9）：信用只由被签名的结果产生

> 主题不变量：**约束先于能力；不许自证。**

| 版本 | 交付物 | 验收 | 回滚点 |
|---|---|---|---|
| **2.8.0** | **【大版本】Credit Contract 1.0**：`credit` 事件（`evidenceRef, evaluatorRef, weight, domain, decay`）+ append-only 账本 | 账本可复算（同输入同输出）；事件不可改；`credit_ledger_mismatch=0` 可测 | 2.7.9 |
| 2.8.1 | 证据签名：Ed25519 + 盲评要求；自述证据 0 计分 | 自述证据 0 计分（AC-SO2）；伪造签名 100% 拒 | 上一 tag |
| 2.8.2 | 信用只从 D + 轮次生命周期派生（O 面重算） | 同段 D 重算两次一致；采样对账 100% | 上一 tag |
| 2.8.3 | 反合谋：循环背书检测（>20% 降权）、互评稀释（>30% 归零）、不可交易 | 3 组构造合谋全部降权（AC-SO3）；转赠路径 0 条 | 上一 tag |
| 2.8.4 | 信用与自主度阶梯挂钩（**分数不进提示词**） | 升降级迟滞内抖动 0（AC-SO5）；提示词中分数 0 处（静态扫描，AC-SO12） | 阶梯表回滚 |
| 2.8.5 | 制裁与申诉：`prison` 接线 + 期限 + 自动解除 + `ruleId/阈值/证据` | 无依据制裁 0（AC-SO6）；到期自动解除 100%（AC-SO7） | 上一 tag |
| 2.8.6 | 信用对账：小时级重算 + 抽样 digest | 差异 >0 立即 P0 告警并可复现 | 上一 tag |
| 2.8.7 | 领域切分：`domain` 隔离，跨域不继承 | 跨域自动继承 0（AC-SO4） | 上一 tag |
| 2.8.8 | 信用可见域：本人档位 + 聚合；P 通道 0 承载 | 他人明细 0/500 可读；presence 中 0 命中 | 上一 tag |
| **2.8.9** | **【N4 九门禁】** 信用门禁 | 证据可签名 100%、自述 0、跨域 0、eval ≥100 例/agent；**4 个负控必须红** | 2.8.8 |

### 9.6 W4 技能节点与经济（2.9.0–2.9.9）：节点可寻址、可计量、可结算

> 主题不变量：**计量的原子是"一次调用"，不是"一次在场"。**

| 版本 | 交付物 | 验收 | 回滚点 |
|---|---|---|---|
| **2.9.0** | **【大版本】Skill Node 1.0**：`nodeId = skillId@version + 载体声明`；节点注册是 D 事件；`provides/requires` 契约 | 节点可寻址、可发现；`requires ⊄ provides` 100% 发 `soul-degraded`（AC-S3） | 2.8.9 |
| 2.9.1 | 节点路由：能力匹配 + 就近 + 降级链（显式 `reason`） | 降级必带 `reason+skipped[]`（SI-4）；静默降级 0 | 上一 tag |
| 2.9.2 | 计量：`callId` 幂等记账（token/工具/墙钟），D 事件 + 账本 | 重放 100 次只计一次；计量可复算 | 上一 tag |
| 2.9.3 | 预算与限流分主体：principal 外层 / agent 内层 / node 配额（双层限流） | 委托 4 子后总速率 ≤ `ratePerPrincipalPerMin`（RT-20）；审计区分内外层 | 上一 tag |
| 2.9.4 | 节点信用与派单：信用影响**队列位置**，不影响价格 | 高信用优先可复算；价格与信用无函数关系（静态检查） | 上一 tag |
| 2.9.5 | 结算与对账：双边账本，差异即事故 | 结算差异 0；用 presence 计费的路径 0 条 | 上一 tag |
| 2.9.6 | 节点下线：租约到期 + drain + 客户端重路由 | 下线 5s 内新任务不再路由到该节点；在途任务不丢 | 上一 tag |
| 2.9.7 | 跨团队节点共享：`scope + grant`（限时、可撤） | 未授权共享 0/500；撤销 ≤5s 生效 | 上一 tag |
| 2.9.8 | 成本硬上限：单目标/单主体/单节点预算闸 | 超限 100% 中止，0 次跑穿（AC-E 系列成本项） | 上一 tag |
| **2.9.9** | **【N5 九门禁】** 经济门禁 | 计量可复算、结算差异 0、超预算 0 跑穿、eval ≥200 例/agent；**5 个负控必须红** | 2.9.8 |

### 9.7 W5 宪法治理（3.0.0–3.0.3）：能证明，才发布

> 主题不变量：**任何可配置的边界都不是边界。**

| 版本 | 交付物 | 验收 | 回滚点 |
|---|---|---|---|
| **3.0.0** | **【超大版本】宪制 1.0**：SI-1..4 + 6 禁令入宪法文件；检查器 CI + 运行时双点且**不可配置**；漂移矩阵（X3）；冻结三档（`evolution.freeze`/`agent.suspend`/`fleet.halt`） | SI 四条各有负控被拦（AC-S5）；宪法可配置覆盖路径 0 条（AC-S6）；漂移矩阵四种判定均可达；三档演练记录在案 | 2.9.9 |
| 3.0.1 | 稳定化：**仅缺陷修复，0 新能力** | 新增能力数 = 0（diff 审计）；P0 缺陷归零 | 3.0.0 |
| 3.0.2 | 契约/兼容矩阵冻结：文档与 CI 同源（含本文 §3.3/§5.4 的 schema 版本矩阵） | 兼容矩阵即产物；schema 1.0/1.1 双读窗口有测试 | 3.0.1 |
| **3.0.3** | **【终认证 N6】** 九方 9 维 + 灵魂 6 维 + SL1–SL16 + 本文 AC 全量 | 全绿 + `security-floor.json` + `rt-evidence/*` 归档；**负控必须红** | 3.0.2 |

### 9.8 每版交付契约与负控棘轮

**每个小版本必须同时满足**（缺一即不发布）：

1. **可独立部署**：不依赖下一版。
2. **可独立回滚**：明确回滚点；数据迁移可逆性必须写明。
3. **不留半更新状态**：指针/迁移要么完成要么不开始。
4. **对外面要么不变、要么显式升版**：工具数、schema 版本、错误码集合的变更必须进 changelog。
5. **一条执行验收命令 + 一个负控 + 一条 changelog**。

**禁止**：跨版本"临时不兼容窗口"；用 feature flag 承载安全边界（安全边界只能靠版本）。

| 指标 | N1 @2.5.13 | N2 @2.6.9 | N3 @2.7.9 | N4 @2.8.9 | N5 @2.9.9 | N6 @3.0.3 |
|---|---|---|---|---|---|---|
| SL1–SL16 | 全绿 | 全绿 | 全绿 | 全绿 | 全绿 | 全绿 |
| `seq_gap_count` | 0 | 0 | 0 | 0 | 0 | 0 |
| `state_digest_mismatch` | 0 | 0 | 0 | 0 | 0 | 0 |
| `resume_success_rate` | ≥99% | ≥99.5% | ≥99.5% | ≥99.5% | ≥99.9% | ≥99.9% |
| `resync_required_rate` | ≤5% | ≤3% | ≤2% | ≤2% | ≤1% | ≤1% |
| `op_apply_latency p95` | ≤300ms | ≤250ms | ≤200ms | ≤200ms | ≤150ms | ≤150ms |
| eval 用例/agent | — | ≥20 | ≥50 | ≥100 | ≥200 | ≥300 |
| 必须失败的负控 | 1 | 2 | 3 | 4 | 5 | 5（+ 每次必红） |

**规则 GATE-1（反仪式化）**：每个门禁必须包含至少一个**故意不达标**的负控；若某期负控意外变绿，门禁本身判定失效，本次 `NO-GO`。理由：45 次检查若每次同一脚本跑绿，评审者第三次之后就不再阅读。

### 9.9 验收标准总表（AC-R），与 SL/指标一一对应

| ID | 验收 | 对应 | 首次达标 |
|---|---|---|---|
| AC-R1 | 断线重连零丢失零重复（含接管、轮换、401 重注册） | SL1/SL9/SL10 + RES-2/4/6 | 2.5.9 |
| AC-R2 | 同一 `(conversationId)` seq 连续无空洞，空洞即事故 | SEQ-3 | 2.5.4 |
| AC-R3 | 跨会话/跨主体不可越权读（含同名 agent、broadcast） | SL2/SL13 | 2.5.8 |
| AC-R4 | 幂等重试返回首次结果（含 `messageId/seq`） | IDM-2 | 2.5.7 |
| AC-R5 | 慢消费者内存有界且 D 通道转 resync | CP-1/背压 | 2.5.10 |
| AC-R6 | 部署 drain 零丢失、重连错峰（≤5s） | SES-1/O-5 | 2.5.9 |
| AC-R7 | 丢光 P/C 后状态可重建 | EV-2/AC-P2/AC-I2 | 2.6.3 |
| AC-R8 | 目标有主/有界/可中止/可判定 | G-1..G-3 | 2.6.0 |
| AC-R9 | 任务租约唯一、冲突显式、依赖无环 | T-1/T-2/T-6 | 2.6.2 |
| AC-R10 | 轨迹边界可校验（`seqRange/goalRef/gapSeqs`）+ 多 Agent 角色正确 | TR-3/TR-5 | 2.7.3 |
| AC-R11 | 归属不可越权写入（SI-1），资产事件只在 D | SI-1/2.7.2 | 2.7.2 |
| AC-R12 | 信用只由签名证据产生、可重算、不可自证 | E-3/IV-7 | 2.8.1 |
| AC-R13 | 干预双写、无 ruleId 不执行、到期自动解除 | IV-1/IV-3/IV-4 | 2.6.0（框架）/2.8.5 |
| AC-R14 | 计量幂等、结算差异 0、超预算中止 | 2.9.2/2.9.5/2.9.8 | 2.9.8 |
| AC-R15 | 所有降级显式（`reason+skipped[]`），静默降级 0 | SI-4 | 2.5.x 起持续 |
| AC-R16 | 公开容量数字必有测量出处 | SL16/B1 | 持续 |

---

## 10. P0 风险（按"会不会让整件事从根上失效"排序）

| # | 风险 | 现状证据 | 后果 | 检出 | 处置（版本） |
|---|---|---|---|---|---|
| **R1** | 排序域错误（全局 id 当 seq） | `bus.go:236-247` | 重放、轨迹切分、因果链全部错；`seq_gap` 永远不可判 | 同会话取两条消息比 seq | 2.5.4（不修则 W0 之后的全部验收无意义） |
| **R2** | 可见性绑临时 id（G11） | `bus.go:284-298` + `main.go:238-242` | 换会话即丢历史；"重连不丢消息"不成立 | 重注册后查旧消息 | 2.5.8 |
| **R3** | 寻址键即授权键（N1） | `bus.go:95-138,291-296` | 同名 agent 跨主体互见互发；无需任何漏洞即可越权 | 两个默认 agent 名注册后互发 | 2.5.8（可与 R2 同版，但**必须独立负控**） |
| **R4** | 快照/订阅非原子 + 无 `id:` | `api/harness.go:242-267` | 静默丢消息且客户端无法自愈（最坏的一种丢） | 竞争注入 1000 次 | 2.5.5 |
| **R5** | 静默截断（无 `hasMore`） | `api/harness.go:206-215` | 客户端少读而不自知；轨迹缺段 | 200+ 条积压读取 | 2.5.9 |
| **R6** | 无去重 | `bus.go:216-255` | 重试=重复；游标无法推进 → 反造空洞 | 同键双发 | 2.5.7 |
| **R7** | 主体未接线（N0/ID-1） | `api/harness.go:24-39` vs `auth/identity.go:74-79` | 团队/归属/信用全部记到 `unbound`；账本不可用 | JWT 会话查 `principalRef` | 2.5.6 |
| **R8** | 全局 200 会话硬编码 | `bus.go:24` | 一个热团队打死整个进程（可用性单点） | 单团队压到 200 | 2.5.11（配额化） |
| **R9** | 轨迹无 peer-agent 角色 / 无边界字段 | `trajectory-record.schema.json:7,157` | 多 Agent 轨迹归因错误 → 归属与信用同时失效 | 构造 A↔B 轨迹看角色 | 2.7.3 |
| **R10** | P/C 承载正文的旁路 | schema 已堵（SL6），但 `meta` 是自由对象（`bus.go:52`） | 一旦有人把正文塞进 `meta`，出口护栏失效 | 哨兵串注入 `meta` | 2.5.10（`meta` 禁正文 + 扫描） |
| **R11** | 干预无双写 | 无实现 | 干预不可审计 = 最危险的权力 | 丢 C 后查 D | 2.6.0（框架）/2.8.5（接线） |
| **R12** | 轨迹与 D 不同源 | `TRAJECTORY-RL-BOUNDARY.md:93` vs 契约 | 训练集污染、归因污染 | `contentHash` 对账 | 2.7.3 |
| **R13** | 版本算术不自洽（X1） | 本文 §9.1 | 门禁节奏无法核验，"50 版"变成口号 | 数版本号 | **D-1 裁定（发布 2.5.4 前）** |
| **R14** | 门禁仪式化 | — | 45 次检查退化为盖章 | 负控是否红 | §9.8 GATE-1 |

**与灵魂总架构 P0 的交叉**：其 P0-1（提案/暂存同权）由 2.7.2 的四权检查器覆盖；P0-4（主体不可鉴权）被本文 R7 修正为"接线问题"；P0-10（可回滚是声明）由 §9.8 的回滚点列覆盖。**两份文档的 P0 不得各说一套**，以本文 R 编号 + 总架构 P0 编号双向引用。

---

## 11. 明确拒绝与待决

### 11.1 拒绝（评审闸门，不是建议）

| # | 拒绝 | 理由 | 替代 |
|---|---|---|---|
| A1 | 引入 CRDT/OT（无 §4.7 三条件之一） | 收敛对象是日志，不是共享文档；引入即新增内存与墓碑治理 | 日志 + CAS + 投影（§4.5） |
| A2 | 新 MCP 方法 / 新 A2A 扩展 | 违反钉版；drain 用 HTTP 503 + 工具结果字段已足 | 现有语义 + IM2A 自有工具字段 |
| A3 | P/C 承载自由文本 | 绕过出口护栏与审计（SL6/SL8） | 枚举 + 数值 + 引用 |
| A4 | 用会话 id 当恢复游标 | 部署即失效（ADR 0006 D3） | 四元组 `(principal, agent, conversation, seq)` |
| A5 | 用 presence 计数做信用/计费 | 在场 ≠ 结果；可被刷 | D 事件 + 签名证据 |
| A6 | 自动扩权/自动晋级 | 自动化只能收敛（§6.6） | 人 + 门禁 |
| A7 | 轨迹归 Agent 所有 | 会被当个人资本交易 | 平台/主体所有，Agent 受限使用 |
| A8 | 用 feature flag 承载安全边界 | 可运行时关闭的边界不是边界 | 版本 + 资产回滚 |
| A9 | 跨组织实时会话 | `crossOrg:false` 冻结；Phase3 前不谈 | A2A 跨组织只在 Phase3 |
| A10 | 客户端时钟参与定序 | 时钟偏移会改变收敛结果 | 服务端 seq（SEQ-1） |
| A11 | 把声誉分数写进提示词 | 诱发迎合与自我美化 | 只给能力边界摘要 |
| A12 | 无测量就写并发数字 | 规则 B1 / SL16 | 公式 + 口径 + 实测 |
| A13 | 用 `eventbus` 当 D 日志/backplane 真相 | best-effort 异步写（N7） | D 日志 + 游标重放 |
| A14 | 永久干预/永久放逐 | 会让行为退化为随机试探 | 有期限 + 自动解除 + 申诉 |

### 11.2 待决（不裁定就不发下一版）

| # | 待决 | 选项 | 建议 | 阻塞 |
|---|---|---|---|---|
| **D-1** | 版本算术（X1） | ①54 交付点 = 50 能力版 + 4 收口；②严格 50（2.5.10–2.5.13 不打 tag） | ①（可审计） | 2.5.4 发布 |
| **D-2** | 共享 MCP 桥如何代表多用户（ID-4） | ①JWT 透传；②网关签发 `act-as` 委托；③每用户一个会话/令牌 | ①+③（②仅跨组织） | 2.5.6 |
| **D-3** | EVO-5 闭环的窗口归属（X4） | ①W3/W4 分摊（本文）；②单开一窗（需重排 50） | ① | W3 规划 |
| **D-4** | `retentionSeqs` / `maxBatch` / `ratePerPrincipalPerMin` 的签字 | 10000 / 500 / 240（建议值） | 需 owner 签 | 2.5.11 |
| **D-5** | `loopbackExempt`：`loopback-only` 还是 `forbidden` | ADR 0006 D13 建议 `loopback-only` | `loopback-only` + 显式豁免清单 | 2.5.13（profile 冻结） |
| **D-6** | 主体断言的签发方 | ①JWT（现有 `auth`）；②`ident-code` 服务 | ①短期、②长期 | 2.5.6 |
| **D-7** | 审计签名身份与密钥托管 | 平台单钥 / 每宇宙一钥 / HSM | 每宇宙一钥 + 轮换 | 2.6.8 |
| **D-8** | 团队/成员表归属（Go 侧还是 Node 侧） | ①Go `internal/`；②Node gateway | ①（实时路径就近判权） | 2.5.8 |
| **D-9** | 多节点 backplane 选型 | ①Redis Pub/Sub；②`stream.Hub` 跨节点；③不用（单写者 + D 补） | ③先行，②备用 | 2.9.x |
| **D-10** | 微信平台能力实测（`STREAMING/UNREAD_SHAPING`） | 实测 or 声明 `CAPABILITY_MISSING` | 先声明缺失 | RT-22 |

---

## 12. 一页速查

```text
判型       D 定序持久审计计未读可载正文 / P TTL 合并不审计 / C 封闭枚举至多一次 / O 只读派生
身份       uni_ > team_ > agt_/prn_ > del_ > hs_（临时） ; 定序域 cnv_
不变量     EV-2 丢光 P/C 仍可重建；SEQ-1 客户端时钟不定序；ID-2 会话不当键；SL13 寻址≠授权
恢复       resume(principal,agent,conversation,seq) → REPLAY / RESYNC_REQUIRED / CURSOR_INVALID
幂等       principal:clientMsgId，TTL 24h，命中返回首次结果（含 seq）
presence   30s 心跳 / 90s 超时 / 60s TTL / 30s 新鲜度 / ≤10Hz 合并 / coalesce-last / 无自由文本
目标       有主·有界·可中止·可判定（G-1）；租约=fencing；进度=D 里程碑，P 只表"在忙"
任务       日志+CAS+投影；LEASE_HELD_BY_OTHER / TASK_VERSION_CONFLICT / EVIDENCE_REQUIRED
轨迹       边界=(goalRef, conversationRef, seqRange, gapSeqs)；P/C/O 不进；角色含 peer_agent
审计       hash chain + Ed25519 + checkpoint；治理动作 D 双写；不存正文
干预       封闭枚举 + ruleId/阈值/证据 + expiresAt；D 是事实、C 只加速；自动只收敛
观测       seq_gap=0、digest_mismatch=0、credit_ledger_mismatch=0 为事故线；标签白名单
容量       公式先于数字；MaxSessions 全局 200 必须配额化；慢消费者必转 resync
版本       W0 预研(2.5.4-13,N1) → W1 目标(2.6,N2) → W2 归属(2.7,N3) → W3 信用(2.8,N4)
           → W4 节点经济(2.9,N5) → W5 宪制(3.0.0-3.0.3, N6 终认证)
一句底线   能被归因的才叫自主，能被重放的才叫不丢，能被对账的才叫信用
```

---

## 附录 A · 本次评审的改动清单

| 类型 | 项 | 位置 |
|---|---|---|
| 修正 | M1–M6（行号/文件名/量级/版本号/主体可得性） | §0.5.2、§1 |
| 重排 | X1 版本算术 → 50 能力版 + 4 收口版；X2 主体绑定提前到 2.5.6；X3 soul 契约拆分；X4 EVO-5 分摊 | §9.1、§2、§11.2 |
| 新增 | 六层身份（+`conversationId` 定序域）、SL13–SL16、ID-4/ID-5、RES-5/6、§3.9 三要素映射 | §3、§2 |
| 补齐 | §4 presence/目标/任务/租约协议（原缺）、§5 轨迹与审计（原缺）、§6 评估与干预（原缺）、§7 指标全集（原缺）、§8 容量与故障（原缺）、§9 54 行版本表（原缺）、§10 P0（原缺） | 全文 |
| 发现 | N0–N9（主体可得、agent 名寻址、锁热点、慢消费者不可辨、静默截断、401 令牌、容量语义不一、eventbus 非日志、轨迹角色缺口、轨迹边界未字段化） | §0.5.4、§1 |

## 附录 B · 相关文件

```text
docs/design/IM2A-v2.5.4-v3.0.3-REALTIME-COLLAB-FOUNDATION.md   本文（实时协作底座）
docs/design/IM2A-v2.5.4-to-v3.0.3-SOUL-AND-EVOLUTION-ARCH.md   灵魂与自进化总架构（三要素来源）
docs/design/IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md             v2.5.0 会话语义（本文上游）
docs/design/IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md              轨迹与 RL 边界（§5 上游）
docs/adr/0006-realtime-session-semantics.md                    D/P/C、恢复、接管、背压（Accepted）
docs/security/SSE-SESSION-ISOLATION.md                         v2.5.3 会话隔离与残余风险
docs/design/TENCENT-V2.5.3-DEPLOYMENT.md                       dev 部署现状与"不声明"清单
docs/contracts/trajectory-record.schema.json                   §5.4 的 1.1 增量对象
packages/protocol/schemas/{realtime-message,realtime-session-profile,session-ref}.schema.json
tests/TEST-MATRIX-v2.5.0-REALTIME.md                           RT-00..RT-24（§9 的验收来源）
agent-im/contracts/INTERFACES.md                               §10/§14/§15/§16/§25/§29/§30/§48
im2agent/internal/{harness/bus.go, api/harness.go}              实时总线与 SSE（G1/G11/N1/N2）
im2agent/cmd/im2a-mcp/main.go                                  MCP 桥（E9/E10/N5）
im2agent/internal/{auth,msgseq,store,audit,metrics,eventbus,stream}  可复用原语（E11–E13/E20）
im2agent/internal/kernel/routes/routes_harness.go               harness 路由与认证装配（N0）
```

