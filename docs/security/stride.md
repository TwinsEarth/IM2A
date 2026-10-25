# IM2A STRIDE 威胁模型与修复报告（v1.8.4）

> 方法：对 9 类核心资产逐一套用 STRIDE 六类威胁（Spoofing 仿冒 / Tampering 篡改 / Repudiation 抵赖 / Information Disclosure 信息泄露 / Denial of Service 拒绝服务 / Elevation of Privilege 提权）。
> 风险评分 = 可能性(1–5) × 影响(1–5)，R≥15 高、8–14 中、<8 低。
> 每条结论都对应真实代码审查（grep + 读源码），**不是纸面假设**；修复均有真实测试断言。

## 0. 本轮真实发现与处置总览

| 编号 | 威胁类 | 资产 | 发现 | 评分 | 处置 |
|---|---|---|---|---|---|
| F-01 | D(DoS) | 网关 | 除 edge/skillruntime/im webhook 外，多数 JSON handler 无 body 上限，超大 body 可放大内存消耗 | 4×4=16 高 | **已修复**：全局 `MaxBody(2MiB)` 中间件，TestV184MaxBody |
| F-02 | I(信息泄露) | 网关 | 根路径 `/` 公开回显 `Authorization: Bearer im2a_demo_token` 明文凭据 | 3×4=12 中 | **已修复**：改为 `"auth":"Bearer token required"`，不再回显 |
| F-03 | T(篡改) | PostgreSQL | 多处 `SELECT cols...where...` 字符串拼接 | 4×5=20（若成立） | **已核实安全**：where 的列名来自代码常量 map，用户值全部走 `$n` 参数占位（auditview/moderation/support 逐一核实），无注入 |
| F-04 | S(仿冒) | Webhook | 第三方回调是否验签 | 4×4=16 | **已核实有效**：配置 TIM_WEBHOOK_SECRET 时强制，支持签名头/`?key=`，失败 401；body 限 1MiB；MsgKey 幂等 |
| F-05 | S/E | 网关 | 共享静态 Bearer token，无法区分用户/无重放保护 | 4×4=16 中高 | **残余（设计内）**：Phase 0 单租户形态；多租户时升级为每用户 JWT（v1.9+），见 §4 |
| F-06 | T/E | 业务 | 五档信任/Capability 是否能被 LLM 绕过 | 3×5=15 高 | **已核实缓解**：TrustZone 代码层强制（非提示词）；relation 门控有真实测试 |
| F-07 | R(抵赖) | 审计 | 关键操作是否可追溯 | 3×4=12 | **已缓解**：audit_events 不可变 + v1.8.3 security_events 双审计流 |
| F-08 | D | 网关 | 高频打爆 | 3×3=9 中 | **已缓解**：IPLimit(100rps,burst200) + MaxBody |
| F-09 | I | 端侧 | 原始音频是否出端 | 4×4=16 | **已缓解（架构）**：端侧唤醒/敏感过滤，音频不出端；v1.4.4 出端流量白名单审计 |
| F-10 | I | 数据 | 手机号/金额/地址出端未脱敏 | 4×3=12 中 | **计划 v1.8.5**：internal/masking 出端脱敏 |

## 1. 逐资产 STRIDE 矩阵

### 1.1 网关与鉴权（gateway :8080 / nginx 443）
| 威胁 | 分析 | 处置 |
|---|---|---|
| S | 共享 Bearer token 静态有效 | F-05 残余；nginx TLS + 后续 JWT |
| T | 请求参数/Body 被篡改 | TLS；业务幂等键（message_id/ledger 幂等）|
| R | 攻击者否认访问 | security_events 记录 IP/方法/路径/状态 |
| I | 错误回显凭据、堆栈 | F-02 已修复；writeErr 不回显 SQL |
| D | 大 body、高并发 | F-01 已修复 + F-08 限流 |
| E | 普通 token 访问 /v1/admin/ | 独立 IM2A_ADMIN_TOKEN；F-05 后续按用户授权 |

### 1.2 PostgreSQL（含 pgvector）
| 威胁 | 分析 | 处置 |
|---|---|---|
| T | SQL 注入 | F-03 已核实：全量参数化（`$n`），列名走代码常量，无字符串拼接用户输入 |
| I | 库文件/备份泄露 | 容器内网，仅 127.0.0.1；非 root 业务账号 hai（v1.8.6 落库加密）|
| R | 账本被改/删除 | 付费账本幂等、WORM 审计；v1.8.6 加只追加视图 |
| D | 连接耗尽 | 连接池 + 网关限流 |

