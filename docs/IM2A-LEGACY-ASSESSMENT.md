# TwinsEarth/IM2A 旧仓库评估与封存决策

> 决策时间：2026-10-25
> 评估对象：`github.com/TwinsEarth/IM2A`（旧 Node 原型线，HEAD `e3981b6`）
> 决策：**不合并，封存旧版，`main` 替换为 Go 内核新主线（发布 v2.0.2）**

---

## 1. 旧仓库事实（只读克隆实测）

| 项 | 值 |
|---|---|
| HEAD | `e3981b676b62e4a8574d8b45668348552835263b`（v0.1.3 主线） |
| 语言/运行时 | Node ESM（零 npm 依赖），核心持久化 `node:sqlite` 需 Node 24 |
| 代码规模 | 152 个 `.mjs/.js` 文件，**39,753 行**；仓库 260 文件 / 6.2MB |
| 目录 | `apps/`(45) `services/`(116) `contracts/`(12) `docs/`(43) `tests/`(12) `client/`(7) `deploy/`(8) |
| 已有 tag | 25 个，`v0.1.3-fix` … `v1.2.0-context`（本地可见 12 个） |
| 已有封存先例 | 分支 `legacy/im2a` + tag `legacy-im2a-2026-10-21`（v0.2.0 TaskLedger 副本，commit `e2338a1`） |
| 契约 | `contracts/INTERFACES.md`（**1,612 行**，§1–§52） |
| 测试口径 | README 声称 732 项；源码中 `test(` 出现 **98** 次（口径差异，未逐条核对） |

## 2. 可复用价值（评"值不值得参考"）

**值得参考（但已在新主线重新实现，不需要移植代码）**：

1. **五档信任矩阵 + 代码层 capability 门控**——旧仓库的核心洞察；新主线用 Go 完整实现（`internal/trustzone`），并升级为**可第三方验证**：实现无关规范 `spec/relation-gating.md` + **115 条公开向量** + 一致性套件（`cmd/relation-conformance`，另一实现接入后逐条比对，不一致 exit 1）。
2. **"陌生人付费消息永远发生在 Agent 预审之后"**——旧仓库的产品规则；新主线落为付费注意力状态机（`internal/api/paid_flow.go` + `internal/ledger`），账本幂等。
3. **人类 IM 与 Agent 协商双通道分离**——新主线保留该原则（D8：人类消息走腾讯 IM PaaS，自建通道承载 Agent/事件/原生客户端）。
4. **`contracts/INTERFACES.md` §1–§52**——可作为产品语义参考；新主线以 `proto/`（9 个契约）+ `spec/` 取代，旧文本留在 legacy 分支可回溯。
5. **`docs/34–39`（主线与版本策略 / 版本发布 SOP / 演进路线）**——流程思想可借鉴；新主线已用 `EDITIONS.md` + `version-sync/check` + CI 门禁实现"版本单一源"。

**不值得复用**：

- 旧实现（Node ESM + SQLite）与新主线（Go 1.27 + PostgreSQL + 长连接网关 + 插件运行时）**运行时、数据模型、部署形态完全不同**；
- 旧测试（Node 24 `node:test`）与新测试（Go + PG16 CI）无法共享；
- 旧 `apps/im2a`（v0.2.0）已在旧仓库内二次封存，历史包袱重。

## 3. 合并风险评估：**高，不合并**

| 风险 | 说明 | 结论 |
|---|---|---|
| 双实现分叉 | 两套运行时并行 → 语义漂移、修一处漏一处 | 不可接受 |
| 契约漂移 | 旧 `INTERFACES.md` §1–§52 vs 新 `proto/` + `spec/` 两套契约 | 需单一事实源 |
| 测试口径 | 旧 732 项 vs 实际 98 个 `test(`；新主线 CI 57 包 + 115 向量 | 合并即引入假绿 |
| 部署混淆 | 旧 Node 服务与新 Go 网关（`im2a-gateway.service`）同名产品 | 运维事故源 |
| 用户认知 | 两个"IM2A"仓库同步维护 | 不可持续 |

## 4. 处置方案（已执行）

1. **封存旧版**：`main` HEAD `e3981b6` 打分支 `legacy/node-v0.1.3` + tag `legacy-node-v0.1.3-2026-10-25`（连同既有 `legacy/im2a` 分支、`legacy-im2a-2026-10-21` tag 一并保留）；
2. **替换主线**：`main` 以新 Go 代码库整体替换（新根提交，历史经 legacy ref 可回溯）；
3. **发布 v2.0.2**：新 IM2A 仓库版本线从 `v2.0.2` 起（版本单一源 `internal/version/version.go`，派生文件由 `make version-sync` 生成）；
4. **保留参考**：旧 `contracts/INTERFACES.md` / `docs/34–39` 只在 legacy 分支可见，不进入新主线树。

## 5. 回滚方式

```bash
git fetch origin
git checkout legacy/node-v0.1.3    # 旧 Node 主线完整历史
# 或按 commit 只读取回：git show legacy/node-v0.1.3:apps/im2agent/server.mjs
```

新主线回滚：`git push --force origin <previous-sha>:main`（本次替换前的 main sha = `e3981b676b62e4a8574d8b45668348552835263b`）。
