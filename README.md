# human-agent-im · AI 人机混合熟人社交 IM 平台

![CI](https://github.com/TwinsEarth/im2agent/actions/workflows/ci.yml/badge.svg)
![Compat](https://github.com/TwinsEarth/im2agent/actions/workflows/compat.yml/badge.svg)
![Release](https://github.com/TwinsEarth/im2agent/actions/workflows/release.yml/badge.svg)
![npm](https://img.shields.io/npm/v/@twinsearth%2Fim2agent?logo=npm&label=npm)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![Flutter](https://img.shields.io/badge/Flutter-SDK-02569B?logo=flutter&logoColor=white)
![Version](https://img.shields.io/badge/version-v2.0.2-blue)
![License](https://img.shields.io/badge/license-MIT-green)

## 我们提供的从来不是"又一个 Agent 平台"。

## 我们就干三件事：  
一个 Agent 能带走的资产  

一张 Agent 能进入的网络   

一套人以人为本的智能宪法

> **Slogan**：让每个人都拥有自己专属的 AI 秘书、助理与管家团队。
>
> 传统 IM 连接人，人类被海量信息奴役。IM2A 连接 Agent 网络，人类退居幕后，掌握最终裁决权。
> 这不仅是 IM，更是 AGI 时代个人生活方式的"数字平权"基础设施。

---
## 一、产品定位

| 维度 | 说明 |
|---|---|
| **个人目标** | 把日常杂碎、行政事务、繁琐沟通外包给 Agent 团队，人类专注自我提升、家庭情感、事业创造 |
| **社会愿景** | 机器承担基础运转与协调劳动，推动人类从"劳动者"向"消费者/指挥者"转换 |
| **终极使命** | 打破资源壁垒，让普通人以极低成本配备顶级私人法务、家庭医生、资深采购、行政助理 |

### 三条不可妥协原则

1. **付费永远发生在 Agent 预审之后**——付费不是买通路，是买已被预审过的有效触点
2. **信任边界是代码层 capability 门控**——不是提示词约束，模型无法绕过（`trustzone.Check`）
3. **"不上传原始音频"必须可验证**——端侧只做唤醒/意图粗分类/敏感过滤，原始音频不离开设备

---

## 二、总体架构

```
┌─────────────────────────────────────────────────────────────────────┐
│ L1 接入层      耳夹耳机/眼镜(BLE) · 手机 · PC · 手表(审批/摘要) · Web │
├─────────────────────────────────────────────────────────────────────┤
│ L2 边缘/端侧   端侧唤醒/意图粗分类 · 敏感前置过滤 · 离线审批缓存       │
│               原始音频不离开设备（可验证承诺）                        │
├─────────────────────────────────────────────────────────────────────┤
│ L3 IM 通信层   腾讯云 IM PaaS · WebSocket 长连接 · 多端同步 · 离线推送 │
├─────────────────────────────────────────────────────────────────────┤
│ L4 Agent 编排层（自研核心资产）                                       │
│   总Agent(管家) → 子Agent调度(日程/采购/法律/亲子/物业)               │
│   → 权限门控 TrustZone PreToolUse 代码层强制                         │
│   → 审批工作流引擎 PENDING→…→COMPLETED/FAILED                       │
│   → Skill Runtime(MCP) 沙箱+权限清单                                │
│   → 记忆分层(工作/情景/语义) · 子Agent隔离                           │
│   → 出口护栏 外发内容必过审                                          │
│   蒸馏闭环：审批/驳回/修改反馈 → Improvement Memory → 自动收紧阈值   │
├─────────────────────────────────────────────────────────────────────┤
│ L5 模型层      端侧小模型(唤醒/意图/过滤) ←→ 云端大模型(规划/协商)    │
│               智能路由：置信度≥0.6 端侧直接用，<0.6 转云端             │
├─────────────────────────────────────────────────────────────────────┤
│ L6 数据层      PostgreSQL(pgvector) · OpenSearch · Kafka · 时序库    │
│               对象存储 WORM 审计                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 三条关键流量分离（核心决策）

| 通道 | 机制 | 用途 |
|---|---|---|
| ① 人类 IM 通道 | 腾讯云 IM PaaS · WebSocket | 人与人情感交互、多端同步、离线推送 |
| ② Agent 协商通道 | 自研 gRPC 双向流 · 8 态 Task 状态机 | Agent 间高频协商，终止时仅注入一张审批卡片 |
| ③ 审批与审计事件流 | Kafka | 每次工具调用/门控/扣费写事件，可解释性 & 合规追溯 |

---

## 三、熟人五档信任矩阵

| 档位 | 自主执行 | 需审批 |
|---|---|---|
| L0 本人 | 全部 | — |
| L1 家庭 | 日程/采购/物业 | 大额/对外承诺 |
| L2 信任 | 预约/留言 | 第三方信息 |
| L3 已知 | 接收留言摘要 | 所有对外回复 |
| L4 陌生 | 仅结构化来意 | 付费后查看 |

---

## 四、陌生人付费注意力经济

```
① 发送即扣费 → 过滤批量垃圾
② 回复才扣费 → 高价值触点
账本不可变 · message_id 幂等 · 单日上限
```

### 付费消息状态机

```
pending（reserve 占位）
  ↓
charged（billingMode=1 发送即扣）
  或 delivered（billingMode=2 等待打开）
  ↓
opened（接收方打开）
  或 rejected（拒收 → 自动退款）
  或 expired（超时未打开 → 自动关闭/退款）
  或 auto_rejected（接收方偏好自动拒绝）
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

## 五、核心模块与版本演进

### v0.1-v0.5 基座（已完成）

| 版本 | 模块 | 说明 |
|---|---|---|
| v0.1 | 7 核心机制 | 消息分片锁/信任门控/审批状态机/协商熔断/幂等账本/出口护栏/编排闭环 |
| v0.2 | 协议契约 | 9 个 Protobuf + PG schema + MCP Skill Runtime + HTTP API |
| v0.5 | 统一网关 | 认证中间件 + 真实消息存储 + 限流 + 事件总线 |

### v0.6 记忆基座（Context 飞轮）

| 版本 | 功能 |
|---|---|
| v0.6 | 三层记忆（Working KV / Episodic 时间线 / Semantic 带置信度）+ 子 Agent 隔离 |
| v0.6.1 | TTL 惰性过期 + 决策闭环集成 |
| v0.6.2 | 记忆导出/导入 + 统计 |
| v0.6.3 | 置信度衰减 + 全局搜索 + 幂等 |
| v0.6.4 | Fact 强化 + 标签过滤 + GC |
| v0.6.5 | 事件流 + 快照回滚 |

### v0.7 蒸馏闭环（自进化）

| 版本 | 功能 |
|---|---|
| v0.7.0 | 连续 N 次负反馈或负反馈占比≥30% 自动收紧（只收紧不放宽） |
| v0.7.1 | 接入门控闭环 |
| v0.7.2 | 事件溯源与可解释性 |
| v0.7.3 | 规则参数 Config 化 |
| v0.7.4 | 用户级导出/导入 |
| v0.7.5 | 报表 + 记忆联动 |
| v0.7.6 | 审批卡蒸馏标注 + 批量重置 |

### v0.8 L5 模型层

| 版本 | 功能 |
|---|---|
| v0.8.0 | Provider 接口（端侧桩 + 云端兼容 + 混合路由） |
| v0.8.1 | 云端 OpenAI 兼容真实调用 |
| v0.8.2 | 智能路由（置信度≥0.6 端侧直接用） |
| v0.8.3 | 模型调用审计事件 |
| v0.8.4 | 多模型 fallback 链 |
| v0.8.5 | Per-agent 云端配额（防烧钱） |
| v0.8.6 | 9 个意图域分类扩展 |
| v0.8.7 | 热切换云端配置 |

### v0.9 陌生人付费经济（当前）

| 版本 | 功能 |
|---|---|
| v0.9.0 | 付费消息端到端闭环 |
| v0.9.1 | 用户自主定价 + 拒收退款 |
| v0.9.2 | 收件箱/已发列表/内容查看 |
| v0.9.3 | 付费统计 dashboard |
| v0.9.4 | 付费消息黑名单 |
| v0.9.5 | 消息过期（超时自动关闭） |
| v0.9.6 | 按状态过滤查询 |
| v0.9.7 | 批量导出（对账用） |
| v0.9.8 | 接收方偏好（自动接受/自动拒绝） |

### v1.0 生产化层（已完成）

| 版本 | 功能 |
|---|---|
| v1.0.x | 认证中间件 + 真实 PG 消息存储 + 全局限流 + 优雅退出 + 配置热加载 |

### v1.1 实时通道（已完成）

| 版本 | 功能 |
|---|---|
| v1.1.x | WebSocket 长连接通道：在线状态、心跳、多端连接管理、消息实时下发 |

### v1.2 腾讯云 IM 双通道物理隔离（已完成）

| 版本 | 功能 |
|---|---|
| v1.2.0 | 人类消息走腾讯云 IM；Agent 消息走自研协商流，物理隔离 |
| v1.2.1 | UserSig 签发服务（签名 + 过期刷新） |
| v1.2.2 | Webhook 回调接收（消息/已读/群事件） |
| v1.2.3 | 消息路由分流：人类→IM、Agent→协商流、审批→审计事件流 |
| v1.2.4 | 群聊支持：关系链/群成员/消息广播 |

### v1.3 Flutter 完整客户端（已完成）

| 版本 | 功能 |
|---|---|
| v1.3.0 | 审批态卡片流 + 巡视态日志 + 会话态 IM 三态全实现 |
| v1.3.1 | 审批卡片交互：通过/修改/驳回/超时升级 |
| v1.3.2 | 巡视态：Agent 工作轨迹、协商时间线、审计事件流 |
| v1.3.3 | 会话态：Agent 摘要层 IM + 人类消息分流 |
| v1.3.4 | api_client 真实对接网关全部路由 |
| v1.3.5 | Flutter Desktop（PC 窗口 + 多端同步） |

### v1.4 端侧智能 + BLE（已完成）

| 版本 | 功能 |
|---|---|
| v1.4.0 | 端侧唤醒词、意图粗分类、敏感前置过滤 |
| v1.4.1 | 端侧意图分类服务（小模型/规则可插拔） |
| v1.4.2 | 敏感词端侧拦截，不出端 |
| v1.4.3 | BLE 网关协议（耳机/眼镜/指环接入） |
| v1.4.4 | "音频不出端"出端流量白名单审计报告 |

### v1.5 手表端 + 多端同步（已完成）

| 版本 | 功能 |
|---|---|
| v1.5.0 | 手表一键审批、离线缓存、冲突合并 |
| v1.5.1 | 手表 App：通知/一键通过驳回/紧急升级 |
| v1.5.2 | 离线审批缓存（断网可审批，联网同步） |
| v1.5.3 | 多端状态同步（审批/消息/关系一致） |

### v1.6 工程化与五方发布（已完成）

| 版本 | 功能 |
|---|---|
| v1.6.x | 全量集成门禁 `make gate`、CI、npm 包、Compat 兼容性门禁、Release workflow 五方发布 |

### v1.7 运营后台（已完成）

| 版本 | 功能 |
|---|---|
| v1.7.0 | Webhook 公网 HTTPS 投递（Let's Encrypt 443） |
| v1.7.1 | 管理仪表盘（用户/Agent/付费/调用量实时面板） |
| v1.7.2 | 管理仪表盘细化（趋势/付费流水/Agent 明细） |
| v1.7.3 | 客服工单：用户反馈→工单→处理闭环 |
| v1.7.4 | 审计可视化：事件流时间线 + 门控决策回放 |
| v1.7.5 | 内容审核：自动规则 + 人工审核队列 |

### v1.8 Agent 网络与安全（已完成）

| 版本 | 功能 |
|---|---|
| v1.8.0 | A2A 统一信封：Agent 间协商通道（msg_id 幂等 + 轮询收件箱 + deliver） |
| v1.8.1 | Agent 通讯录一等公民：对外别名/AttentionLevel/tags/月预算钱包，信任档位继承自 Agent 背后的人 |
| v1.8.2 | 双层经验库：案例层写入仅限人类审批决策 + 模式层须人类批准才生效；Propose-Commit-Audit 三分离（最终审计裁决归人类） |
| — | 真实 APNS 推送通道（.p8 JWT，官方 apns2 库，待 Apple 凭据真机投递） |
| v1.8.3 | 安全合规基线：安全中间件 + 审计落库（security_events）+ 威胁模型框架 |
| v1.8.4 | STRIDE 威胁建模与修复：强认证、IP 限流、安全响应头、注入防护、越权拦截 |
| v1.8.5 | 数据脱敏：手机号/金额/地址出端脱敏，审计保留原值（可证明） |
| v1.8.6 | 等保/个保对齐：合规文档 + 数据主体权利（访问/更正/删除/导出/拒绝自动处理） |
| v1.8.7 | 渗透测试套件：8 攻击向量真实打公网网关 + 修复闭环 + 渗透报告 |
| v1.8.8 | 版本收口 + 五方发布；修复 `-race` 三个并行缺陷 |

### v1.9 身份与数据主权（已完成）

| 版本 | 功能 |
|---|---|
| v1.9.0 | JWT 身份地基：stdlib HMAC-SHA256 签发/校验、HS256、恒定时间比较、登录 upsert、/me |
| v1.9.1 | 多用户隔离收紧：Authenticate 统一中间件，横向越权一律 403 |
| v1.9.2 | Context 端到端信封加密：AES-256-GCM、随机 nonce、GCM 认证、明文不出端 |
| v1.9.3 | Context 可移植导出/导入加密包 |
| v1.9.4 | 应用层真实余弦向量检索（256 维 bigram 哈希 + L2 归一化） |
| v1.9.5 | 私有化离线全栈：多阶段 Dockerfile + GA 手册 |
| v1.9.7 | 全链路压测：1000 并发 ×5000 真实写 PG，3911 QPS、p99 690ms、零失败 |
| v1.9.8 | 灰度发布门控：FNV 一致性分桶，按 user/Agent 百分比放量 |
| v1.9.9 | Prometheus 监控：真实计数 + /metrics + /healthz admin token 鉴权 |

### v2.0 微信 Bot 体系（稳定版）

| 版本 | 功能 |
|---|---|
| v2.0.0 | Bot 组绑定：一个 wxid 绑一组、组内 ios/android/windows 三端，DB 唯一索引强制 |
| v2.0.1 | wxid 持久识别（首次分配 uid，后续复用）；Agent 衍生额度（1 主/N 管理/N 子）；VIP free/basic ¥99/pro ¥299/enterprise ¥999 |
| v2.0.2 | 公私钥身份：Ed25519 密钥对，私钥 AES-256-GCM 加密落库 |
| v2.0.4 | 微信 Bot 协议契约：消息信封 / 卡片按钮回传 / 幂等键 / Ed25519 签名 |
| v2.0.5 | Bot API 内核：时间漂移 ±300s、验签、幂等重放、类型分发 |
| v2.0.6 | Bot SDK Client：自动构造信封/签名/幂等/POST |
| v2.0.7 | Agent 间通信全链路：identify→occupy→directory upsert→a2a.send |
| v2.0.8 | 临时协商群：创建/加入/发言/历史/销毁（软 closed + 物理清消息成员） |
| v2.0.9 | Bot↔Bot 路由：双方必须已绑定、禁止自发自、收件箱拉取 |
| v2.1.0 | 个人 Agent 工作汇报：聚合当日活动生成草稿、人工审批、按 user+date 去重 |
| v2.1.1 | 稳定版：18 个 v2 网关端点公网可达（https://geefish.cn），全量门禁通过，五方发布 |
---

## 六、HTTP API 端点清单

### 统一网关（`/v1/` 前缀）

| 模块 | 方法 | 路径 | 说明 |
|---|---|---|---|
| **信任** | POST | `/v1/trust/grant` | 授权 `{userId,targetId,level 0..4}` |
| | GET | `/v1/trust/get?user=&target=` | 查询信任档位 |
| | POST | `/v1/trust/check` | 门控检查（ALLOW/REQUIRE_APPROVAL/DENY） |
| **Agent** | POST | `/v1/agents/register` | 注册 Agent |
| | POST | `/v1/agents/capabilities` | 注册 Capability |
| **审批** | POST | `/v1/approvals` | 创建审批单 |
| | POST | `/v1/approvals/{id}/approve` `/reject` `/modify` | 状态迁移 |
| | GET | `/v1/approvals/pending?user=` | 待办列表 |
| **协商** | POST | `/v1/negotiations` | 建立协商会话 |
| | POST | `/v1/negotiations/{id}/turns` | 提交一轮 |
| **消息** | POST | `/v1/msg/seq` | `{userId}` → `{seq}` |
| | POST | `/v1/msg/seq/batch` | `{userId,n}` → `{seqs}` |
| **合规** | POST | `/v1/compliance/check` | `{messageId,content}` |
| **编排** | POST | `/v1/gate/check` | 编排器门控 |
| | POST | `/v1/stranger/intent` | 陌生人来意预审 |
| | POST | `/v1/orchestrator/tickets/{id}/decide` | 审批决策 |
| **审计** | GET | `/v1/audit` | 审计轨迹回放 |
| **MCP** | POST | `/mcp` | MCP JSON-RPC |
| | GET | `/mcp/skills` | Skill 清单 |
| **记忆** | POST | `/v1/memory/master` | 注册总 Agent |
| | POST | `/v1/memory/fact` | 写语义记忆 |
| | GET | `/v1/memory/cross/facts` | 跨子 Agent 读取 |
| **蒸馏** | GET | `/v1/distill/stats` | 蒸馏统计 |
| | POST | `/v1/distill/feedback` | 手动反馈 |
| **模型** | POST | `/v1/model/chat` | 智能路由对话 |
| | GET | `/v1/model/info` | 模型信息 |
| | GET | `/v1/model/stats` | 调用统计 |
| | GET | `/v1/model/quota` | 云端配额 |
| | POST | `/v1/model/cloud` | 热切换云端 |
| **付费消息** | POST | `/v1/paidflow/send` | 发送付费消息 |
| | POST | `/v1/paidflow/open` | 打开消息 |
| | POST | `/v1/paidflow/price` | 设置最低定价 |
| | POST | `/v1/paidflow/reject` | 拒收并退款 |
| | GET | `/v1/paidflow/inbox?receiverId=&status=` | 收件箱 |
| | GET | `/v1/paidflow/outbox?senderId=&status=` | 已发列表 |
| | GET | `/v1/paidflow/content?messageId=` | 查看内容 |
| | GET | `/v1/paidflow/stats?userId=` | 统计 dashboard |
| | POST | `/v1/paidflow/block` | 拉黑发送方 |
| | POST | `/v1/paidflow/unblock` | 解除拉黑 |
| | POST | `/v1/paidflow/expire` | 过期清理 |
| | GET | `/v1/paidflow/export?userId=&role=` | 批量导出 |
| | POST | `/v1/paidflow/preferences` | 接收方偏好 |

---

## 七、快速开始

前置：macOS/Linux，Go ≥ 1.21。本仓库 Go 代码 **stdlib-only**，离线可编译。

```bash
# 全量关卡：build + vet + go test -race + 7 服务自测
make gate

# 启动统一网关
make gateway PORT=8080
curl -s localhost:8080/healthz

# 付费消息示例
curl -s -X POST localhost:8080/v1/paidflow/send \
  -d '{"senderId":100,"receiverId":200,"content":"hello","billingMode":1,"amountCents":100}'

# 模型对话（端侧桩，无 key 自动回退）
curl -s -X POST localhost:8080/v1/model/chat \
  -d '{"messages":[{"role":"user","content":"今天天气怎么样"}]}'

# Flutter 客户端
cd client/app && flutter create . && flutter run
```

环境变量：
- `GATEWAY_TOKEN`：Bearer 认证（空=跳过）
- `PG_DSN`/`PG_DRIVER`：PostgreSQL 连接（空=内存存储）
- `LLM_BASE_URL`/`LLM_API_KEY`/`LLM_MODEL`：云端模型（空=回退端侧桩）

---

## 八、仓库结构

```
human-agent-im/
├── docs/                        # 技术文档
│   ├── 技术设计基线-v1.0.md     # 架构/协议/开发计划
│   ├── 技术设计基线-v2.0-后续版本规划.md
│   └── 版本路线图-v0.6-v1.0.html
├── proto/                       # 9 个 Protobuf 协议契约
├── internal/                    # 核心逻辑库
│   ├── msgseq/  trustzone/  approval/  negotiation/  ledger/
│   ├── audit/   config/     id/       store/       skillruntime/
│   ├── compliance/           # 出口护栏
│   ├── memory/                # 三层记忆（v0.6）
│   ├── distill/               # 蒸馏闭环（v0.7）
│   ├── model/                 # 模型抽象层（v0.8）
│   └── api/                   # HTTP handler + bootstrap
├── services/                   # 8 个服务入口
│   ├── gateway/               # 统一网关（v0.5）
│   ├── agent-orchestrator/    # 编排器（核心）
│   ├── approval-engine/
│   ├── negotiation-svc/
│   ├── relation-svc/
│   ├── payment-svc/
│   ├── msg-svc/
│   └── compliance-gate/
├── deploy/
│   ├── docker-compose.yml      # postgres/redis/kafka/etcd/opensearch
│   └── migrations/001_init.sql
├── client/app/                  # Flutter 客户端
├── .github/workflows/ci.yml    # GitHub Actions
├── Makefile
└── go.mod
```

---

## 九、开发路线（v0.6 → v1.0）

| 里程碑 | 版本 | 核心交付 |
|---|---|---|
| **M1 活大脑** | v0.6-v0.8 | 记忆基座 + 蒸馏闭环 + 模型抽象层 |
| **M2 闭环经济** | v0.9-v0.10 | 陌生人付费（当前）+ 协商双向流实跑 |
| **M3 生产化** | v0.11-v0.12 | PG+向量+Kafka 生产化 + 腾讯云 IM 接入 |
| **M4 全端** | v0.13-v0.15 | Flutter 全端（手机/PC/手表/Web） |
| **M5 GA** | v1.0 | 本地加密 + Context 可导出 + 正式发布 |

---

## 十、护城河

| 壁垒 | 说明 |
|---|---|
| **Context 飞轮** | 世界上没有 Personal Agent，只有 Context + Agent。积累的人类纠错记录、授权边界、办事习惯是核心资产，迁移成本极高 |
| **技术领先** | 大模型泛化能力 + 代码级零信任网络，攻克 Agent 越权、API 安全调用、云端/本地记忆分层 |
| **关系网网络效应** | 一旦熟人、同事、家人都在使用，他们的 Agent 已建立高频协作默契（如管家知道对方助理周四下午只接受商务会谈） |

---

**已交付（稳定版）**：在 v1.8.7 三条流量分离基础上，v1.9 补齐 JWT 身份、多用户隔离、Context 端到端 AES-256-GCM 加密、向量检索、压测（3911 QPS）、灰度门控与 Prometheus 监控；v2.0–v2.1 全量落地微信 Bot 体系（组绑定、wxid 持久识别、VIP 额度、Ed25519 公私钥、签名协议、A2A 通信、临时群、Bot 路由、工作汇报），18 个 v2 网关端点公网实测可达。

**工程质量（稳定版收口）**：全量 `go test ./...`（注入真实 PG_DSN）37 包全 ok、0 FAIL、0 panic；`make gate`（build/vet/`-race`/7 服务自测）全 PASS；8 张 v2 表跑测前后行数完全一致（零残留）；compat 跨平台矩阵 6/6 通过；GitHub Release 已上传 linux/darwin/windows × amd64/arm64 二进制 + sha256；npm `@twinsearth/im2agent`（core CLI）已发布为 latest。

---

## 十一、第三方组件与归属：OpenClaw（子插件）

本仓库以**独立子插件**的形式集成了 **OpenClaw**：

| 项 | 值 |
|---|---|
| 项目 | OpenClaw（<https://github.com/openclaw/openclaw>） |
| 版本 | **2026.10.7** |
| 许可证 | **MIT** |
| 版权 | `Copyright (c) 2026 OpenClaw Foundation` |
| 许可证原文 | [`THIRD_PARTY_LICENSES/OpenClaw-MIT.txt`](THIRD_PARTY_LICENSES/OpenClaw-MIT.txt)（逐字复制） |
| 完整归属声明 | [`ATTRIBUTION.md`](ATTRIBUTION.md) ｜ [`NOTICE`](NOTICE) |

**微信通道的前置依赖（重要）**：OpenClaw **内置通道不含微信**。微信个人号通道是外部社区插件
**`openclaw-weixin@3.3.1`**（MIT，`Copyright (c) 2026 Tencent` + `Copyright (c) 2026 Contributors to
NewFuture/openclaw-weixin`，基于腾讯 iLink bot API），许可证原文见
[`THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt`](THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt)。
部署/启用微信前必须安装：

```bash
openclaw plugins install openclaw-weixin --accept-capabilities --acknowledge-install-policy-warning --force
openclaw channels list --all   # 应显示 Weixin 已安装
```

子插件会在启动时探测该通道是否可用；**未安装时只进入 `degraded`，不会谎报微信能力**。

**集成了什么**：微信个人号通道（扫码登录 `openclaw channels login`、消息投递 `openclaw message send`）、
账号文件契约（`$HOME/.openclaw/openclaw-weixin/accounts/*.json`）、网关状态探测（`openclaw gateway status`）、
以及 OpenClaw agent 侧调用的 MCP 工具 `im2a_route`。

**没有做什么**：**未复制、未内联、未 vendor 任何 OpenClaw 源代码**。适配层（`internal/plugin/openclaw`）
为独立实现，只调用未修改的上游发行版。为互操作而沿用的命令名/字段名/文件结构/工具名属派生契约，
已在代码中以 `// 契约来源：OpenClaw <cli/path>` 标注。

**运行时公示**（部署后可直接验证）：

```bash
curl -sS http://127.0.0.1:18080/v1/about/attribution        # JSON
curl -sS "http://127.0.0.1:18080/v1/about/attribution?format=text"
curl -sS http://127.0.0.1:18080/v1/plugins                 # 插件清单（含 attribution）
bash scripts/attribution-check.sh --runtime                # 合规自检
```

启动时网关会打印归属横幅：

```
IM2A gateway <ver> — bundled plugin: OpenClaw (<ver>) MIT, Copyright (c) 2026 OpenClaw Foundation
  attribution: /v1/about/attribution | upstream: https://github.com/openclaw/openclaw
```

**配置项**（均为可选，缺省自动探测）：

| 环境变量 | 说明 | 默认 |
|---|---|---|
| `OPENCLAW_ENABLED` | 是否启用子插件 | 自动（探测到可执行文件即启用） |
| `OPENCLAW_BIN` | openclaw 可执行文件路径 | `PATH` 查找 → 已知安装位置 → 旧版绝对路径兼容 |
| `OPENCLAW_HOME` | 上游 HOME（用于发现 token 与账号文件） | 当前用户 HOME |
| `OPENCLAW_CHANNEL` | 通道名 | `openclaw-weixin` |
| `OPENCLAW_ACCOUNTS_DIR` | 账号文件目录 | `<HOME>/.openclaw/<CHANNEL>/accounts` |

**立场**：本项目与 OpenClaw Foundation **无隶属、无赞助、无官方背书**关系；"OpenClaw" 仅在上游署名与
来源指明的必要范围内使用。上游缺失时子插件进入 `degraded` 状态，**不影响 IM2A 其余功能**。

### 十一.2 DeepSeek-Bot（DS Bot）· 源码级移植

与前两者**性质不同**：这里不是运行时集成，而是**把上游的一小部分逻辑用 Go 重写**（Apache-2.0 意义上的衍生作品）。

| 项 | 值 |
|---|---|
| 项目 | DeepSeek Bot（DS Bot），`ds-bot@0.2.1`（<https://github.com/Fei2411/DeepSeek-Bot>） |
| 许可证 | **Apache-2.0**（`Copyright 2026 Fei2411 and the DS Bot contributors`） |
| 许可证 / NOTICE | [`DeepSeek-Bot-LICENSE.txt`](THIRD_PARTY_LICENSES/DeepSeek-Bot-LICENSE.txt) ｜ [`DeepSeek-Bot-NOTICE.txt`](THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt)（**NOTICE 逐字保留**） |
| 落点 | `internal/plugin/dsbot`（package `dsbotplugin`），插件 ID `dsbot` |

**移植了什么**（修改声明见 [`ATTRIBUTION.md`](ATTRIBUTION.md) §3.2）：

- `ds-bot/web-search.js` 的 provider 抽象与结果解析 → `search.go`（上游 `AGENTS.md` 明示该文件"独立于 Bot 团队代码"）
- 长消息切分策略 → `chunk.go`（纯函数 `Split(text, limit)`，按 rune 边界，保护 URL 与超长词）
- Bot 名册/权限语义 → `roster.go`（**只读 JSON 规格**，不执行任何 Bot）

**没有移植**：`ds-bot/src/host/**`（深度耦合 DSH 注入点）、`client.js`（1.08 MB 构建产物）、
`app/`（Electron 壳）、`cordis.patch.yml`（DSH bundle patch）。**本机不运行 DSH。**

**配置项**：

| 环境变量 | 说明 | 默认 |
|---|---|---|
| `DSBOT_ENABLED` | 是否启用子插件 | `1` |
| `DSBOT_ENDPOINT` | 可选：活的 DS Bot/DSH HTTP 基址；配置且健康时才声明 `dsbot.bridge` 能力 | 空 |
| `DSBOT_TIMEOUT_MS` | 桥接探测/调用超时 | `3000` |

**为什么它对我们有用**：IM2A 原先没有 Web 搜索能力，且微信出站对超长文本缺少一致的切分策略；
这两块正好补上。上游是"把 DSH 变成一群长期存活的 Bot"，其 host 实现与 DSH 强绑定，
**能拿走的是算法与语义，不是代码**。

### 十一.3 端侧出端白名单（fail-closed）

`/v1/edge/pipeline` 的端侧出端裁决使用 `internal/edge.EgressGuard`。**默认白名单为空 = fail-closed**：
未显式配置时任何出端请求都会被拒绝（`egress_allowed=false`），不会静默放行。源码中**不包含任何
环境/生产地址**——地址属于部署配置，由部署方显式提供：

| 环境变量 | 说明 | 默认 |
|---|---|---|
| `IM2A_EGRESS_ALLOW` | 允许出端的 host 列表，逗号分隔（不含 scheme），如 `api.example.com,localhost` | 空（fail-closed：全部拒绝） |
| `IM2A_EDGE_EGRESS_ENDPOINT` | 待裁决端点（只用于构造 body/审计，不发起网络请求）；未设置时由白名单第一个 host 派生 `https://<host>/v1/intent` | 空 |

**格式校验**（不静默接受垃圾配置）：逗号分隔的**裸 host**（如 `api.example.com`、`127.0.0.1`）；
首尾空白 trim、空项忽略、大小写不敏感、重复项去重；带 scheme（`https://`）、路径（`/`）、端口（`:`）
或非法字符的项会被**丢弃并在启动日志打印 WARN**，绝不进入放行集合。若最终没有任何合法项，则等价于
未配置 → **fail-closed 全部拒绝**。

> ⚠️ **行为变更（升级必读）**：本项修复前，白名单默认值里含一个真实地址，且旧 `EgressGuard` 把
> 空白名单当作"不限制"——即**未配置 = 全放行**。现在改为**默认空 + fail-closed**：未显式配置
> `IM2A_EGRESS_ALLOW` 的部署升级后，`/v1/edge/pipeline` 的出端裁决会从"允许"变为"拒绝"。
> 这是有意的安全修复（原行为与"原始音频不出端"承诺相反），不是故障。

**部署要求**：必须显式设置 `IM2A_EGRESS_ALLOW`，否则端侧出端一律被拒——这是有意的安全默认值
（"忘了配置"不能变成"全部放行"），不是故障。
