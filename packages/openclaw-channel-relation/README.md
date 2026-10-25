# @im2a/openclaw-channel-relation

IM2A 的 **平台版本（edition）之一**：把 IM2A 的三样核心能力以 **OpenClaw 频道插件**形态交付，可经 ClawHub / npm 分发。

> **定位**：本包是 IM2A 的产品线延伸（类比 iOS / Android / Windows 版本），**不是**完整 IM2A，也不替代主产品。
> 核心（core）版本单一源在 [`internal/version/version.go`](../../internal/version/version.go)；
> 本 edition 的版本在仓库根 [`editions.json`](../../editions.json) 声明（当前 `0.1.0`），与 core 独立演进。

## 三样核心能力

| 能力 | 说明 | 源码 |
|---|---|---|
| **关系档位门控** | 5 档信任（L0 self … L4 stranger）× 7 领域（schedule/family/purchase/workmail/stranger/finance/external），三态 `auto_execute / requires_approval / forbidden`；五步判定顺序：白名单 → 档位下限 → 领域矩阵 → 金额阈值 → 日预算 | `src/relation.ts` |
| **付费注意力状态机** | 付费消息状态 `charged / delivered / rejected / expired` + 决策态 `blocked / below_price / auto_rejected`；注意力级别 0–3；billing mode 1\|2；自动拒收阈值、黑白名单 | `src/paid-attention.ts` |
| **三态 UI** | 审批态（审批卡片流）/ 巡视态（时间线日志）/ 会话态（消息视图） | `src/ui.ts` |

协议规范与公开测试向量在仓库根：

- 规范：[`spec/relation-gating.md`](../../spec/relation-gating.md)（实现无关，normative 顺序与不变量 I1–I7）
- 向量：[`spec/vectors/relation-gating.v1.json`](../../spec/vectors/relation-gating.v1.json)（115 条，覆盖矩阵 35 格 / 顺序 14 / 单调性 35 / 不变量 14 / 边界 8 / 未知输入 7 / 纯度 2）
- 参考一致性套件（Go）：[`cmd/relation-conformance`](../../cmd/relation-conformance)

## 安装与使用

```bash
# 在 OpenClaw 宿主中安装（发布后）
openclaw plugins install @im2a/openclaw-channel-relation

# 本地开发：在仓库根安装本包目录（仓库根没有 package.json）
npm install -g ./packages/openclaw-channel-relation
# 或让 OpenClaw 以链接方式安装本目录
openclaw plugins install --link ./packages/openclaw-channel-relation
```

宿主版本要求：`openclaw >= 2026.6.1`；Node **>= 22.18.0**。
测试与本地调试可直接运行 `src/*.ts`（原生 TypeScript type stripping）；**宿主运行时**读取的是发布工具链产出的 `dist/src/index.js`，因此发布前必须产出 `dist/`。

## 验证（本包自带，零依赖）

```bash
npm test                 # build 校验 + 5 组测试（edition / relation / paid-attention / ui / conformance）
npm run conformance      # 只跑公开向量（逐条比对 allowed/requiresApproval/reasonCode，任何不一致 exit 1）
npm run pack:check       # 打包内容与元数据检查（无 npm 时明确 SKIP 并说明原因）
```

> `dist/`：本仓库不做 bundling（`scripts/build.mjs` 只做导入与导出自检）；发布流程由发布工具链生成 `dist/src/index.js`。
> `files` 中保留 `dist/`，本地未构建时 `pack:check` **不视为失败**（它只检查白名单/体积/必需元数据；npm 不可用时明确 SKIP，SKIP ≠ PASS）。

## 配置

插件 manifest（`openclaw.plugin.json`）声明 channel `im2a-relation` 及配置 schema。**实际 channel 配置键**（与 `CHANNEL_CONFIG_SCHEMA` 一致）：

| 配置 | 说明 |
|---|---|
| `defaultTrustLevel` | 新会话缺省信任档（0–4；缺省 4 = stranger，最保守） |
| `minPriceCents` | 付费注意力最低价格（分） |
| `autoRejectAboveCents` | 超过该金额自动拒收（0 = 不启用） |
| `blockedSenders` | 黑名单发送方列表 |
| `transportUrl` | IM2A core 传输地址（http/https/ws/wss） |
| `defaultUiState` | 初始 UI 态：`approval` / `overview` / `conversation` |

> 注意：`requiresApprovalAboveCents`、`budgetCentsPerDay`、`allowedSkills` 等属于
> `decide(capability, call)` 的 **Capability 入参**（由 core 按 agent 配置传入），**不是** channel 配置键。

## 能力边界（重要）

1. **本包是 channel 插件，不是完整 IM2A**：它提供"关系门控 + 付费注意力 + 三态 UI"的前端与判定逻辑；账本、审批持久化、A2A 协商仍在 IM2A core。
2. **K0 类插件不提供安全边界**：同进程受信代码，`permissions` 仅是评审契约，无法阻止 `os`/`unsafe` 级别的越权。真正的权限强制只在 K1（独立进程）/K2（跨主机）成立——本包按 channel 插件运行，**不应被当作沙箱**。
3. **不实现执行能力**：不执行 shell / 浏览器 / 文件操作；执行属于 OpenClaw 宿主与其它工具插件。
4. **判定是纯函数**：`decide(capability, call)` 无 IO、无状态；相同的输入永远得到相同输出（规范 I6）。
5. **付费状态机是内存/调用方持久化模型**：本包不做账本记账，只给出合法状态迁移与幂等键语义。

## 版本与发布

- edition 版本：仓库根 `editions.json` → `make version-sync` 同步到本包 `package.json`；
- 发布：`npm publish packages/openclaw-channel-relation --access public`（CI 中由 `editions.json` 驱动）；
- 兼容性：`im2a.compatibleCore` 声明兼容的 core 区间（当前 `>=2.3.25 <2.4.0`）。

## License

MIT（见 `LICENSE`）。归属与第三方依赖声明见仓库根 `ATTRIBUTION.md` / `NOTICE`。