### 1.3 人类消息（腾讯云 IM PaaS）
| 威胁 | 分析 | 处置 |
|---|---|---|
| S | 伪造回调 | F-04 webhook 强制验签 + SDKAppID 校验 |
| T | 消息伪造 | UserSig 服务端签发，密钥不出服务端 |
| R | 消息否认 | 回调落 im_webhook_events，MsgKey 幂等 |
| I | 传输窃听 | 全链路 TLS / 腾讯云加密通道 |

### 1.4 Agent 协商通道（A2A，a2a_envelopes）
| 威胁 | 分析 | 处置 |
|---|---|---|
| S | 伪造 Agent 来信 | Agent 身份在 agent_directory 注册；信封经网关 token |
| T | 信封内容篡改 | 信封 ON CONFLICT DO NOTHING（delivered 幂等）；v1.9 加签 |
| E | 跨用户投递/收件箱越权 | Inbox 按 recipient 过滤；v1.8.7 渗透加横向越权用例 |
| D | 协商风暴 | 轮次/token 熔断（8 态状态机）|

### 1.5 审批与付费账本
| 威胁 | 分析 | 处置 |
|---|---|---|
| T | 重复扣费/改金额 | message_id 幂等、ON CONFLICT DO NOTHING、单日上限（payment-svc 测试）|
| E | 绕过预审直接付费 | 代码层：付费只发生在预审通过后（架构原则①）|
| R | 扣费/退款无凭证 | 账本不可变 + 审计事件 |

### 1.6 记忆与经验库（memory / case_experiences / patterns）
| 威胁 | 分析 | 处置 |
|---|---|---|
| T | 模型自写经验污染决策 | propose_audit 中 pattern 默认 approved_by_human=false，仅人可置真（v1.8.2 测试）|
| E | 子 Agent 越权读他人记忆 | 子 Agent 记忆隔离；按 user_id 分区 |
| I | 个人 Context 泄露 | v1.8.5 脱敏 + v1.8.6 导出/删除权 |

### 1.7 审计事件流（audit_events / security_events）
| 威胁 | 分析 | 处置 |
|---|---|---|
| R | 操作无记录 | 双审计流，只追加 |
| T | 审计被改写 | 应用账号无 UPDATE/DELETE 权限（v1.8.6 收紧）|
| I | 审计中含敏感明文 | v1.8.5 落库/出端脱敏 |

### 1.8 Webhook（/v1/im/webhook）
F-04：验签真实、body 限 1MiB、幂等；残余：密钥轮换流程（v1.8.6 纳入运维手册）。

### 1.9 端侧音频（手机端）
F-09：架构承诺音频不出端，端侧只做唤醒/敏感过滤；v1.4.4 以"出端流量白名单审计报告"使承诺可验证。

## 2. 已落地修复的证据

- 全局请求体上限：`internal/security/security.go::MaxBody`，网关 wrap 接入 2MiB；
  - `TestV184MaxBody`：显式 ContentLength 超限→413；小 body→200；chunked 超大→handler 读不到超额字节且 Read 报错。
- 根路径不再回显凭据：`services/gateway/main.go` 根响应改为 `auth: "Bearer token required"`。
- 安全头/CORS/审计：v1.8.3 已落地（TestV183*）。
- SQL 注入面：逐一核实 auditview.go / moderation.go / support.go / message_pg.go，均参数化。

## 3. 网关中间件链（修复后的最终顺序，外→内）

```
MaxBody(2MiB) → Headers(安全头) → CORS(白名单) → Audit(落库) → IPLimit(100/200) → Bearer 认证 → 业务
```

## 4. 残余风险与后续计划

| 项 | 版本 | 动作 |
|---|---|---|
| 静态共享 token → 每用户 JWT/重放防护 | v1.9+ | 登录态 + 短期 access token |
| A2A 信封签名、横向越权渗透 | v1.8.7 | 自动渗透套件（SQLi/未授权/越权/XSS/路径穿越）|
| 出端脱敏 | v1.8.5 | 手机号/身份证/金额/地址 |
| 等保/个保、审计只追加、密钥轮换 | v1.8.6 | 合规文档 + 数据主体权利 |

## 5. 复现

```bash
go test ./internal/security/ -run TestV18 -count=1 -v   # 安全头/CORS/审计/MaxBody
curl -s -D- -o /dev/null http://localhost:8080/         # 根路径不再回显 token
# 413：构造超过 2MiB 的 POST body
head -c 3000000 /dev/zero | curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Authorization: Bearer im2a_demo_token' -H 'Content-Type: application/json' \
  --data-binary @- http://localhost:8080/v1/trust/
