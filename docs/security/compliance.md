# IM2A 合规对齐与数据主体权利（v1.8.6）

> 对标《网络安全等级保护基本要求》（GB/T 22239-2019，等保 2.0）三级、《个人信息保护法》（PIPL）、《数据安全法》。
> 本文说明 IM2A 在数据主体权利、数据分类分级、安全审计、密钥管理上的**代码级实现**与运维要求。
> 相关：威胁模型见 `threat-model.md`、STRIDE 见 `stride.md`、数据脱敏见 `data-masking.md`。

---

## 1. 数据主体权利（PIPL §44–47）

个人信息主体对其个人信息享有**知情、决定、查阅、复制、可携带、更正、删除**等权利。IM2A 在 `internal/privacy` 与网关 `/v1/privacy/*` 提供真实落库实现，所有请求写入 `data_subject_requests`（migration 015）可审计。

| 权利 | 法律条款 | 接口 | 实现 |
|---|---|---|---|
| 查阅 / 复制 / 可携带 | §45 | `POST /v1/privacy/export?user_id=<id>` | 聚合 26 张归属表 + 付费账本，`json_agg` 导出，落 export 记录 |
| 删除（被遗忘权） | §47 | `POST /v1/privacy/delete?user_id=<id>` | 事务内物理删除派生数据 + 账户匿名化，落 delete 记录 |
| 处理记录查询 | §44/§55 | `GET /v1/privacy/requests?user_id=<id>` | 返回该用户全部 DSR 请求及处理结果 |

> 三个接口均要求**管理员 Token**（`IM2A_ADMIN_TOKEN`，普通用户 Token 无权访问），删除为高风险操作须走运营/客服审核流程后执行。

### 1.1 删除策略（三类处置）

| 数据类别 | 处置 | 依据 |
|---|---|---|
| 个人派生数据（记忆/画像/关系/设备/推送凭证/Agent 目录/审批草稿） | **物理删除** | 主体撤回同意 / 行使删除权 |
| 账户 `users` | **匿名化**（`phone_encrypted=NULL, status=2`，不物理删除） | 保证外发消息与账本引用一致性，匿名化后不可再识别 |
| 审计日志 `audit_events` / `security_events` | **法定保留** | 等保三级安全审计留存 ≥ 6 个月 |
| 付费账本 `paid_message_ledger` | **法定保留** | 财税凭证、交易纠纷举证 |
| 外发消息（涉及对方主体） | **保留** | 另一方个人信息不因单方删除而灭失 |

信任关系（`trust_levels`）按 `user_id` / `target_id` **双向解除**；客服工单 `support_tickets.user_id` 置 NULL 解除关联，工单内容保留供客服处理。

### 1.2 DSR 记录表（data_subject_requests）

| 列 | 类型 | 说明 |
|---|---|---|
| id | BIGSERIAL | 主键 |
| request_id | TEXT UNIQUE | 业务请求号（EXP-/DEL- + 日期 + 纳秒） |
| user_id | BIGINT | 内部用户 ID |
| kind | TEXT | export / delete |
| status | TEXT | pending / completed / rejected |
| result | JSONB | 导出聚合结果或删除行数报告 |
| reason | TEXT | 申请原因 |
| created_at / completed_at | TIMESTAMPTZ | 受理与完成时间 |

---

## 2. 数据分类分级

| 级别 | 数据 | 存储 | 保护措施 |
|---|---|---|---|
| L4 核心（敏感个人信息） | 手机号、身份证、银行卡、精确位置 | 字段级加密（`phone_encrypted`），出端脱敏（v1.8.5） | 加密存储 + 角色白名单 + 审计 |
| L3 重要（个人画像） | 记忆、事实、经验库、信任等级、审批记录 | PG + pgvector | 子 Agent 隔离、代码层门控、属主隔离 |
| L2 一般（业务数据） | 关系链、群组、设备、Agent 目录 | PG | 访问控制、鉴权 |
| L1 公开（审计/凭证） | 安全事件、账本（去标识）、审计流 | PG / Kafka / WORM | 只追加、不可篡改 |

---

## 3. 等保 2.0 三级控制项映射

