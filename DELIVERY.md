# DELIVERY.md · 交付物清单

> 版本：v0.5.0
> 核对日期：2026-10-22
> 仓库：human-agent-im · AI 人机混合熟人社交 IM 平台

---

## 一、文档类（2 个）

| # | 文件 | 内容 | 状态 |
|---|---|---|---|
| 1 | `docs/技术设计基线-v1.0.md` | 产品形态 / 分层架构 / 框架选型 / SDK / 协议设计（A2A・MCP・内部协商・审批）/ 数据模型 / 客户端 UI / 运营客服运维 / Phase 0-3 开发计划 / 9 项风险约束 | ✅ 已交付 |
| 2 | `README.md` | 项目说明、核心机制对照表、快速启动命令、仓库结构、开发路线 | ✅ 已交付 |

---

## 二、协议契约（9 个 Protobuf，`proto/`）

| # | 文件 | 定义内容 |
|---|---|---|
| 3 | `common.proto` | 信任档位、付费计费模式、账本状态、错误码 |
| 4 | `auth.proto` | 登录 / 刷新 Token / 登出 |
| 5 | `user.proto` | 用户资料、设备管理 |
| 6 | `relation.proto` | 关系链、五档信任授权 / 撤销 |
| 7 | `msg.proto` | IM 消息业务封装（审批卡片 / 来意卡片 / Agent 摘要） |
| 8 | `approval.proto` | 审批单创建 / 通过 / 驳回 / 修改，8 态状态机 |
| 9 | `negotiation.proto` | Agent 协商 8 态 Task、意图枚举、gRPC 双向流 |
| 10 | `agent.proto` | Agent 注册表、Skill Runtime、权限门控 Check |
| 11 | `payment.proto` | 付费消息账本（占位 / 扣费 / 退款 / 日限） |

---

## 三、核心逻辑库（7 个 Go 包，`internal/`）

| # | 包 | 实现机制 |
|---|---|---|
| 12 | `config` | 服务通用配置加载 |
| 13 | `msgseq` | 消息序号 64 分片锁 + FNV 哈希（§2.3） |
| 14 | `trustzone` | 五档信任矩阵 + Capability 门控（§1.4/§3.3） |
| 15 | `approval` | 审批状态机 + 事件溯源 + 超时升级（§7.3） |
| 16 | `negotiation` | 协商 8 态状态机 + 轮次/token 熔断（§2.4） |
| 17 | `ledger` | 付费账本幂等扣费 + 单日上限（§1.3/§10-R2） |
| 18 | `audit` | 不可变审计日志（文件 / 内存双实现） |

---

## 四、服务（8 个，`services/`，自测全 PASS）

| # | 服务 | 核心职责 | 自测结果 |
|---|---|---|---|
| 19 | `agent-orchestrator` | 总 Agent 路由 + 权限门控 + 审批闭环 + 来意预审 + 审计轨迹（旗舰） | ✅ PASS |
| 20 | `approval-engine` | 审批工作流状态机 | ✅ PASS |
| 21 | `negotiation-svc` | Agent 间协商 | ✅ PASS |
| 22 | `relation-svc` | 五档信任矩阵门控 | ✅ PASS |
| 23 | `payment-svc` | 付费消息账本 | ✅ PASS |
| 24 | `msg-svc` | 消息序号生成 | ✅ PASS |
| 25 | `compliance-gate` | 合规出口护栏 | ✅ PASS |
| 26 | `gateway` | 统一 API 网关（v0.5.0 新增） | ✅ PASS |

---

## 五、客户端（Flutter 骨架，`client/app/`，12 个文件）

| # | 文件 | 说明 |
|---|---|---|
| 27 | `pubspec.yaml` | 工程配置（纯 Flutter SDK，无外部依赖） |
| 28 | `analysis_options.yaml` | Lint 规则 |
| 29 | `lib/main.dart` | 入口：审批态 / 团队 / 会话 三态导航 |
| 30-32 | `lib/models/`（approval_ticket / agent_status / stranger_intent） | 审批单 / Agent 状态 / 来意卡片模型 |
| 33-35 | `lib/screens/`（approval / overview / conversation） | 审批态、巡视态、Agent 摘要 IM |
| 36 | `lib/widgets/approval_card.dart` | 审批卡片（通过 / 修改 / 驳回） |
| 37 | `lib/services/api_client.dart` | API 客户端桩（Phase 1 对接 gRPC / 腾讯云 IM） |
| 38 | `assets/` | 占位资源 |

---

## 六、部署与工程配置（4 个）

| # | 文件 | 说明 |
|---|---|---|
| 39 | `deploy/docker-compose.yml` | postgres/redis/kafka/etcd/opensearch 一键启动 |
| 40 | `Makefile` | build/vet/selftest-all/proto/up/down/gateway |
| 41 | `go.mod` | Go 模块（stdlib-only，离线可编译） |
| 42 | `.gitignore` | 忽略规则 |

---

## 验证状态汇总

| 验证项 | 结果 | 方式 |
|---|---|---|
| `go build ./...` | ✅ 通过 | 实跑 |
| `go vet ./...` | ✅ 通过 | 实跑 |
| `go test -race ./...` | ✅ 通过 | 实跑 |
| 8 个服务自测 | ✅ 8/8 PASS | `make selftest-all` |
| Flutter 编译 | ⚠️ 未验证 | 本机无 Flutter SDK；`cd client/app && flutter create . && flutter run` |
| docker-compose 启动 | ⚠️ 未验证 | 本机无 Docker；`make up` |
| Protobuf 代码生成 | ⏸ 未执行 | 需安装 protoc；`make proto` |

---

## v0.5.0 新增（相对 v0.42）

- **统一 API 网关**：`services/gateway/`，合并 8 个服务到一个端口
- **Makefile 网关目标**：`make gateway PORT=8080`
- **README 网关说明**：快速启动加 2b 节

---

## 复现命令

```bash
cd /Users/fang/Doubao/chats/2026-10-21/new-chat/human-agent-im
export PATH="$HOME/sdk/go/bin:$PATH"
make gate                 # 全量关卡（build+vet+race+selftest）
make gateway PORT=8080    # 启动统一网关
```
