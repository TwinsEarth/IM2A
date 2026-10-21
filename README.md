# IM2A

> **IM2A = IM to Agent。** 一个开源的人机混合熟人社交 IM：人、人的 Agent 团队、陌生人的 Agent 之间用同一套协议沟通；
> 人只做最终审批与情感沟通，Agent 负责协调、过滤、比价、预约与执行。

- **独立项目**：不依赖任何外部编排框架；单进程 Node（≥22）+ SQLite 即可运行。
- **架构思想参考 w2m 的工程实践**（任务账本 / 消息序号 / 内容寻址数据面 / 代码层门控 / 不可变审计），
  但代码、协议、仓库与 w2m 完全分离，不共享包、不共享服务、不共享部署。
- **可运行优先**：开箱即可在浏览器里聊天、审批、看 Agent 汇报；配了 LLM key 后由真实模型起草回复。

## 快速开始

```powershell
git clone https://github.com/TwinsEarth/IM2A.git
cd IM2A
node apps/im2a/server.mjs        # http://127.0.0.1:8797（默认离线模板模式，无需密钥）
node scripts/test.mjs            # 198 项测试，全部本地、无网络、无第三方依赖
```

启用登录 + SQLite + 真实大模型（可选）：

```powershell
$env:IM_AUTH = '1'
$env:IM_SESSION_SECRET = '<长随机串>'
$env:DEEPSEEK_API_KEY = 'sk-...'          # 缺省 → 离线模板规划/回复
$env:DEEPSEEK_MODEL = 'deepseek-v4.1-flash'
node apps/im2a/server.mjs
```

演示账号（本机 Demo 默认口令，生产必须改）：林总 `demo-lin-2026` · 周先生 `demo-zhou-2026` · 林母 `demo-mom-2026`

## 它演示什么

| 场景 | 体验 |
|---|---|
| 熟人 IM | 与家人/同事的会话，Agent 自动起草跟进回复 |
| 陌生人过滤 | 陌生人消息默认不可直达；Agent 预审 + 来意卡片 + 审批通过后才进入会话（付费模式可配） |
| 审批闭环 | 通过 → 扣费 → 查看 → 回复 → 结算 → 信任档位升级；驳回则对方不被扣费 |
| Agent Runtime | 规划（LLM 或离线规则）→ 权限门控 → Skill 执行 → 起草 → 出口护栏；模型不能越权 |
| 可解释审计 | 每个门控/审批/Agent 回复/扣费都有哈希链记录，可回放 |

## 仓库结构（v0.1）

```
apps/im2a/          可运行的 IM 服务：HTTP API + 浏览器 UI + Agent Runtime + LLM + SQLite + 鉴权 + TLS
services/           冻结的治理语义（不含网络）：信任矩阵/能力门控/审批/协商/记忆/出口护栏/账本/消息封装
tests/              端到端与模块测试（本地单进程，无网络依赖）
docs/               计划 / 技术方案 / 设计方案 / 交付清单
scripts/test.mjs    零依赖测试运行器（单进程 import，规避 Windows 子进程限制）
```

详细文档：[计划](docs/PLAN.md) · [技术方案](docs/ARCHITECTURE.md) · [设计方案](docs/DESIGN.md) · [交付清单](docs/DELIVERY.md) · [开源生态调研](docs/LANDSCAPE.md)

## 状态

- ✅ v0.1：可运行 Demo（聊天/审批/付费消息/Agent Runtime/LLM 接入/登录/SQLite/TLS）+ 198 项本地测试
- ✅ v0.2.0：TaskLedger 落地（只追加事件 + 状态机 + 幂等键 + 租约 + 过期回收 + 重放），13 项契约测试
- ⏳ v0.2 后续：Agent Worker 接入 / SSE 事件流 / 消息 bizSeq 保序去重（按 [技术方案 §6](docs/ARCHITECTURE.md)）
- ⏳ v0.3：多节点 relay、内容寻址 blob、可插拔模型网关
- ⏳ v1.0：端侧硬件接入（不做首发承诺）、生产 PostgreSQL 存储、审计对外导出

## 贡献

- 先读 [设计方案](docs/DESIGN.md) 的「不可妥协的三条原则」与 [交付清单](docs/DELIVERY.md) 的验证边界；
- `node scripts/test.mjs` 必须全绿；新增模块请附最小复现测试；
- 安全相关改动（信任/门控/扣费/审计）需要设计说明，不接受只改实现。

## License

MIT
