# clients-flutter · 客户端最小可读骨架

> ## ⚠️ 未编译声明（必读）
>
> **本机没有安装 Flutter SDK / Dart SDK，本目录下的代码没有经过 `flutter pub get`、`flutter analyze` 或编译验证。**
> 代码按 Dart 3 语法手工编写并做了人工审阅（正确 import、类型标注、async/await、const、无伪代码），
> 但**不能保证一次编译通过**。接入 CI 前请先在一台装有 Flutter 的机器上执行：
>
> ```powershell
> cd E:\GPT\agent-im\apps\clients-flutter
> flutter pub get
> flutter analyze
> flutter run -d windows --dart-define=API_BASE_URL=http://127.0.0.1:8787
> ```
>
> `pubspec.yaml` 里的 SDK 约束与 `http` 版本号均标了 **⚠ 需复核**，请以团队统一的 Flutter 稳定版为准。

---

## 1. 这个骨架是什么

「AI 人机混合熟人社交 IM」的**客户端最小骨架**，只做两件事：

| Tab | 作用 | 文档 |
|---|---|---|
| **审批态**（默认） | 一屏一卡、三个动作（通过 / 修改 / 驳回）、倒计时、L3 高风险二次确认、按 `priority` 分节 | `docs/07` §1 |
| **巡视态** | 协商泳道 + 执行中任务 + 轨迹钻取（调用链 / 记忆摘要 / 中间结果） | `docs/07` §2 |

另有「陌生人来意卡片」页（`conversation.dart`）：结构化来意 + 预审结论 + 计费模式 + 忽略/接受并查看。

数据源只有一个：轮询 `GET /api/state`（默认 5s）；
动作只有一条：`POST /api/approvals/{id}/decide`。客户端**从不本地推断状态迁移**——
服务端返回的 `snapshot` 就是唯一状态源。

## 2. 目录结构

```text
apps/clients-flutter/
├── pubspec.yaml                  # flutter sdk 约束 + http 依赖（⚠ 版本需复核）
├── README.md                     # 本文件
├── .gitignore
└── lib/
    ├── main.dart                 # 双 Tab（审批态 / 巡视态）+ 轮询生命周期
    ├── models/
    │   ├── approval.dart         # TICKET_STATE 九态 / TICKET_TYPE / 优先级 / 信任档位 / 风险等级
    │   ├── negotiation.dart      # TASK_STATE 八态 / INTENT 六意图 / 会话与轮次
    │   └── paid_message.dart     # MSG_STATE 九态 / BILLING 两档 / 来意四要素
    ├── screens/
    │   ├── approval_home.dart    # 审批态 + 卡片 + 修改抽屉 + 倒计时
    │   ├── oversight_view.dart   # 巡视态 + 协商摘要 + 执行轨迹钻取
    │   └── conversation.dart     # 陌生人「来意卡片」与会话动作
    └── sdk/
        ├── api_client.dart       # REST 封装、错误码 -> ApiException
        └── state_store.dart      # ChangeNotifier 状态仓库 + 轮询 + 错误文案
```

## 3. 与冻结契约的映射（逐字一致）

状态枚举的 `wire` 值必须与 `contracts/INTERFACES.md` 完全一致，客户端**不得**自造状态：

| 模型 | 枚举 | 契约位置 | 值 |
|---|---|---|---|
| `approval.dart` | `TicketState` | §4 `TICKET_STATE` | `PENDING` `REVIEWING` `APPROVED` `REJECTED` `MODIFIED` `EXECUTING` `COMPLETED` `FAILED` `EXPIRED` |
| `approval.dart` | `TicketType` | §4 `TICKET_TYPE` | `NEGOTIATION` `PAYMENT` `SKILL_CALL` `EXTERNAL_ACTION` |
| `approval.dart` | `DecisionAction` | §4 `decide()` action | `APPROVE` `MODIFY` `REJECT` |
| `approval.dart` | `TrustZone` | §1 `TRUST_LEVELS` | `SELF`(0) `FAMILY`(1) `TRUSTED`(2) `KNOWN`(3) `STRANGER`(4) |
| `approval.dart` | `TicketPriority` | §4 `priority` | `urgent` `high` `normal` `low` |
| `negotiation.dart` | `TaskState` | §5 `TASK_STATE` | `SUBMITTED` `WORKING` `INPUT_REQUIRED` `AUTH_REQUIRED` `COMPLETED` `FAILED` `CANCELED` `REJECTED` |
| `negotiation.dart` | `NegotiationIntent` | §5 `INTENT` | `PROPOSE` `COUNTER` `ACCEPT` `REJECT` `REQUEST_INFO` `REQUEST_AUTH` |
| `paid_message.dart` | `MsgState` | §9 `MSG_STATE` | `INTENT` `AWAITING_PAYMENT` `REFUSED` `PAID` `VIEWED` `REPLIED` `SETTLED` `REFUNDED` `EXPIRED` |
| `paid_message.dart` | `BillingMode` | §9 `BILLING` | `SEND_PAY`(1) `VIEW_REPLY_PAY`(2) |

