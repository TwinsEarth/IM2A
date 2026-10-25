# Changelog

本项目所有重要变更记录于此。格式参考 [Keep a Changelog](https://keepachangelog.com/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

五方发布口径：**GitHub Release（多平台二进制 + 校验和）· npm（`@twinsearth/im2agent`）· CI（build/vet/race 测试/服务自测）· Compat（契约 + 跨平台兼容矩阵）· Release workflow（打 tag 全自动）**。

---

## [v2.3.25] - 2026-10-25 (任务真执行：Runner 注入真实 MCP Skill 执行器，消除"假已办")

### 新增
- **Runner 真正执行工具**：PermissionGate 放行后，Runner 调 `ToolExecutor.Execute(ctx, sub, step, trust)`
  真正落地动作，回复改为 Skill 的真实返回（而非固定 ReplyHint）；`RunnerResult` 新增 Executed/ExecResults。
- **skillbridge 适配层**（新包 `internal/skillbridge`）：编排抽象工具名 → 真实 MCP 工具映射：
  calendar.add/list→calendar.query、purchase.search→family-purchase.compare、
  purchase.checkout→family-purchase.order（元→分）、property.report→home-maintenance.request；
  通道动作（im/mail/delete）由出站链路完成；legal.draft/parenting.remind 暂无 Skill 时返回
  `ErrToolNotWired`，Runner 据实回"还没接通"，绝不假装已办。
- `ToolExecutor` 接口签名加入 trustLevel（与门控同一信任档，二次门控不采信模型自报）。
- gateway 装配 `skillruntime.DefaultRegistry()` + `skillbridge.New(rt)` 注入 Runner。

### 测试
- 新增 `skillbridge/bridge_test.go`：真实比价/下单/报修/日程返回真实结果、通道动作 no-op、
  未接通工具 ErrToolNotWired。
- 新增 `orchestration/runner_exec_test.go`：执行器被真实调用且回复用 Skill 输出、
  未接通/执行失败诚实回告、无执行器回退计划提示。

### 验证
- build/vet 零输出；skillbridge/skillruntime/orchestration `-race` 通过；
  全量 `go test ./...` 44 包 ok、0 FAIL。

## [v2.3.24] - 2026-10-25 (agent_registry 接生产：首消息自动开通 + 门控读真实信任档)

### 新增
- **注册表接生产**：`agentregistry.Store.TrustLevel(wxid)` 在用户首条消息时幂等开通
  master + 五个子 Agent（schedule/procurement/legal/parenting/property），全部
  `ON CONFLICT DO NOTHING`——绝不覆盖运营/管理员后续设置的 trust_level、status、配额。
- **门控读真实信任档**：L4Hook 新增 `TrustProvider`，规划前按 wxid 从注册表读 master 的真实
  trust_level 传入 PermissionGate，替换固定的 3；读取失败安全回退默认档，不阻断消息。
- master 被暂停时 TrustLevel 返回 ErrPaused；gateway 装配 `agentregistry.New(deps.DB)`。

### 测试
- 新增 `agentregistry/provision_real_test.go`（真实 PG）：首次开通 6 个 Agent、管理员改 trust
  后重复开通不被覆盖、master 暂停返回 ErrPaused、空 wxid 拒绝，cleanup 零残留。
- 新增 `orchestration/l4trust_test.go`：trust=2 高风险 im.send 被拒、trust=3 放行、
  provider 出错回退默认档。

### 验证
- `go build/vet` 零输出；`go test ./...` 全 43 包 ok、0 FAIL；agentregistry/orchestration `-race` 通过；
  真实 PG 开通测试通过且 probe 零残留。

## [v2.3.23] - 2026-10-25 (L4 三层记忆接 PG/pgvector，重启不丢)

### 新增
- **wxid → 记忆命名空间稳定映射**：新增迁移 `034_l4_subjects`（identity 表 + wxid 唯一索引），
  `memory.SubjectResolver` 把微信 wxid 幂等映射为记忆 user_id，并统一加 `L4SubjectOffset=1_000_000_000`
  高位偏移，与真实 users.id 小整数命名空间彻底隔离，杜绝跨渠道/跨用户串记忆。
- **L4Hook 接入持久三层记忆**：MaybeHandle 现携带 wxid；规划前注入"跨助理协调摘要 + 各子 Agent
  最近办事轨迹 + 管家级高置信偏好"；L4 接管后按子 Agent 落情景记忆（episodes），同一用户下一轮
  及进程重启后可被检索召回。记忆走 `memory.Store` 接口（生产 PGStore，单测 MemoryStore）。
- `CommanderMemory`/`ProceduralMemory` 字段由具体类型 `*memory.MemoryStore` 改为 `memory.Store` 接口，
  从而可注入 PG 持久实现；gateway 在 L4 开启且 DB 可用时装配 PGStore + PGSubjectResolver。
- inqueue `L4Interceptor.MaybeHandle` 增加 wxid 参数，中间件传 `ev.FromWxid`。

### 测试
- 新增 `memory/subject_real_test.go`：真实 PG 落库（幂等同 id、不同 wxid 不同 id、高位偏移、
  重启重解析可读回、空 wxid 报错），cleanup 零残留。
- 新增 `orchestration/l4memory_test.go`：任务落情景记忆、模拟重启后摘要召回、跨用户隔离、
  闲聊透传不写记忆。
- 同步 inqueue/orchestration 既有测试签名。

### 验证
- `go build/vet` 零输出；`go test ./...` 全 43 包 ok、0 FAIL；memory/orchestration/inqueue `-race` 通过；
  真实 PG 身份映射测试通过且 probe 零残留。

## [v2.3.22] - 2026-10-25 (第三轮全量审查：修复审批 MemoryStore.ListPending 恒空)

### 修复
- **approval `MemoryStore.ListPending` 永远返回空**：`Save` 只初始化 `byUser[userID]` 的 key、
  从不追加 ticket id，导致按用户查待审恒为空（此前只能用 `All()` 绕过）。现 upsert 时维护
  byUser 索引（去重），ListPending 正确返回非终态票据。
- 新增 `listpending_test.go`：锁死"按用户列出待审、终态排除、用户隔离"真实行为
  （旧测试只断言驳回后为 0，无法暴露该缺陷）。

### 验证
- `go build/vet` 零输出；`go test ./...` 全量 ok、0 FAIL；approval `-race` 通过。

---

## [v2.3.21] - 2026-10-25 (L4 深度审查修复：闲聊不再被吞 + 澄清 + 门控收紧 + 033 真实落库)

### 修复（第二轮逐文件精读发现的真实缺陷）
- **L4 开启后会吞掉纯闲聊（最重要）**：原 `L4Hook.MaybeHandle` 对任何消息都返回 handled=true，
  导致 256-token 的 flash 路由器（非对话模型）用一句“收到。”/短句接管，旧链路的完整 chat 模型永不执行，
  灰度一开 Bot 反而更笨。现新增 `RunnerResult.Passthrough`：Runner 只接管“办事 task”与“澄清”，
  纯闲聊 / relay / nego_reply / system / 低置信安全降级（SafeChatPlan）一律透传回旧链路，由完整 chat 模型作答；
  activation=ignore 静默不回。
- **NeedClarify 未处理**：无 subtasks 分支原本不回 ClarifyQuestion，现在信息不全时直接回追问句。
- **门控金额漏洞**：`purchase.checkout` 若 args 缺 amount 字段，旧逻辑跳过预算检查、trust≥3 即自动放行；
  现缺金额→必须上抛审批（无法核对预算），金额≤0→拒绝。
- **Commander systemPrompt 字段不符**：原 prompt 引用 Plan 顶层不存在的 `requires_confirmation`（实际在 ToolStep），
  改为 schema 自洽的路由指令，并明确闲聊时 reply_hint 留空、交对话模型。
- 蒸馏统计只计“办事”裁决，透传闲聊不再虚增 approved/pass-rate。

### 测试加固
- 重写 `l4hook_test.go`：任务被接管、纯闲聊透传、8 档开关、nil safe。
- 重写 `runner_e2e_test.go`：闲聊透传 / 澄清回问 / relay·nego·system 透传 / ignore 静默 /
  allowed·deny·over-budget / checkout 缺金额需审批+负数拒绝 / 统计只计办事 / nil safe。
- 033 `agent_registry` 真实 PG 落库断言实跑通过（Ensure 幂等、FOR UPDATE 配额原子+1、
  配额超限、paused 拦截、未知 agent 404），零残留。

### 验证
- 全仓 `go build ./...` / `go vet ./...` 零输出；`go test ./...` 43 包 ok、0 FAIL；
  orchestration/inqueue/agentregistry 三包 `-race` 通过。

---


## [v2.3.20] - 2026-10-25 (全量审查加固：真实落库测试 + 性能基准)

### 新增（审查加固测试）
- `internal/inqueue/inqueue_real_test.go`：真实 PG 落库断言（INQUEUE_REAL_PG 门控，CI 自动 skip）。
  - Enqueue 幂等（重复 idem_key 返回 duplicate=true，不重复入队）；
  - Worker FOR UPDATE SKIP LOCKED 抢占 queued → 处理 → status=done 全链路；
  - 零残留 cleanup（前后各清一次 audit_v2319_% 数据）。生产实测通过，库内残留 0。
- `internal/orchestration/bench_audit_test.go`：L4 本地决策性能基准（无网络）：
  - Runner 全链路（stub planner）5.9 µs/op、1466 B/op、15 allocs；
  - Gate 闸门 52 ns/op、零分配；ExitGuard 护栏 0.96 µs/op、零分配。
  - 结论：编排层本地开销相对模型秒级延迟可忽略，瓶颈仍在模型与出站。

### 审查结论（v2.3.0–v2.3.19）
- 全仓 go build / go vet 零输出；43 个测试包全 ok、0 FAIL；L4 四包 -race 全过。
- 迁移 032/033 真实落库，inbound_events/agent_registry 结构与唯一索引核对通过。
- 修复 L4_ENABLED 判定不一致（v2.3.19）；L4 已在 worker 真接线（默认 off）。

---

## [v2.3.19] - 2026-10-25 (L4 生产真接线 + 审查修复)

### 新增
- `services/gateway/main.go` inqueue worker 真正装配 L4：
  - `NewL4Hook(NewRunner(New(NewFlashPlanner(model)), DefaultGate, NewExitGuard, NewDistillStats))`；
  - 自聊消息经 `inqueue.WithL4` 前置拦截：L4 接管则用 openclaw Sender 直接把护栏文案发回微信，旧 Relay 不再跑；
  - 协商会话（NegoSessionID 非空）始终走旧链路，不被 L4 接管；
  - L4_ENABLED 默认 off，启动日志明示 enabled/off。
- `/v1/ops/orchestration` 状态更新为 D-wired-v2.3.19。

### 修复（全量审查 v2.3.0–v2.3.18 发现）
- **L4_ENABLED 判定不一致**：l4hook 用 `strconv.ParseBool`（不接受 "on"），main.go 用 `=="on"`，设 `L4_ENABLED=on` 时 hook 永远不激活。改为统一 `orchestration.EnvBool`（on/1/true/yes 大小写不敏感），main.go 状态上报也复用同一函数。
- 新增 `TestL4Hook_OnAliasAccepted` 覆盖 on/ON/true/yes 与 off/0/no 八档。

### 验证
- 全仓 `go build ./...`、`go vet ./...` 零输出；43 个测试包全 ok，0 FAIL；L4 四包 -race 全过。
- 迁移 032/033 真实落库（inbound_events/agent_registry 结构与唯一索引核对通过）。

---

## [v2.3.18] - 2026-10-25 (inqueue L4 中间件)

### 新增
- `internal/inqueue/l4_middleware.go`：
  - 定义最小切面 `L4Interceptor`（`MaybeHandle`）和 `ReplyFunc`，inqueue 不直接 import orchestration，避免环依赖；
  - `WithL4(h, interceptor, reply)` 把旧 Handler 包一层：hook 接管 → 直接调 ReplyFunc 发回微信，旧 handler 不跑；未接管 → 透传；
  - interceptor 为 nil 时零开销返回原 handler；Reply 出错向上抛，让 worker 标记 error。
- 4 个单测：nil 透传、handled 短路、未 handled 透传、reply 错误传播。

### 价值
- 生产接线的最后一块拼图就绪：relay 里把旧 handler 和 `orchestration.NewL4Hook(NewRunner(...))` 以及 openclaw 出站函数传进 `WithL4`，systemd 打开 `L4_ENABLED=1` 即灰度生效。

---

## [v2.3.17] - 2026-10-25 (L4 灰度接线钩子)

### 新增
- `internal/orchestration/l4hook.go`：
  - `L4Hook` 从环境变量 `L4_ENABLED` 读开关；默认 off（nil），对生产零影响；
  - `NewL4Hook(runner)` 关时返回 nil，on 时返回钩子；
  - `MaybeHandle(ctx, text, summary)` 接管即返回已过护栏的微信文案，handled=true 时旧链路不再跑；
  - nil safe。
- 3 个单测：默认 off、on 接管、nil safe。

### 价值
- Runner 已就绪但没接生产。这版把"开关"做成零风险灰度：systemd drop-in 追加 `Environment=L4_ENABLED=1` 即可打开，回滚就删掉。下版再把 inqueue worker 真接上。

---

## [v2.3.16] - 2026-10-25 (D4 端到端编排 Runner)

### 新增
- `internal/orchestration/runner.go`：
  - `Runner` 把 Commander / PermissionGate / ExitGuard / DistillStats 串成一根管子；
  - `Run(ctx, userText, recentSummary, trustLevel)` 返回 `RunnerResult{Reply, Allowed, NeedApprove, Reason, Plan}`；
  - 纯闲聊直接过护栏发回复；工具调用逐 Subtask 走 PreToolUse 门控；任意 Deny 友好拒绝；任意 NeedsApproval 上抛审批；
  - 每次裁决写 DistillStats（approved/rejected/pending）；nil safe。
- 6 个端到端 smoke 测试：闲聊直通、低风险工具放行、高风险低信任拒绝、超预算上抛审批、统计累加、nil safe。

### 价值
- L4 Phase A/B/C/D 所有零件第一次在一个进程内被串起来跑通：消息→Plan→门控→护栏→统计。后续只要把 FlashPlanner（已就绪）和 inqueue worker 接进来，就是生产路径。

---

## [v2.3.15] - 2026-10-25 (D3 蒸馏效果监控)

### 新增
- `internal/orchestration/distill_stats.go`：
  - `DistillStats` 内存计数器，goroutine 安全；
  - `Record(state)` 按审批终态计数（approved/rejected/modified/failed/pending）；
  - `Snapshot()` 返回通过率 / 驳回率 / 修改率 + 健康判定（样本≥50、驳回率<40%、通过率>40%）；
  - `JSON()` 一行快照，可直接挂到 `/v1/ops/distill`；
  - nil safe。
- 6 个单测：空快照、计数与速率、高驳回判不健康、JSON 回环、并发安全、nil safe。

### 价值
- 蒸馏闭环跑起来后，能一眼看出"Agent 最近变聪明没"：驳回率飙高立刻报警（提案质量下降），通过率过低说明阈值被收紧过了头。

---

## [v2.3.14] - 2026-10-25 (D2 程序记忆检索与 playbook)

### 新增
- `internal/orchestration/procedural_memory.go`：
  - `ProceduralMemory.RecordDecision(ctx, sc, trigger, suggestion, decision, note)`：把审批终态按方向写回程序记忆（approve +0.1 / modify −0.1 / reject −0.3）；
  - `Suggestion(ctx, sc, trigger)`：子 Agent 启动时先查相似审批情境，返回"上次：用户批准（把握 60%）"式提示；
  - `ShouldAutoExecute(ctx, sc, trigger)`：置信度 ≥0.6 且历史为 approve → 直接执行不再上抛审批；被驳回过的 trigger 永远不会自动执行；
  - 严格按 Scope 隔离，空 trigger / nil store 安全。
- 5 个单测：记录并检索、reject 降置信、自动执行阈值、跨 Scope 不串、空输入安全。

### 价值
- 子 Agent 不再"每次都从零推理"；用户批准过的小事（买牛奶、订周三会议室）下次直接办；用户驳回过的（鲜花、贵的东西）自动收紧，不再烦用户。

---

## [v2.3.13] - 2026-10-25 (C2 三层记忆增强：Commander 协调视图)

### 新增
- `internal/orchestration/commander_memory.go`：
  - `CommanderMemory` 总 Agent 专用记忆视图，命名空间 `AgentID="__commander__"`；
  - `SetCoordination/GetCoordination`：跨子 Agent 协调摘要写在 commander 命名空间，子 Agent 读不到；≤1000 字硬截断；
  - `UserDigest(userID, agentIDs)`：跨子 Agent 拉最近活动（每 Agent 最多 2 条 episode）拼成 ≤600 字摘要，供 Commander 路由用，不直接拉子 Agent 原始 facts/episodes；自动跳过 commander 自身。
- 6 个单测：协调摘要读写回环、不泄露给子 Agent、跨 Agent digest 聚合、跳过 commander 自身、超长截断、nil safe。

### 价值
- 落实 L4 v2 §2.7 硬约束："总 Agent 只持有跨子 Agent 的协调摘要，不直接读取子 Agent 原始记忆向量"——记忆隔离从设计落到代码。

---

## [v2.3.12] - 2026-10-25 (D1 审批反馈 → Improvement Memory 桥)

### 新增
- `internal/orchestration/feedback_bridge.go`：
  - `ApprovalFeedbackBridge.OnTicketResolved(ticket)` 把审批终态翻译成 distill.Record 调用；
  - APPROVED=+0.1（强化）、REJECTED=-0.3（明显反对，倾向收紧阈值）、MODIFIED=-0.1（弱反对）；
  - FAILED/EXPIRED 不打分（不是用户主观决策）；
  - nil safe，空 agent/type 用 unknown/general 兜底；编译期断言 *distill.Engine 满足接口。
- 6 个单测：approve 强化、reject 负分、modify 弱负、failed 不打分、nil safe、空字段兜底。

### 价值
- 用户在微信里点"驳回/修改"，distill 自动收紧同类建议的审批阈值，Agent 越用越顺手——这就是蒸馏闭环的入口。

---

## [v2.3.11] - 2026-10-25 (C4 出口护栏)

### 新增
- `internal/orchestration/guardrail.go`：
  - `ExitGuard.AuditMessage(text)` 外发前粗过滤：黑名单（wxid_/gh_/http(s):///internal:///ticket_/msg_id_/trace_）、空文案兜底、超长截断（≤500 字）；
  - 命中后不直接拒绝，统一改写成一句安全兜底文案，`HitRule` 留审计；
  - `SetForbidden(words)` 运行期可扩展黑名单。
- 6 个单测：干净文案通过、空文案兜底、wxid 拦截、URL 拦截、超长截断、黑名单可扩展。

### 价值
- Agent 永远不可能把 wxid、内部 URL、ticket 单号直接甩到微信用户面前；超长回复自动折叠，不会刷屏。

---

## [v2.3.10] - 2026-10-25 (C3 子 Agent 记忆检索与注入)

### 新增
- `internal/orchestration/memory_inject.go`：
  - `BuildSubagentContext(ctx, store, sc, userText)` 把子 Agent 启动时要用的记忆拼成 ≤600 字系统补充；
  - 关键词命中 facts → 列出偏好/常办事；未命中则兜底列最多 3 条高置信事实；
  - 再拉最近 5 条 episodes；按 Scope（user+agent）严格隔离，不串其他子 Agent；
  - 低置信 (<0.5) 事实不注入，nil store 安全返回空。
- 5 个单测：fact+episode 注入、跨 Scope 不泄露、空记忆返回空、nil store 安全、低置信过滤。

### 价值
- 子 Agent 启动时不再"失忆"：说过"咖啡只喝美式"，下次采购子 Agent 自动知道；其他子 Agent 的记忆不串扰。

---

## [v2.3.10] - 2026-10-25 (C2 工作记忆层)

### 新增
- `internal/memory/working.go`：三层记忆里补"工作记忆"层（语义 Fact / 情景 Episode 已有）。
  - `WorkingMemory` 进程内、按 (user, agent) 隔离、默认 10 分钟 TTL；
  - `Attach(user, agent, goal)` 开新任务；`Update(step, pending)` 推进并续命；
  - `Snapshot(user, agent)` 直接给 prompt 一段"当前任务：X；下一步：Y；待办：Z"；
  - `Clear` 任务完成主动清；`GC` 批量清过期。
- 6 个单测：Attach/Get、Update 续命、子 Agent 隔离、过期自动失效、Clear、空 snapshot。

### 价值
- 同一个用户在"采购"和"日程"两个子 Agent 之间不会串任务；任务结束 10 分钟自动忘，下一条消息不会被上一个任务的上下文污染。

---

## [v2.3.9] - 2026-10-25 (C1 Skill 返回结果微信渠道格式化)

### 新增
- `internal/skillruntime/wechatfmt.go`：
  - `FormatForWechat(CallResult)`：把 Skill 结构化结果翻译成 ≤200 字、人类可读、无内部 ID 的短文案；
  - DENIED/REQUIRES_APPROVAL 分别翻译成"需要确认"和"等你回复 1/2/3"；
  - 内部字段（wxid/ticket_id/message_id/task_id/url/…）一律剥离，不泄露给微信；
  - `FormatErrorForWechat(err)`：把 timeout/permission/quota/未知错误翻译成用户能懂的话。
- 6 个单测：文本透传、DENIED 友好、审批提示、内部 ID 剥离、200 字截断、错误分类。

### 价值
- Skill 真办完事后，微信里看到的是"已下单：咖啡机，金额：1200"，而不是一坨 JSON 或 wxid 串；不再出现机器味。

---

## [v2.3.8] - 2026-10-25 (B4 超时升级 worker + 执行幂等)

### 新增
- `internal/approval/worker.go`：
  - `EscalationSink` 接口解耦通知出口（生产接微信/手表/短信）；
  - `Worker` 周期 ticker 调 `CheckTimeouts(now)`，把 30min/2h 升级事件投递到 sink；
  - `Tick(ctx, now)` 可单测直接调，不必等真实 ticker；
  - `CompleteIdempotent` / `FailIdempotent`：网络重试/worker 重复投递时不重复扣费/不报错。
- 5 个单测：30min 升级手表、2h 升级短信（二次 tick）、准时不升级、Complete 重复幂等、已 rejected 后 Fail 吞掉。

### 价值
- 审批超时不再死等；微信里用户没回 30 分钟自动推手表、2 小时升级短信；执行重试不会因状态机限制重复副作用。

---

## [v2.3.7] - 2026-10-25 (B3 微信文本审批卡片)

### 新增
- `internal/approval/wechatcard.go`：`ResolveReply(userID, text, listPending)` 把微信文本解释为审批决策。
  - 纯数字 1/2/3 直接对应 approve/reject/modify；
  - 关键词命中（同意/通过/批准/OK；驳回/拒绝/不要/太贵/不行；修改/改成/换/调整）；
  - 无 pending ticket 或关键词不命中时返回 `ErrNotAnApprovalReply`，正常走聊天路径，不抢占闲聊；
  - 严格按 userID 隔离，跨用户回复不会处理别人的审批单。
- `Decision.Apply(engine, by)` 一行落到 Approve/Reject/Modify。
- 8 个单测：数字同意、关键词驳回、关键词修改、无 pending 时按聊天、闲聊不抢、英文 OK、跨用户不串、长文保守命中。

### 价值
- 微信没有按钮，"1 同意 / 2 驳回 / 3 修改" 这种纯文本卡片闭环跑通；用户在微信里回一条就完成审批，不需要切到任何后台。

---

## [v2.3.6] - 2026-10-25 (B2 审批 action_hash 绑定)

### 新增
- 复用已有 `internal/approval` 事件溯源状态机（PENDING→REVIEWING→APPROVED→EXECUTING→COMPLETED/FAILED 已在 v0.x 落地，含 Escalate/CheckTimeouts）。
- 给 `Ticket` 增加 `ActionHash` 字段；创建时通过 `payload["__action_hash__"]` 注入。
- 新增 `Engine.BeginExecutingWithHash(id, by, hash)`：进入 EXECUTING 前强校验 hash 与创建时一致，不一致返回 `ErrActionHashMismatch`（防审批洗白：拿 A 的批准执行 B）。
- 3 个单测：hash 正确通行、错误 hash 拒绝、老票据（无 hash）向后兼容、pending 直跳 executing 仍被状态机拒绝。

### 价值
- 审批与执行在代码层强绑定；Agent 不能"先批一个便宜的，再执行一个贵的"。

---

## [v2.3.6] - 2026-10-25 (B2 审批工作流状态机)

### 新增
- 迁移 `034_approval_tickets.sql`：表 `approval_tickets`，status 带 CHECK 约束（pending/approved/rejected/modified/executing/completed/failed），action_hash 防审批洗白。
- 新包 `internal/approval`：
  - 状态机转移：pending→approved/rejected/modified；approved→executing；executing→completed/failed；终态不可转移。
  - `BeginExecute` 强制 action_hash 与审批时一致（不一致拒绝，防"拿 A 的批准执行 B"）。
  - `ListPending(olderThan)` 给 B4 超时升级用。
- 真实 PG 单测：pending→approved→executing→completed 全链路直通行；pending→executing 直跳被拒；hash 不匹配被拒；rejected 终态不可反悔。

### 价值
- 审批不再是应用层 if/else，DB CHECK + 事务行锁双保险；Agent 不可能"绕过批准直接执行"。

---

## [v2.3.5] - 2026-10-25 (B1 PermissionGate PreToolUse)

### 新增
- `internal/orchestration/gate.go`：PermissionGate 在 Dispatcher 执行任何 Skill 前同步裁决。
  - deny-by-default：未在 builtinTools 注册的工具一律拒绝。
  - capability 白名单：schedule/procurement/property/legal/parenting/master 各有允许调用的工具集。
  - trust 档位：高风险工具（purchase.checkout / im.send / mail.send / system.delete_record）要求 trust≥3。
  - 预算闸门：purchase.checkout 金额 > 500 元时 Allow=true 但 NeedsApproval=true，需人工审批。
- 7 个单测：低风险放行、未知工具拒绝、跨域越权拒绝、低信任调高风险拒绝、高信任高风险放行、超预算需审批、预算内自动。

### 价值
- Agent 的工具调用不再凭 Prompt 自觉，代码层硬拦截；为后续 Dispatcher 真正执行子任务提供安全闸门。

---

## [v2.3.4] - 2026-10-25 (A3/A4 Commander 接 flash)

### 新增
- `internal/orchestration/flash_planner.go`：`FlashPlanner` 实现 `Planner` 接口，把用户消息+近期摘要拼成单轮 user message，system prompt 复用 commander 管家指令；温度 0、maxTokens 256。
- 5 个单测：合法 JSON 拆出 schedule 子任务、低置信降 chat、坏 JSON 降 chat、provider 错误降 chat、recentSummary 注入、ctx 超时降 chat。

### 价值
- Commander 不再是 stub：生产路径注入 `internal/model.OpenAICompatible(deepseek-flash)` 后即可真实做意图分类；任何失败路径仍安全降级到 chat，不会比 v2.3.0 更糟。

---

## [v2.3.3] - 2026-10-25 (A2 每用户 PG Agent 注册表)

### 新增
- 迁移 `033_agent_registry.sql`：表 `agent_registry`（user_wxid+agent_id 唯一，role master/sub，trust_level 0-5，quota_max/quota_used，status active/paused）。
- 新包 `internal/agentregistry`：Ensure 幂等注册、ListByUser、EnsureAllowed（行锁 FOR UPDATE，未注册/暂停/超配额分别返回 ErrNotFound/ErrPaused/ErrQuotaExceeded，通过则原子 quota+1）、ResetQuota、SetStatus。
- 真实 PG 单测：master+calendar 注册、列表 2 条、calendar quota=2 第三次被拒、master 不受限、未知 agent not found、暂停后被拒，探针行 DELETE 零残留。

### 价值
- 总 Agent/子 Agent 不再凭空调度：每个 wxid 的 master 与子 Agent 有落库身份；VIP 配额在代码层强制，越配额直接拦截。

---

## [v2.3.2] - 2026-10-25 (A5 出站令牌桶)

### 新增
- `internal/openclaw/ratelimit.go`：按 bot 账号串行限速，同账号两次发送至少间隔 3s（≈20 条/分钟上限，留余量）。同步 `Send` 与 outbox worker 共用同一闸门，ctx 取消立即返回。
- `sendOnce` 入口先过 `WaitOutbound`，防微信判定刷屏。
- 4 个真实单测：首发不阻塞、次发等 ~3s、ctx 快速取消、不同账号互不阻塞。

---

## [v2.3.1] - 2026-10-25 (A1 webhook 异步入队)

### 新增
- PG 表 `inbound_events`（032 迁移）：idem_key 唯一、status queued/processing/done/error。
- `internal/inqueue`：Enqueue（幂等 ON CONFLICT DO NOTHING）+ Worker（`FOR UPDATE SKIP LOCKED` 抢占，500ms 轮询，单事件 30s 超时，失败落 error 不丢）。
- webhook 开关 `WEBHOOK_ASYNC=on`：写库后立即返回 `{ok,queued,dup}`，不再同步等 LLM；后台 worker 真正调 relay/NegoReply。默认 off，回退 v2.3.0 同步路径。
- main.go 启动 worker goroutine（开关 on 时）。

### 价值
- openclaw 回调不再被 30s LLM 同步阻塞；P99 ACK 降到 <1s；进程崩溃后 queued 事件重启自动重投，消息不丢。

---

## [v2.3.0] - 2026-10-25 (L4 编排层 Phase A 骨架)

### 新增
- `internal/orchestration` 包：Commander（管家，只决策不执行）+ 结构化 Plan JSON Schema（activation/intent/confidence/subtasks/reply_hint）+ Dispatcher 有界循环（maxSteps/maxSubtasks/deadline 熔断）。
- 安全降级：planner 报错/超时/非法 JSON/低置信(<0.4)一律回退 chat，绝不硬拆任务。
- 10 个真实单测：合法/代码块包裹/非法 JSON/未知 intent/报错/超时/低置信/超子任务熔断/chat 直通/safe plan。
- admin 端点 `GET /v1/ops/orchestration` 返回 `l4Enabled`（env `L4_ENABLED=on` 才启用，默认 off，生产路径完全不变）。

### 说明
- 本版本只落地骨架与开关，不改 webhook 同步路径、不接真实工具；A1 异步队列/A5 令牌桶/B 阶段门控审批将在后续小版本继续。

---

## [v2.2.9] - 2026-10-25 (A8 错误码语义化)

### 改进
- 模型/调用失败不再一律回"反应慢了一下"：`friendlyModelErr` 按原因区分——超时→"再发一次"；429/限流→"稍等几秒再发"；401/key/配置→"服务配置出问题，稍等我修"；其余→"脑子卡了一下"。用户知道下一步该做什么。

---

## [v2.2.8] - 2026-10-25 (outbox 补发成败计数)

### 新增
- outbox 异步补发窗口增加原子计数：补发最终成功 `sentTotal`、6 轮退避耗尽仍失败 `failedTotal`，并以 ERROR 日志记录失败账号/收件人/原文，不再"静默丢消息"。
- `/v1/ops/outbox` 返回新增 `sentTotal` / `failedTotal`，运维可在排队深度之外直接看到补发健康度。

---

## [v2.2.7] - 2026-10-25 (A7 出站排队可观测)

### 新增
- **A7 出站可观测端点**：`internal/openclaw.SnapshotOutbox()` 暴露 outbox 排队深度/容量、当前在途发送数（并发上限 10）、熔断中账号列表（含剩余秒数）。
- admin 鉴权端点 `GET /v1/ops/outbox`（IM2A_ADMIN_TOKEN），运维可直接看到"当前排队多少、几个在发、哪些账号被熔断"，不再是黑盒。

---

## [v2.2.6] - 2026-10-25 (self_chat 超时预算 + 故障自然降级)

### 修复（体验/韧性收尾）
- **D5 模型调用独立超时预算**：self_chat 的 LLM 调用（流式/非流式）从直接复用 webhook 的 30s 上下文，改为独立 15s 子预算，模型挂起时不再让用户干等满整个超时。
- **模型故障自然降级**：LLM 调用失败（超时/网络抖/返回空）不再向上抛 400 让用户静默，而是自然回复「我这边反应慢了一下，麻烦再发一次试试～」并落库留痕，用户始终有回音。

### 核查确认已落地的清单项（无需重复开发）
A3 msgId 5 分钟滑窗幂等、B6 trimPrompt 滑动轮次+截断、B7 占位语并发下发、C1/-14/-2 token 失效识别与账号熔断、C2 指数退避、C3 会话过期熔断暂停、C4 10s 超时+outbox 异步补发、C6 按收件人 bot 账号真实投递、D2 systemd 自拉起、D3 健康检查+掉线/失效「需重扫」通知、D4 出站并发 10 信号量+outbox 1024。

---

## [v2.2.5] - 2026-10-21 (微信Bot「理解用户」修复 · LLM 意图门 · 自然化人设 · 改名识别增强)

### 根因
真机日志定位：微信 Bot 不智能不是模型弱，而是 `Relay()` 里有一个**无条件**的协商自动回复——只要最近存在一张未完成协商单，用户任何自由文本（如「你能干什么」「你叫什么名字」）都会被挂到旧协商单并转发，回复一律「好，跟小红说了」；无收件人时还会自己猜收件人。属于「规则先行、LLM 后置」导致的不理解。

### 新增
- **LLM 意图门 `internal/relay/intent.go`**：`classifyIntent` 用 LLM（temperature=0、MaxTokens=200、12s 超时）输出 JSON `{intent: chat|relay|nego_reply, target, body, confidence}`：闲聊/提问=chat、明确「告诉某人 X」=relay、直接回答协商问题=nego_reply。复用同包 `extractJSONObject`。**安全降级**：非法 JSON / 模型超时 / 低置信一律落回 chat，绝不臆造联系人。
- **改名识别增强 `internal/relay/rename.go`**：新增 `stripRenameGreeting` 剥句首寒暄（你好/您好/嗨/哈喽/hi/hello/喂）与前导标点；`renameAgentRe` 增加 `我的Agent叫/我的助理叫/我的小助理叫/给我的agent起名叫` 等模式。修复「你好，我叫小房」「我的 Agent 叫小佑佑」识别不到。

### 修复
- **协商不再劫持自由文本**：`nego_autoreply.go` 改为仅在 `intent=nego_reply` 时才推进协商并转发，否则交回 self_chat（旧文件备份 `nego_autoreply.go.bak_v227`）。
- **自然化私人助理人设**：`relay.go` 的 self_chat systemPrompt 改为口语化、有温度；被问「你能做什么」自然分条；外发/金钱/承诺先确认；拿不准收件人就反问。外发不附 wxid/协商单号/messageId。

### 测试
- 新增 `intent_test.go`、`rename_parse_test.go`；`go test ./internal/relay/` 全 PASS。

### 真机验证（真实落库 + probe 零残留）
- 真实 DeepSeek 跑 5 条分类：你能干什么→chat、你叫什么名字→chat、两点可以我带3人→nego_reply、告诉小红今晚7点晚饭→relay target=小红、明天10点开会→不臆造收件人。
- 线上 loopback `POST /v1/wechat/webhook` probe（真实 A bot 造 probe 身份、验完 DELETE 复核 0 残留）：闲聊→self_chat 自然能力介绍、协商答复→nego_reply、「你好我叫小房」→user_name=小房、「我的 Agent 叫小佑佑」→agent_name=小佑佑。
- gateway 重新构建部署 active，healthz（带 token）返回 status=ok。

---

## [v2.2.4] - 2026-10-25 (微信实机链路修复 · 双大脑消除 · 自然姓名转发 · 出站韧性)

### 修复（真机 E2E 暴露的真实缺陷）
- **入站 400 根因（数字 msgId 反序列化）**：openclaw 渠道把 `msgId` 当 number 下发，IM2A webhook 结构体原要求 string，导致 `json: cannot unmarshal number into ... msgId of type string`，所有微信入站被丢弃。`webhookRequest.MsgID` 改为自定义 `flexString` 类型，`UnmarshalJSON` 兼容 string/number/null，幂等键统一 `string(MsgID)`。修复后数字 msgId POST 从 400 变 200，真机入站全部落库。
- **双大脑消除（openclaw 不再自调模型）**：openclaw 入站插件 `maybeIM2ARelay` 仅在 `IM2A_RELAY_URL` 存在时才转发，否则回落到本地 deepseek-chat，形成"openclaw 自己回一遍 + IM2A 又回一遍"。新增常驻 systemd 单元 `openclaw-gateway.service`（`Environment=IM2A_RELAY_URL=http://127.0.0.1:8080/v1/wechat/webhook`、`Restart=always`），替换手工 nohup 网关；入站确定性转发 IM2A、不再走自身模型（真机日志 `maybeIM2ARelay replied`，无 `model-fetch`）。同时落实 D2 进程崩溃自拉起。
- **自然姓名转发不再被旧协商吸走**：自由文本「告诉小红今晚7点晚饭」此前因无 `@im.wechat` 原文不匹配 `parsePeer`，被 `autoReplyOpenNego` 挂到 24h 内未完成协商单成 `nego_reply`、且 `peer_delivered=f`（库标未送达但对方实际收到）。新增 `internal/relay/peer_named.go`：意图前缀（告诉/转告/转达/发给/通知/向/跟，可带帮我/替我/一下）+ 已绑定联系人姓名（自定义昵称优先、内置称呼兜底、长名优先、重名优先有协商关系者、排除本人）切出姓名与正文；`Relay` 在命名之后、原始 wxid 解析与旧协商挂接之前插入姓名路由，命中即按"收件人自己的 bot 账号"真实投递并准确标记 `peer_delivered`。
- **真机验证（两真机）**：A（阿明）「告诉小红明天上午10点开会」→ `route=peer_route`、`peerWxid=B`、`peer_delivered=t`、真实 `peerMessageId`，B 手机确认收到；A 只收到一条自然回执「好，我转给小红了。」，无单号/wxid。

### 新增（出站稳定性 / 延迟 / 韧性，A/B/C/D 系列）
- **SSE 流式**：`internal/api/chat_stream.go` 流式首 token 即出，修正 SSE chunk 结构体 tag（`content`/`delta`/`choices`）。
- **错误码语义化与重试**：`internal/errcode/errcode.go` 区分真限流与会话过期（-14/-2）；指数退避（2s→4s→30s）、-14 会话过期熔断暂停、10s 发送超时 + 异步补发、发送信号量容量、outbox 缓冲、连接池/keep-alive 复用。
- **健康检查**：`internal/openclaw/health.go` 账号在线/掉线探测。
- **统一超时预算**：网关/发送全链路 deadline。

### 测试
- 新增 `internal/relay/peer_named_test.go`：意图前缀与姓名/正文切分（含"说下午"不被误吞、冒号/逗号分隔、长名优先、只有称呼无正文不转发）。
- 新增 `internal/openclaw/sender_logic_test.go`：`TestClassifySendError`/`TestBackoffPlan`/`TestCircuitBreaker`/`TestSendSemaphoreCapacity`/`TestOutboxBuffer`/`TestPauseDuration` 6 个纯逻辑单测。
- `make gate` 全 PASS（build / vet / `-race` 全包 / 7 服务自测 / gateway 路由自测），零 FAIL；测试 wxid 用 `probe_*` 验完即删（零残留）。

---

## [v2.2.3] - 2026-10-24 (微信实机链路打通 · 用户命名 · 自然语言 · 广播幂等)

### 新增
- **微信里自助命名/改名**：用户直接对 Bot 说「我叫阿明」给自己起名、「你叫阿福」给专属 Agent 起名/改名（迁移 031：`wechat_identity.user_name/agent_name`）；后续所有转达、协商回执、临时群发言自动换成新昵称，单聊 systemPrompt 注入 Agent 名。
- **协商文案去机器味**：手机侧不再出现协商单号/wxid/messageId/状态尾巴；发起方回执「好，我转给小红了。」、对方收到「阿明：正文」、回复回执「好，跟小红说了。」；单号只在后台 PG/审计。
- **Agent 自动协商真投递**：自动轮次统一用「收件人自己的 openclaw 账号」发对方自身 ilink id（修正用发言方账号导致的假 sent/对方收不到），轮次文案改用对方昵称。
- **可读协商单号防撞**：单号改为 `nego_<用户>_<Agent>_<时间>_随机段_3位CRC`，同秒连开两张不再撞主键。

### 测试
- `TestParseRename`（15 条：命名命中/问句/日常话不误判）+ `TestRenameReal`（真实 PG：起名/改名/缓存失效/peer 文案/零残留）。
- 旧测试按「收件人账号投递」新逻辑校正（peer push、双 Agent 自动协商 wantSends），`go test ./internal/relay` 全 PASS。

### 修复
- 修复弱网/移动网络下运营后台一键群发弹"创建失败：Failed to fetch"：实排确认服务端实际已落库并群发（nginx 已写回 202，但响应在移动网络链路上丢失），浏览器误报失败、诱导重试会**重复群发**。
- 服务端创建幂等：`wechat_broadcasts` 增加 `idempotency_key` 列与唯一部分索引（迁移 029）；`Create` 同键 10 分钟窗口内直接返回原任务，INSERT 用 `ON CONFLICT DO NOTHING` + RowsAffected 兜底并发竞态，重试绝不重复创建、重复群发。
- API 支持 body `idempotencyKey` 与请求头 `X-Idempotency-Key`；`Get/List` 回传幂等键。
- 前端失败自愈：每次群发生成幂等键，文案变化或上一轮终态才换键；POST 即使 Failed to fetch 也按幂等键回查任务并进入轮询，只有确实查不到才提示失败。
- nginx 加固：显式 HTTP/1.1、清空 hop-by-hop Connection 头、补全代理超时，降低移动网络连接复用导致的响应丢失。

### 测试
- 新增 `TestBroadcastIdempotency`：同键重放返回同一任务、异键才新建、发送器调用次数 = 两条任务目标数（重试零多发），真实 PG 落库断言，fakeSender 不触达微信，t.Cleanup 零残留（广播/绑定/组）。

---

## [v2.2.2] - 2026-10-24 (微信全量广播 · 运营后台一键群发)

### 新增
- 运营后台一键群发：`POST /v1/admin/broadcast`（admin token 鉴权）+ 管理页 `/admin/broadcast`（目标预览 / 文案 / 实时进度 / 逐条明细 / 历史）。
- 官方 Bot 账号向所有在线绑定微信逐条投递，自动排除官方发送号自身；worker 池（默认 4）+ 全局节流（默认 1.2s/条）规避微信风控。
- 落库 `wechat_broadcasts`（任务）与 `wechat_broadcast_deliveries`（逐条明细，UNIQUE(broadcast_id,to_wxid) 幂等），迁移 028。
- 状态机 pending→running→completed/partial/failed；只处理 pending 明细，重入安全，已发/已失败不重发。
- 支持 `BROADCAST_ACCOUNT` 显式指定官方发送号，缺省取 admin 组绑定、再兜底最小 accountId。
- 查询 `GET /v1/admin/broadcast?preview=1|id=|list`：目标预览 / 单任务明细 / 历史列表。

### 测试
- internal/broadcast 真实 PG 测试：排除发送方自身、一条失败→partial、messageId/error 回写、重入幂等；fakeSender 不触达微信，全部 t.Cleanup 零残留。

---

## [v2.2.1] - 2026-10-24 (微信 Bot 智能链路 · 实机打通)

### 微信 → openclaw → IM2A gateway 中枢（6 项工程任务全部真机验证）
- **#104 入站**：标准 REST 端点 `POST /v1/wechat/webhook`（?key= / X-Webhook-Key 鉴权，幂等 msgId），与 MCP 工具 `im2a_route` 共用同一 relay.Service；openclaw 无原生入站出站 webhook，日常入站由 AGENTS.md 强制的 MCP 薄转发承载（已真机验证）。
- **#105 识别**：webhook 按 wxid 查 wechat_identity，返回 userId/bound。
- **#106 路由**：self（发给自己 Bot）→ DeepSeek 主 / Kimi 备双通道秘书回复；peer（@对方）→ A2A 协商。
- **#107 出站**：`openclaw message send --channel openclaw-weixin` CLI Sender，真实返回 deliveryStatus/messageId；OPENCLAW_DRY_RUN 支持离线断言。
- **#108 Agent↔Agent 协商**：微信 peer 消息桥接为 8 态协商会话（PROPOSE→WORKING→…→COMPLETED/REJECTED/AUTH_REQUIRED），落 negotiation_sessions/turns，webhook 带 negoSessionId/negoIntent 推进；轮次熔断上抛。
- **#109 用户↔用户**：消息落 bot_route_messages + wechat_relay_messages，经发送方 bot 账号 openclaw 主动推对方微信，delivered/messageId 回写。
- **迁移**：027_relay_peer_delivery（peer_delivered / peer_message_id）。
- **测试**：relay 真实 PG 测试（self/幂等/peer 推送/协商 PROPOSE→ACCEPT），openclaw CLI dry-run 真机 PASS，全部零残留 t.Cleanup。
- **模型**：DeepSeek 主 / Kimi(kimi-k2.6) 备 ChainProvider，主失败自动切备。

---

## [v2.1.1] - 2026-10-24 (稳定版)

### 微信 Bot 体系（v2.0.0–v2.1.0 全量落地 + 实机公网可达）
- **v2.0.0** Bot 组绑定：一个 wxid 绑一组、组内 ios/android/windows 三端，DB 唯一索引强制。
- **v2.0.1** wxid 持久识别（首次 Identify 分配 uid，后续复用）；Agent 衍生额度（1 主 / N 管理 / N 子），VIP 档位 free/basic ¥99/pro ¥299/enterprise ¥999，事务 FOR UPDATE 扣费。
- **v2.0.2** 公私钥身份（A 模式服务器托管）：Ed25519 密钥对、私钥 AES-256-GCM 加密落库。
- **v2.0.4** 微信 Bot 协议契约：信封 / 卡片按钮回传 / 幂等键 / Ed25519 签名。
- **v2.0.5** API 内核：时间漂移 ±300s、验签、幂等重放、类型分发。
- **v2.0.6** Bot SDK Client：自动构造信封/签名/幂等/POST。
- **v2.0.7** Agent 间通信全链路：identify→occupy→directory upsert→a2a.send。
- **v2.0.8** 临时协商群：创建/加入/发言/历史/销毁（软标 closed + 物理清消息成员）。
- **v2.0.9** Bot↔Bot 路由：双方必须已绑定、禁止自发自、收件箱拉取。
- **v2.1.0** 个人 Agent 工作汇报：聚合当日活动生成草稿、人工审批、按 user+date 去重。

### 审查修复（本轮）
- 补 018 表生产库迁移；为 wechatbot 包补真实落库测试（组满/端重复/wxid 跨组/坏设备/Unbind）。
- 新增统一网关 `internal/api/wechat_v2.go`，18 个 v2 端点接入 `/v1/wechatbot/ /v1/wechat/ /v1/quota/ /v1/tempgroup/ /v1/botroute/ /v1/report/`，公网 https://geefish.cn 实测可达。
- 修复 019 migration `plan_key` 未加引号导致网关启动崩溃（embed 迁移 42703）。
- 修复 6 个测试文件 `defer d.Close()` 早于 `t.Cleanup` 造成连接先关、删除静默失败（t.Cleanup LIFO 先删后关），8 张 v2 表测试后残留全 0。
- privacy cleanup 死锁（40P01）加重试×3。

### 门禁
- `go build ./...`、`go vet ./...`、37 包全量 `go test ./...` 两轮零 FAIL；gateway 实机 active。

---

## [v1.9.9] - 2026-10-24 (Beta)

### v1.9.0–v1.9.5 身份与数据主权
- **v1.9.0** JWT 身份地基：stdlib HMAC-SHA256 签发/校验、强制 HS256、恒定时间比较、登录 upsert、/me。
- **v1.9.1** 多用户隔离收紧：Authenticate 统一中间件，横向越权读他人资源一律 403。
- **v1.9.2** Context 端到端信封加密：AES-256-GCM、随机 nonce、GCM 认证、错 key 拒绝、明文不出端。
- **v1.9.3** Context 可移植导出/导入加密包（密文快照迁移语义）。
- **v1.9.4** 应用层真实余弦向量检索（256 维 bigram 哈希 + L2 归一化），替换 ILIKE。
- **v1.9.5** 私有化离线全栈：多阶段 Dockerfile（33.8MB）、全栈 compose、GA 手册。

### v1.9.6–v1.9.9 GA 上线
- **v1.9.7** 全链路压测：1000 并发 ×5000 真实写 PG，QPS 3911、p99 690ms、零失败；100 并发协商会话全 201；限流阈值 env 可配置（RATE_LIMIT_RPS/BURST）。
- **v1.9.8** 灰度发布门控：internal/gray FNV 一致性分桶，按 user/Agent 百分比放量；GET /v1/gray/eval；10000 实体分布实测。
- **v1.9.9** Prometheus 监控：internal/metrics 真实计数 + /metrics 文本端点 + 请求延迟 summary；/metrics 与 /healthz 加 admin token 鉴权；监控告警手册。

### 安全
- /metrics、/healthz、/v1/admin/*、/v1/privacy/* 统一 admin token 鉴权。
- 修复 real_test cleanup 顺序导致的测试残留（t.Cleanup LIFO 先删后关）。

---

## [v1.8.8] - 2026-10-24

### 门禁 `-race` 实测缺陷修复（全新缓存稳定通过）
- **迁移并发死锁（`internal/store/migrate.go`）**：跨包测试 / 多实例同时 `Migrate` 对 001–015 DDL 并发执行，在 PG 表锁与系统目录上死锁（SQLSTATE 40P01）。改为单连接 `pg_advisory_lock('IM2A')` 串行执行全部迁移，defer 解锁（conn.Close 兜底）；非 PG 后端拿锁失败回退直跑。
- **推送重试越权 + 失败误标 sent（`internal/push/push.go`、`internal/api/push.go`）**：`/v1/push/retry?user_id=X` 原实现全量调度全表用户（越权），且腾讯云返回 500 时任务仍被错误置为 sent。改为单事务 `SELECT ... FOR UPDATE SKIP LOCKED` 按用户/全量抢占，凭据查询 / HTTP / 状态回写同事务，失败仅累加 attempts 保持 pending；新增 `DispatchUserPending`（空 userID 报错），多实例并发安全。
- **Postgres 存储测试长期误 skip（`internal/store/store_test.go`）**：`TestPostgresSkipped` 因 `PG_DRIVER` 从未设置而永远空 driver 跳过（证明从未真跑）。修复默认 driver=pgx、按 identity 外键顺序先建用户再建信任档、`t.Cleanup` 级联清理并加前缀兜底（`ENC_PG_STORE_%`）。
- **推送测试跨进程串扰（`internal/push/v173_real_test.go`、`internal/api/v174_real_test.go`）**：两个测试进程共享物理表 `im_push_tasks` 互相标记，用 `pg_advisory_lock('PUSH')` 串行化，互不污染。
- **移除硬编码生产 admin token（`internal/security/pentest_real_test.go`）**：渗透测试不再固化管理凭据，admin token 仅经 `IM2A_ADMIN_TOKEN` 环境变量注入，未设置时 live 套件自动跳过；带 token 实跑 security_headers/authz/sql_injection/path_traversal/oversize_body/horizontal_isolation/stored_xss/rate_limit 共 8 项全 PASS。
- **证据**：`go clean -testcache` 全新缓存后连续 2 轮 `make gate` 均 `GATE_EXIT=0`，0 FAIL / 0 race / 0 deadlock，27 包 ok、8 服务自测 PASS、全业务表零残留。

### 安全 / 合规（全版本审查修复）
- **修复不可变审计（WORM）被测试绕过的合规红线**：v1.7.6–v1.7.10 运营后台测试曾用 `ALTER TABLE ... DISABLE TRIGGER` 临时关闭 append-only 审计触发器再删数据。已彻底移除 5 处绕过，改用唯一 `trace_id`/`ref` 隔离数据，审计触发器在测试全程启用；同时保留「UPDATE/DELETE 真实审计行必须被拒绝」的断言。
- **修复全量并行测试偶发 FAIL 根因**：上述 `DISABLE TRIGGER` 会全局切换触发器状态，正是 `privacy.TestV186ExportAndErase` 在并行运行时偶发失败的根因。修复后连续 3 轮全量 `go test ./...` 全部 ok、0 FAIL / 0 panic。
- **测试零残留**：新增统一的 `internal/api/cleanup_helpers_test.go`，为早期 v1.0/v1.2–v1.8.0/渗透测试等约 20+ 个测试补全 `t.Cleanup`，干净库跑全量前后业务表行数完全一致。
- **消除静默吞错**：`im.go`、`push.go` 等模块 5 处忽略错误改为显式记录。
- **历史库清理**：一次性清理 22 张业务表残留、孤儿用户与工作记忆脏数据，恢复干净基线。

### 工程化 / 发布
- 版本 single-source 收口到 **v1.8.8**：`internal/version/version.go`、`Makefile`、`npm/package.json`、`npm/bin/im2a.js`、README 徽章。
- 新增本 `CHANGELOG.md`，系统沉淀 v0.x–v1.8.8 演进。
- 全仓库 `gofmt`（33 个文件）；非测试代码无 `TODO/FIXME/HACK`。
- 五方发布：GitHub Release（6 平台二进制 + checksums）、npm 1.8.8、CI、Compat、Release workflow。

### 验证证据（2026-10-24 实跑）
- `go build ./...` / `go vet ./...`：通过；`gofmt -l`：0 遗漏。
- `go test ./...`：37 包全 ok（含 `-race`），0 FAIL / 0 panic，连续 3 轮 ALL OK。
- `make gate`：build / vet / race 测试 / 8 服务自测全 PASS。
- `make compat`：9 proto 契约、版本与 healthz 一致、核心路由在场、6 平台矩阵可编译，ALL CHECKS PASS。
- 生产网关 `/healthz`：`version=v1.8.8`。

---

## [v1.8.7] - 渗透测试与安全收口

- 新增可运行的渗透测试套件（`internal/security/pentest_real_test.go`）：覆盖未认证访问、弱 Bearer、伪造管理 token、注入、越权、越权读他人数据、限流、安全响应头等 8 个攻击向量，对真实网关发起攻击。
- 修复闭环：管理 token 独立于普通 token 并仅放行管理/出端/隐私路由；`/v1/egress/*` 强认证；越权访问返回 403 并写 `security_events`。
- 输出《IM2A 安全测试报告》，区分已修复 / 已接受 / 环境资源闸门。

## [v1.8.6] - 等保 / 个保对齐

- 数据主体权利接口：访问（export）、更正（correct）、删除/被遗忘（erase）、导出副本、拒绝自动化处理。
- `security_events` 与合规文档对齐；导出/擦除操作全程留痕，且不破坏 append-only 审计。

## [v1.8.5] - 数据脱敏

- 手机号、金额、地址在出端响应中按规则脱敏；审计与账本保留原值（脱敏可证明、可追溯）。
- 真实落库断言：出端脱敏、审计原值、按用户隔离。

## [v1.8.4] - STRIDE 威胁建模与修复

- 完成 STRIDE 威胁建模（仿冒/篡改/抵赖/信息泄露/拒绝服务/越权）。
- 修复：强认证、IP 级限流、安全响应头、注入防护、IDOR 越权拦截、审计不可抵赖。

## [v1.8.3] - 安全合规基线

- 安全中间件（认证、限流、响应头、安全事件）。
- 安全事件落库 `security_events`；建立威胁模型框架与检查清单。

## [v1.8.2] - 双层经验库（Propose-Commit-Audit）

- 经验库双层：逐案例诊断层 + 蒸馏后的全局模式层。
- 责任分离：案例层写入源**仅限人类审批决策**（模型不得自写）；模式层须**人类批准**才生效；最终审计裁决权归人类/确定性规则，不交给 Summary Auditor 模型。
- 真实落库断言：Agent 自主写入被拒、未批准模式不生效、审批反馈方可入库。

## [v1.8.1] - Agent 通讯录一等公民

- Agent 拥有对外别名（邮箱/电话）、Attention Level、tags、受限月预算钱包，在沙箱内代表用户对外。
- 信任对象为「Agent 背后的人」，Agent 继承信任档位而非独立积累。
- 借鉴 IMClaw 的 Trust/tags/Attention 产品化设计，并将付费对象修正为「付费触达个人（经 Agent 预审）」。

## [v1.8.0] - A2A Agent 协商通道

- 统一信封 E2A/E2AGateway，归一化多 IM 渠道（借鉴 dsh-meeting / JiuwenSwarm）。
- Agent 间协商：`msg_id` 幂等、轮询收件箱、deliver 回执；协商流与人类 IM 物理分离。
- 采用 SLIM 思路：协商与 Skill Runtime 统一传输，A2A/MCP 复用 HTTP/2 加密传输，不自建传输层。

> 同期落地：**真实 APNS 推送通道**（.p8 JWT，官方 apns2 库，待 Apple 凭据真机投递）。

---

## [v1.7.x] - 运营后台与公网 HTTPS

- **v1.7.0**：Webhook 公网 HTTPS 投递，`deploy/https-setup.sh` 自动签发 Let's Encrypt 证书并放行 443。
- **v1.7.1–v1.7.2**：管理仪表盘（用户/Agent/付费/调用量实时面板，7 天趋势、付费流水、Agent 明细）。
- **v1.7.3**：客服工单闭环（用户反馈 → 工单 → 处理），独立 admin token 与 `support_tickets` 顶卡。
- **v1.7.4**：审计可视化（事件流时间线、按 trace 门控决策回放）。
- **v1.7.5**：内容审核（规则落库自动扫描 + 人工审核队列 + 规则管理 + 后台 UI）。

## [v1.6.x] - 工程化与五方发布

- 腾讯云 IM 真实打通：官方 TLSSig v2 UserSig、account_import、跨账号 C2C、真实建群与群消息。
- Webhook 回调接收（字段归一、?key 验签、SdkAppID 校验、幂等落库）。
- 付费账本收口：v0.9 老表确定性迁移到 `paid_message_ledger`，老表归档。
- 统一版本 single-source（`/healthz` 暴露 version + commit）、6 平台二进制、校验和、Compat 兼容矩阵、npm 跨平台启动器。
- **v1.6.5 首次五方发布**：CI / Compat / Release workflow / GitHub Release / npm。

## [v1.5.x] - 手表端与多端同步

- 手表一键审批、通知、一键通过/驳回、紧急升级。
- 离线审批缓存（断网可审批，联网同步）、多端状态同步（LWW 冲突裁决、离线重放、超时升级）。

## [v1.4.x] - 端侧智能 + BLE

- 端侧唤醒词、意图粗分类（小模型/规则可插拔）、敏感词前置过滤（不出端）。
- BLE 网关协议（耳机/眼镜/指环接入）；「音频不出端」出端流量白名单审计报告。
- Flutter 端真实接入 `flutter_blue_plus`（真机 BLE 通信依赖硬件环境）。

## [v1.3.x] - Flutter 完整客户端

- 审批态卡片流（通过/修改/驳回/超时升级）、巡视态（Agent 工作轨迹/协商时间线/审计事件流）、会话态（Agent 摘要层 IM + 人类消息分流）。
- `api_client` 真实对接网关全部路由；Flutter Desktop 三栏窗口与多端同步。

## [v1.2.x] - 腾讯云 IM 双通道物理隔离

- v1.2.0：人类消息走腾讯云 IM、Agent 消息走自研协商流；v1.2.1：UserSig 签发；v1.2.2：Webhook 回调；v1.2.3：消息路由分流；v1.2.4：群聊（关系链/群成员/广播）。

## [v1.1.x] - 实时通道

- WebSocket 长连接：在线状态、心跳、多端连接管理、消息实时下发。

## [v1.0.x] - 生产化层

- 认证中间件、真实 PG 消息存储、全局限流、优雅退出、配置热加载。

---

## [v0.x] - MVP 到平台雏形

- **v0.1–v0.2**：六张核心表、存储接口层（内存/PG 双实现）、MCP Skill Runtime、7 个核心服务、真实 Go 测试。
- **v0.3–v0.5**：Demo 单二进制、网页运维界面、Linux 服务器二进制、局域网 docker-compose、大模型驱动、跨端客户端、公网生产部署、统一 API 网关 + 事件总线。
- **v0.6**：Context 记忆基座（工作/情景/语义，子 Agent 隔离）、TTL、导入导出、衰减、搜索、幂等、事件流、快照回滚。
- **v0.7**：蒸馏闭环（审批反馈 → Improvement Memory → 阈值自适应，不改基座模型）。
- **v0.8**：L5 模型抽象层（Provider 接口、云端真实调用、智能路由、审计、fallback 链、配额、9 意图域、热切换）。
- **v0.9**：陌生人付费注意力经济（发送即扣费 / 回复才扣费、自主定价、退款、收件箱、黑名单、过期、对账导出、偏好）。

---

## 架构落地说明（与早期设计基线的差异）

- 早期设计基线中的 **gRPC / Kafka / pgvector** 未引入；实际协商通道走 **A2A over HTTP**，审计/事件走**事件流 + 落库**，检索优先用 PostgreSQL 能力。OpenSearch 按部署环境（低内存服务器）默认关闭。
- 核心不可妥协原则保持不变：① 付费在 Agent 预审之后；② 信任边界是代码层门控（非提示词自觉）；③ 「不上传原始音频」以出端流量白名单审计可验证。

## 已知资源闸门（诚实标注，非功能缺失）

- Flutter 客户端需在具备 Flutter SDK 的机器执行 `flutter create . && flutter analyze` 补齐平台目录后编译；真机 BLE 需耳机/眼镜/指环硬件。
- 腾讯云 IM 主动 API live 测试、APNS 真机推送需对应凭据/证书与真机环境。
