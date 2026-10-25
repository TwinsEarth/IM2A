# Release v0.9.9 — 陌生人付费注意力经济全链路

**日期**：2026-10-22
**类型**：稳定版
**里程碑**：M2 闭环经济

---

## 本版本包含

### v0.9.0-v0.9.8 付费经济系列（9 个小版本）

| 版本 | 功能 |
|---|---|
| v0.9.0 | 陌生人付费闭环（compliance 预审 → ledger reserve/charge） |
| v0.9.1 | 用户自主定价 + 拒收退款 |
| v0.9.2 | 付费消息收件箱/已发列表/内容查看 |
| v0.9.3 | 付费统计 dashboard |
| v0.9.4 | 付费消息黑名单 |
| v0.9.5 | 消息过期（超时自动关闭） |
| v0.9.6 | 按状态过滤查询 |
| v0.9.7 | 批量导出（对账用） |
| v0.9.8 | 接收方偏好（自动接受/自动拒绝） |

### 新增端点（15 个）

```
POST /v1/paidflow/send          发送付费消息
POST /v1/paidflow/open          打开消息（billingMode=2 扣费）
POST /v1/paidflow/price         设置接收方最低定价
POST /v1/paidflow/reject        拒收并退款
GET  /v1/paidflow/inbox         收件箱（?status= 过滤）
GET  /v1/paidflow/outbox        已发列表（?status= 过滤）
GET  /v1/paidflow/content       查看消息内容
GET  /v1/paidflow/stats         付费统计 dashboard
POST /v1/paidflow/block         拉黑发送方
POST /v1/paidflow/unblock       解除拉黑
POST /v1/paidflow/expire        过期清理
GET  /v1/paidflow/export        批量导出（对账）
POST /v1/paidflow/preferences    接收方偏好设置
```

### 状态机

```
pending（reserve）
  ↓
charged（billingMode=1 发送即扣）
  或 delivered（billingMode=2 等待打开）
  ↓
opened（接收方打开）
  或 rejected（拒收 → 退款）
  或 expired（超时 → 自动关闭/退款）
  或 auto_rejected（偏好自动拒绝）
```

### 拦截顺序

```
send 请求
  ↓
1. compliance 预审（关键词/手机号/URL）
2. 接收方定价检查（minCents）
3. 黑名单检查
4. 接收方偏好（autoReject / autoAccept）
5. ledger reserve → charge
```

---

## 验证状态

| 验证项 | 结果 |
|---|---|
| `go build ./...` | ✅ 通过 |
| `go vet ./...` | ✅ 通过 |
| `go test -race ./...` | ✅ 通过 |
| 7 服务自测 | ✅ 7/7 PASS |
| 付费流程冒烟 | ✅ 全场景通过 |

---

## 已知限制

- 消息存储为内存实现（生产需换 PG）
- 无用户认证（gateway 层 Bearer token 可选）
- 无速率限制（生产需加）
- Flutter 客户端未实机编译

---

## 下一步

**v0.10**：协商双向流实跑（negotiation-svc 从状态机到真实 gRPC 双向流）