金额一律整数分（`amountCents` / `Cents` 结尾）传输，只在 `formatYuan()` 展示层换算。

## 4. 对接 `apps/demo-server`（已按真实响应结构对齐）

启动：

```powershell
node E:\GPT\agent-im\apps\demo-server\server.mjs     # 默认 127.0.0.1:8787
```

### 4.1 演示服务实际提供的路由

| 方法 | 路径 | 用途 | 客户端是否已接 |
|---|---|---|---|
| GET | `/api/health` | 健康检查 | 是（`ping()`） |
| GET | `/api/state` | 全量状态快照（核心轮询） | 是（`fetchState()`） |
| POST | `/api/approvals/{id}/decide` | 审批三动作，body `{ action, comment? }` | 是（`decide()`） |
| POST | `/api/reset` | 重置演示世界 | 否（curl / 控制台用） |

下列路由属于**契约规划**（`contracts/openapi/orchestrator.openapi.yaml`），
演示服务**尚未实现**，骨架里保留了 SDK 方法但调用会得到 404：
`POST /api/intent`、`POST /api/intent/{id}/screen|pay|view|reply`。

### 4.2 `GET /api/state` 真实结构（节选，客户端只取前三项）

```json
{
  "approvals": [
    {
      "id": "tkt-0001",
      "userId": "user-lin",
      "sourceAgentId": "agent-lin-stranger-filter",
      "ticketType": "NEGOTIATION",
      "state": "PENDING",
      "priority": "high",
      "createdAt": "2026-10-21T03:00:00.000Z",
      "deadline": "2026-10-21T03:30:00.000Z",
      "traceId": "trace-stranger-001",
      "payload": {
        "kind": "STRANGER_INTENT",
        "messageId": "msg-0001",
        "senderName": "周先生",
        "topic": "A 轮融资沟通（20 分钟）",
        "summary": "周先生（早期投资人）希望就 A 轮融资沟通 20 分钟……",
        "amountCents": 5000,
        "billingMode": 2,
        "screening": { "verdict": "PASS", "rule": "接受工作与融资类询盘" },
        "negotiation": { "rounds": 2, "artifact": "周五 16:00-16:20 视频 20 分钟" },
        "risk": "medium",
        "decisionHint": "通过后对方将被扣 ¥50 并进入你的会话；你回复后才算完成结算。"
      }
    }
  ],
  "negotiations": [
    {
      "id": "neg-0001",
      "topic": "A 轮融资沟通：周先生希望与林总约 20 分钟",
      "state": "COMPLETED",
      "round": 2,
      "tokens": 1300,
      "initiatorAgentId": "agent-zhou-butler",
      "responderAgentId": "agent-lin-butler",
      "terminalReason": null,
      "artifact": { "type": "AGREEMENT", "summary": "周五 16:00-16:20 视频 20 分钟" },
      "turns": []
    }
  ],
  "messages": [
    {
      "id": "msg-0001",
      "senderUserId": "user-zhou",
      "receiverUserId": "user-lin",
      "topic": "A 轮融资沟通（20 分钟）",
      "summary": "周先生（早期投资人）希望就 A 轮融资沟通 20 分钟……",
      "state": "AWAITING_PAYMENT",
      "billingMode": 2,
      "amountCents": 5000,
      "createdAt": "2026-10-21T03:00:00.000Z"
    }
  ],
  "meta": { "generatedAt": "2026-10-21T03:00:00.000Z" }
}
```

兼容性说明（客户端已处理）：
1. 协商列表键名可能是 `negotiations`（演示服务）或 `sessions`（契约网关），两者都读；
2. snapshot 可能被包在 `{ "state": { ... } }` 里，也兼容；
3. `messages` 不含来意四要素与预审结论——它们挂在关联审批单的 `payload` 上，
   `StateStore.ticketForMessage()` 用 `payload.messageId` 关联后展示（见 `conversation.dart`）。

### 4.3 `POST /api/approvals/{id}/decide` 返回

```json
{
  "ok": true,
  "ticketId": "tkt-0001",
  "state": "COMPLETED",
  "result": { "messageId": "msg-0001", "charged": true, "amountCents": 5000 },
  "messageState": "SETTLED",
  "snapshot": { "approvals": [], "negotiations": [], "messages": [] }
}
```

