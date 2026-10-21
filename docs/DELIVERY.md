# IM2A 交付清单 v0.1

> 口径：**文件系统实测**（非记忆）。状态图例：✅ 本机实测通过 · ⚠️ 已提供但有边界 · ⏳ 计划中（未交付）。
> 验收命令：`node scripts/test.mjs`（本地单进程、无网络）、`node apps/im2a/server.mjs`（浏览器）。

## 1. 交付范围（v0.1）

| 类别 | 文件 | 内容 |
|---|---|---|
| 文档 | 5 | README · PLAN · ARCHITECTURE · DESIGN · DELIVERY（本文件） |
| 可运行应用 `apps/im2a/` | 9 | server.mjs（HTTP/路由/静态）· flow.mjs（治理世界主线）· agent-runtime.mjs · skills.mjs · llm.mjs · auth.mjs · store.mjs（SQLite）· public/index.html · .env.example |
| 治理模块 `services/` | 19 | orchestrator 7 模块 + 8 测试；payment 2 模块 + 2 测试；messaging 3 模块 + 3 测试；audit 1 模块（哈希链） |
| 测试 | 6 | store / llm / runtime / auth / tls（5 个）＋ scripts/test.mjs 运行器 |
| 工程 | 5 | package.json · LICENSE(MIT) · .gitignore · .github/workflows/ci.yml · 本清单 |

## 2. 核心能力与验证方式

| 能力 | 实现 | 验证 | 状态 |
|---|---|---|---|
| 熟人 IM | 会话列表/消息流/Agent 自动起草 | `tests/runtime.test.mjs` + 浏览器手测 | ✅ |
| 陌生人过滤 + 付费消息 | 预审→审批→扣费→查看→回复→结算→信任升级（种子场景） | 服务层状态机单测（payment）+ 浏览器手测 | ✅ |
| 审批工作流 | 9 态 + 事件溯源 + 超时升级 | `services/orchestrator/test/approval-engine.test.mjs` | ✅ |
| 五档信任 + 能力门控 | 矩阵 + 声明 + 信任双门控 | `trust-zone` / `capability-gate` 单测 | ✅ |
| Agent Runtime | 规划（LLM/离线）→门控→Skill→起草→护栏 | `tests/runtime.test.mjs` 9 项 | ✅ |
| 大模型接入 | OpenAI 兼容，DeepSeek-V4.1-Flash 默认 | `tests/llm.test.mjs` 7 项（注入 fetch） | ✅ |
| SQLite 持久化 | 会话/消息/审计链/序号 | `tests/store.test.mjs` 4 项 | ✅ |
| 登录鉴权 | scrypt + HMAC Cookie + 越权 403 | `tests/auth.test.mjs` 5 项 | ✅ |
| HTTPS/TLS | 证书可选，缺证书启动失败 | `tests/tls.test.mjs` 3 项 | ✅ |
| 审计可解释 | 哈希链 + 篡改检测 | store/audit 测试 | ✅ |
| **TaskLedger（v0.2.0）** | 只追加任务事件 + 状态机 + 幂等键 + 租约 + sweepExpired + fromEvents 重放 | `tests/task-ledger.test.mjs` 13 项 | ✅ |
| **Agent Worker / SSE / 多节点 relay** | 规划 v0.2 后续切片 | — | ⏳ |
| **PostgreSQL / OIDC / 硬件** | 规划 v0.3–v1.0 | — | ⏳ |

## 3. 验收记录（本机，2026-10-21）

```
node scripts/test.mjs   → 198 passed / 0 failed（服务层 + 应用层；无网络、无第三方依赖）
node apps/im2a/server.mjs → 浏览器可登录、发消息、审批、Agent 回复、审计链可见
已知未验证：无浏览器自动化 E2E（人工手测）；未做性能/压测；多进程/多机未实现
```

## 4. 诚实边界（不回避）

1. **单进程、单写者**：SQLite + 进程内 Agent Runtime；高并发/多机在 v0.2–v0.3。
2. **治理状态重启回种子**：消息/审计落库，但审批单/账本/信任矩阵仍是内存重建（v0.2 落库）。
3. **无浏览器 E2E**：UI 只有人工手测与静态检查，无 Playwright。
4. **无真实支付**：账本是演示账本，不接支付渠道。
5. **大模型未在 CI 实调**：测试注入 fetch；真实调用需 `DEEPSEEK_API_KEY`。
6. **无速率限制/多租户**：除 LLM 预算外无配额；登录无吊销表。

## 5. v0.2 重写验收清单（下一步，见 PLAN/ARCHITECTURE §6）

```
[ ] TaskLedger：append-only + 状态机 + 幂等键 + 租约，含失败/重放测试
[ ] Agent Worker：领取/心跳/崩溃回收，至少 2 个 worker 并发
[ ] SSE 事件流：断线重连（Last-Event-ID），不丢不重
[ ] 消息 bizSeq 保序 + 去重：并发写入测试
[ ] 兼容层：旧 API 行为不变；端到端（聊天/审批/付费）双路径通过
[ ] 文档同步：ARCHITECTURE 模块表状态更新；DELIVERY 记录实测证据
```

## 6. 如何复现

```powershell
cd IM2A
node scripts/test.mjs                 # 全部测试
$env:IM_AUTH='1'; $env:IM_SESSION_SECRET='local-demo'; node apps/im2a/server.mjs
# 浏览器 http://127.0.0.1:8797 ；登录口令见 README（仅本地 Demo）
```
