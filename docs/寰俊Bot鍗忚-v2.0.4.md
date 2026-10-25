# 微信 Bot 协议 v2.0.4（完整契约）

> 传输：HTTPS + JSON over `https://geefish.cn/v1/wechatbot/*`
> 鉴权：wxid + Ed25519 签名（v2.0.2 userkey）
> 幂等：每条上行请求带 `idem`，服务器 24h 去重
> 版本：v1

---

## 1. 消息信封（Envelope）

### 1.1 上行（Bot → 服务器）
```json
{
  "v": 1,
  "wxid": "wx_abc123",
  "ts": 1792816000,
  "idem": "uuid-每次重试相同",
  "type": "approval.decision",
  "payload": { ... },
  "sig": "base64(ed25519_sign(priv, canonical_json))"
}
```

### 1.2 下行（服务器 → Bot）
```json
{
  "v": 1,
  "wxid": "wx_abc123",
  "ts": 1792816000,
  "trace": "req-trace-id",
  "type": "approval.card",
  "payload": { ... },
  "buttons": [
    {"id": "approve", "label": "同意", "style": "primary"},
    {"id": "reject",  "label": "驳回", "style": "danger"},
    {"id": "modify",  "label": "修改", "style": "default"}
  ]
}
```

### 1.3 canonical 签名原文
服务器验签时，取上行 envelope **去掉 sig 字段**后的稳定 JSON（key 按字典序），对其原文做 Ed25519 验签。

---

## 2. 卡片按钮回传（重点）

### 2.1 服务器推卡片
下行 `approval.card` 带 `buttons` 数组。每个按钮：
| 字段 | 说明 |
|---|---|
| id | `approve` / `reject` / `modify` / `accept` / `deny` / `upgrade` |
| label | 微信内显示文案 |
| style | primary / danger / default |

### 2.2 用户点按钮后 Bot 上行
Bot 不重新构造业务对象，只回传按钮 id + 关联对象 id：
```json
{
  "type": "approval.decision",
  "payload": {
    "ticket_id": "tk_2026_001",
    "button_id": "approve",
    "note": "同意",
    "client_ts": 1792816100
  }
}
```

### 2.3 服务器处理按钮回传
1. 验签 → 2. 幂等（同一 ticket_id + button_id 24h 内只生效一次）→ 3. 查审批单当前状态 → 4. 状态机校验（已审批的 ticket 拒绝重复操作，返回 `ERR_TICKET_STATE`）→ 5. 执行决策 → 6. 回发确认卡片。

### 2.4 重复点击防护
- 同一 `ticket_id` 已 `approve` 后，再点 `approve` 返回 `ERR_TICKET_STATE`，不重复执行。
- 按钮回传响应里带 `ticket_state`（approving/approved/rejected/expired），Bot 收到后置灰已点按钮。

---

## 3. 幂等键（Idempotency）

| 项 | 规则 |
|---|---|
| 生成 | Bot 端 uuid，同一业务操作重试时保持不变 |
| 存储 | `wechat_idem(idem PK, wxid, response JSONB, created_at)` 保留 24h |
| 重放 | 同 idem 直接返回首次响应 HTTP body，不触发副作用 |
| 按钮回传 | 除 idem 外，业务层再按 `ticket_id + button_id` 去重 |

---

## 4. 签名（Ed25519）

| 项 | 规则 |
|---|---|
| 密钥 | v2.0.2 userkey.Bind 生成；私钥 AES-GCM 加密落库 |
| 算法 | Ed25519（RFC 8032），签名 base64 |
| 漂移 | ts 与服务器差 > 300s 拒绝 `ERR_TS_SKEW` |
| 失败 | `ERR_SIG_INVALID`（401），不泄露公钥是否存在 |
| 下行 | 不签名（HTTPS 加密），Bot 端校验 wxid 防串号 |

---

## 5. 消息类型全表

| type | 方向 | payload 关键字段 | 门控 |
|---|---|---|---|
| `bind.confirm` | Bot→S | wxid, user_id | userkey.Bind 幂等 |
| `approval.card` | S→Bot | ticket_id, title, summary, ttl_sec, buttons | 审批 8 态 |
| `approval.decision` | Bot→S | ticket_id, button_id, note | 状态机 + 幂等 |
| `agent.create` | Bot→S | role(main/manager/sub), prompt | agentquota 额度 |
| `agent.create.result` | S→Bot | agent_id, quota_left | — |
| `intent.card` | S→Bot | from_wxid, summary, price_cents, trust_level | 五档信任 |
| `intent.review` | Bot→S | intent_id, button_id(accept/deny) | ledger 扣费 |
| `report.daily` | S→Bot | done[], pending[], approvals_due | 只读 |
| `report.ack` | Bot→S | report_id, seen | — |
| `quota.warn` | S→Bot | plan, used/limit, suggest_upgrade | — |
| `error` | S→Bot | code, message, retryable | — |

---

## 6. 错误码

| code | HTTP | 含义 |
|---|---|---|
| `OK` | 200 | 成功 |
| `ERR_SIG_INVALID` | 401 | 签名失败 |
| `ERR_TS_SKEW` | 401 | 时间戳漂移 |
| `ERR_WXID_UNBOUND` | 403 | wxid 未绑定 |
| `ERR_QUOTA_EXCEEDED` | 409 | Agent 额度满 |
| `ERR_TICKET_STATE` | 409 | 审批单状态不允许该操作 |
| `ERR_IDEM_REPLAY` | 200 | 幂等重放（返回首次结果） |
| `ERR_RATE_LIMIT` | 429 | 限流 |

---

## 7. 与既有模块映射

| 协议消息 | 后端模块 |
|---|---|
| bind.confirm | userkey.Bind + wechatid.Identify |
| approval.* | approval 状态机 |
| agent.create | agentquota.Occupy + agentdir.Upsert + a2a.Send |
| intent.* | negotiation + ledger |
| report.daily | distill 摘要 |
| 门控 | trustzone 五档 |

## 8. 不在本协议范围

- Agent 间后台协商流（A2A，v2.0.7）
- 临时协商群（v2.0.8）
- 个人对个人 Bot 通话路由（v2.0.9）
- 端侧硬件（独立通道）