客户端直接消费 `snapshot`（`ApiClient.decide()` → `StateStore.decide()`），
所以「通过 → 扣费 → 查看 → 回复 → 结算」全链路一次刷新到位。

### 4.4 错误体与错误码

演示服务错误体：`{ "error": "ILLEGAL_TRANSITION", "message": "..." }`（`error` 为字符串）；
契约网关可能是 `{ "error": { "code": "...", "message": "..." } }`。两种都兼容，统一抛 `ApiException(code, message, statusCode)`。

`state_store.dart` 的 `humanizeError()` 负责把错误码翻成人话，界面不允许直接显示英文码：

| 契约错误码 | 文案 |
|---|---|
| `FORBIDDEN` | 这不是你的单据，无法操作。 |
| `EXPIRED_TICKET` | 这张审批单已超时，请刷新后查看。 |
| `NOT_FOUND` | 单据不存在或已被撤回。 |
| `ILLEGAL_TRANSITION` | 状态已变化，请刷新后重试。 |
| `NOT_PAYABLE` | 这条消息未通过预审，不能付费。 |
| `NEEDS_SCREENING` | 对方还在说明来意，稍后再看。 |
| `DAILY_CAP_EXCEEDED` | 今日发送已到上限。 |
| `SELF_MESSAGE` | 不能给自己发付费消息。 |
| `TIMEOUT` / `NETWORK` | 直接展示 `ApiException.message`。 |

### 4.5 服务端地址

默认 `http://127.0.0.1:8787`（与 demo-server 一致）。覆盖方式：

```powershell
flutter run --dart-define=API_BASE_URL=http://192.168.1.10:8787
```

## 5. 关键交互约束（与 docs/07 对齐）

| 约束 | 骨架里的落点 |
|---|---|
| 三个动作恰好 `APPROVE` / `MODIFY` / `REJECT` | `ApprovalCard` 底部按钮 |
| L3 高风险必须二次确认 | `_approve()` 里 `riskLevel == high` 时弹 `AlertDialog` |
| 修改只提交结构化 diff | `_ModifySheet` 只收金额，回传 `{'amountCents': cents}` |
| 卡片倒计时 1s 刷新 | `ApprovalCard` 内的 `Timer.periodic` |
| 服务端排序优先（priority→deadline） | 客户端只在 `pendingGroups` 里分节，不重排 |
| 「接受并查看」= pay + view | `_acceptAndView()` 依次调用 `payMessage` / `viewMessage`（幂等） |
| 总管看不到子 Agent 原始记忆 | `_SessionSheet` 只展示 digest 与协商摘要 |

## 6. 已知局限（本骨架刻意没做的）

1. **没有状态管理库**：只用 `ChangeNotifier` + `AnimatedBuilder`，避免为骨架引入 `provider`/`riverpod`。
2. **没有持久化**：冷启动空白，靠首轮 `GET /api/state` 填充；正式版需本地缓存卡片与草稿 `patch`。
3. **没有鉴权**：demo-server 无登录态；正式版需接入实名与 token 刷新。
4. **没有手表/耳机通道**：`docs/07` §5/§6 的三屏与语音链路未实现。
5. **没有熟人 IM 消息列表**：熟人聊天走 IM PaaS（见 `docs/01`/`docs/04`），骨架只覆盖审批与来意。
6. **轮询而非长连接**：5s 轮询仅用于联调，正式版应换 WebSocket/长轮询。
7. **来意四要素依赖审批单 payload**：演示服务的 message 快照不含这些字段，属结构性缺口，正式网关应在 message 上补齐。
8. **无障碍与降级只做了基础提示**：断连横幅、按钮置灰；完整方案见 `docs/07` §8。

## 7. 下一步（对接顺序建议）

1. 在装有 Flutter 的机器上 `flutter pub get && flutter analyze`，把编译错误清零。
2. `node apps/demo-server/server.mjs`，打开 `GET /api/state`：应看到 2 张待审批卡片
   （陌生人付费消息 `NEGOTIATION` / 采购 `SKILL_CALL`）与 1 条 `AWAITING_PAYMENT` 消息。
3. 在审批态点「通过」：卡片应变为 `COMPLETED`，来意消息进入 `SETTLED`，账本只出现 1 条 `CHARGE`。
4. 点「修改」提交新金额：演示服务忽略 `patch`、只记 `modifiedAt`；正式网关需补 `patch` 校验。
5. 补 `flutter_test` 单测：`TicketState.parse` 非法值抛错、`ApprovalTicket.fromJson` 缺字段容错、
   `humanizeError` 覆盖全部错误码。