| 控制域 | 三级要求 | IM2A 实现 |
|---|---|---|
| 安全通信网络 | 通信保密性、完整性 | 全链路 TLS 1.2+（HTTPS 443）；A2A/Agent 协商独立加密通道；HSTS |
| 安全区域边界 | 边界防护、访问控制、入侵防范 | 云安全组最小放行；网关 IP 限流（100/200）；MaxBody 2MiB；CORS 白名单 |
| 安全计算环境-身份鉴别 | 唯一标识、鉴别信息复杂度、失败处理 | Bearer Token + 独立管理员 Token；腾讯云 IM UserSig（HMAC-SHA256，过期刷新） |
| 安全计算环境-访问控制 | 最小权限、默认拒绝 | 五档信任矩阵 TrustZone **代码层 Capability 门控**（非提示词）；PreToolUse 强制 |
| 安全计算环境-安全审计 | 审计覆盖、记录保护、留存 ≥6 月 | 每请求/门控/扣费落 `security_events`/`audit_events`，只追加；WORM 对象存储 |
| 安全计算环境-数据完整性/保密性 | 传输/存储加密 | TLS + 字段级加密；PG 静态加密（云盘） |
| 安全计算环境-剩余信息保护 | 存储空间释放前清除 | 被遗忘权物理删除派生数据；DSR 匿名化 |
| 安全计算环境-个人信息保护 | 最小收集、授权、删除 | 端侧敏感前置过滤（原始音频不出端）；数据主体权利；出端脱敏 |
| 安全管理中心 | 集中审计/管控/日志 | 运营后台审计可视化、门控决策回放、统一安全事件表 |

---

## 4. 个人信息处理记录（PIPL §55）

| 项 | 内容 |
|---|---|
| 处理目的 | Agent 行政协调（预约/协商/审批）、熟人通信、陌生人付费触达、安全审计 |
| 处理方式 | 采集（端侧/IM）→ 加密传输 → 分层存储（端侧/PG）→ 子 Agent 隔离处理 → 审计 → 按 DSR 删除/导出 |
| 个人信息种类 | 账号标识、手机号（加密）、关系链、日程/记忆画像、付费流水、设备标识、审批记录 |
| 保存期限 | 账户存续期；记忆按 TTL 衰减；审计/账本 ≥6 个月（法定）；注销后派生数据删除、账户匿名化 |
| 敏感信息 | 手机号、身份证、银行卡、位置 → 加密 + 脱敏 + 最小授权 |
| 出境 | 默认境内云（腾讯云）；不出境原始音频 |
| 权利行使 | `/v1/privacy/export`、`/v1/privacy/delete`、`/v1/privacy/requests`（管理员受理） |

---

## 5. 密钥管理与轮换运维

| 密钥 | 用途 | 存放 | 轮换周期 |
|---|---|---|---|
| `GATEWAY_TOKEN` | 普通 API 鉴权 | systemd 环境变量（不入库不入仓） | 泄漏即轮换 |
| `IM2A_ADMIN_TOKEN` | 管理员 / DSR / 出端接口 | systemd 环境变量 | 90 天 |
| `TIM_SECRET_KEY` | 腾讯云 IM UserSig 签发 | systemd 环境变量 | 随控制台轮换 |
| `LLM_API_KEY` | 大模型调用 | systemd 环境变量 | 随服务商轮换 |
| PG 口令 | 数据库 | 部署密钥管理 | 90 天 |
| TLS 证书 | HTTPS | Let's Encrypt（certbot） | 自动续期（90 天） |

**轮换操作**：编辑 systemd `EnvironmentFile` → `systemctl restart im2a-gateway`；旧密钥在灰度窗口内双 Token 并存，切换后吊销。密钥**严禁**提交至 Git；CI 用 Secret 扫描（`run_secret_scanning`）兜底。

---

## 6. 验证证据（v1.8.6）

- 单元/落库断言：`internal/privacy/privacy_real_test.go :: TestV186ExportAndErase` —— 真实 PG 建两个用户、造记忆/关系/Agent 目录/设备/账本，断言导出聚合正确、删除后派生数据归零、账户匿名化、账本法定保留；**零残留**。
- 网关 E2E：无 Token → 401；`export` 返回 wm=1/users=1/ledger=1；`delete` 返回 wm=1/users_anon=1；`requests` 返回 export+delete 两条；删除后 `phone_encrypted=NULL,status=2`、账本保留=1；临时数据清理残留=0。
- 回归：`go build ./...`、`go vet ./...` 通过。

---

## 7. 待办（资质准备）

- 等保三级**测评**需委托具备资质的测评机构（本文档为技术对标与整改证据，不替代正式测评报告）。
- 个保影响评估（PIA）、隐私政策与用户协议文本由法务定稿。
- ICP/增值电信业务资质按实际运营主体申请。
