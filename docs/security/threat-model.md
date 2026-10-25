# IM2A 安全与合规基线（v1.8.3）

> 本文是安全工作的**框架与基线**；完整的逐资产 STRIDE 威胁建模、风险评分与修复闭环见 v1.8.4《STRIDE 威胁模型》。

## 1. 安全设计原则（三条不可妥协）

1. **信任边界是代码层门控，不是提示词**：五档信任矩阵、Capability 校验在服务端代码强制（TrustZone），LLM 无法绕过。
2. **付费发生在 Agent 预审之后**：陌生人触达必须先过意图预审，再进入付费账本。
3. **最小权限 + 默认拒绝**：子 Agent 在沙箱与受限身份（独立别名/预算钱包）内运行；跨域访问默认拒绝，显式授权才放行。

## 2. 资产清单（v1.8.4 逐资产 STRIDE 的对象）

| 资产 | 位置 | 敏感等级 |
|---|---|---|
| 网关与鉴权 | gateway（HTTP 8080/nginx 443） | 高 |
| 业务数据 | PostgreSQL（pgvector） | 高 |
| 人类消息 | 腾讯云 IM PaaS | 高 |
| Agent 协商 | 自研 A2A 通道（a2a_envelopes） | 中 |
| 审批/付费账本 | approval / paid_message_ledger | 高（不可变） |
| 记忆与经验库 | memory / case_experiences / patterns | 高（个人 Context） |
| 审计事件流 | audit_events / security_events | 高（追溯） |
| Webhook | /v1/im/webhook（签名校验） | 中 |
| 端侧音频 | 手机端（不出端） | 高（可验证承诺） |

## 3. v1.8.3 已落地的安全控制

| 控制 | 实现 | 证据 |
|---|---|---|
| 安全响应头 | `security.Headers`：nosniff / DENY frame / CSP / HSTS / Referrer-Policy / Permissions-Policy | TestV183Headers |
| CORS 白名单 | `security.CORS`：默认仅放行 geefish.cn，非白名单来源预检 403，不回 ACAO | TestV183CORSWhitelist |
| 请求级安全审计 | `security.Audit` 落库 `security_events`（migration 014），按 401/403/429 自动归类 | TestV183AuditPersistsAndClassifies |
| 真实 IP 提取 | X-Forwarded-For 首段（nginx 反代） | 同上 |
| 管理员隔离 | /v1/admin/ 独立 IM2A_ADMIN_TOKEN；安全事件仅管理员可查 | 既有 + /v1/admin/security |

## 4. 纵深防御分层

```
客户端/端侧（唤醒/敏感过滤，音频不出端）
        │
nginx 443（TLS、Let's Encrypt）
        │
网关：CORS 白名单 → 安全审计 → 限流(IP) → Bearer 认证 → 安全头 → 业务
        │
TrustZone 代码层 Capability 门控（五档信任）
        │
PostgreSQL（参数化查询，所有 SQL 用 $1 占位）
        │
WORM 审计 + security_events 双审计流
```

## 5. 后续版本计划

- **v1.8.4**：逐资产 STRIDE（Spoofing/Tampering/Repudiation/Info Disclosure/DoS/Elevation），风险评分（可能性×影响），对发现的真实问题修复并回归。
- **v1.8.5**：手机号/身份证/金额/地址出端脱敏。
- **v1.8.6**：等保 2.0 三级与《个人信息保护法》对齐，数据主体权利（导出/删除）。
- **v1.8.7**：自动化渗透测试套件 + 修复 + 报告。

## 6. 安全事件查询

```
GET /v1/admin/security?type=auth_failed   # 仅管理员 Token
```

事件类型：`request` / `auth_failed`(401) / `blocked`(403) / `rate_limited`(429)。
