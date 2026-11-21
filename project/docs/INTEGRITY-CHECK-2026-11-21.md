# 完整性检查 — 2026-11-21

检查对象：`E:\DSH\DS\IM2A`（迁移后项目根）与会话库 `C:\Users\fangw\.dsh`。

## 1. 当前会话位置

| 项 | 值 |
| --- | --- |
| 会话日志 | `C:\Users\fangw\.dsh\sessions\--E-DSH-DS--\session-24a36ef3-8c46-4879-916c-20919559a8ce\session.v4.jsonl.zstd` |
| 大小 | 23.7 MB |
| 会话 id | `session-24a36ef3-8c46-4879-916c-20919559a8ce` |
| header.cwd | `E:\DSH\DS` |
| 创建时间 | 2026-10-23T13:48:35Z |
| 工作区登记 | `3b79bba0-…`，path `E:\DSH\DS`，10 个 session |

**注意**：会话工作区是 `E:\DSH\DS`，比项目根 `E:\DSH\DS\IM2A` 高一层。

### 会话库现状（相对迁移当时发生变化）

| 项 | 迁移时 | 现在 |
| --- | --- | --- |
| project 目录 | `--E-GPT--` 等 6 个 | `--E-DSH-DS--`（193）+ default-workspace（1） |
| 会话日志数 | 518 | 194 |
| 重复 id | 0 | 0 |
| header 可读 | — | 194/194 |
| 迁移还原副本 `session-4c497ceb`（绑定 E:\DSH\DS\IM2A） | 存在 | **已不存在** |

结论：会话库被后续操作统一收敛到了 `E:\DSH\DS`；项目里 `sessions/current`（118 个会话快照，110.7 MB）仍然完整、可解码，可随时重新还原到 `E:\DSH\DS\IM2A`。

## 2. 检查结果

### A. 根结构 — 全部通过

| 项 | 结果 |
| --- | --- |
| 顶层 11 项（platform/product/docs/tools/sessions/archive/scratch/runtime/.secrets/AGENTS.md/README.md） | PASS |
| `AGENTS.md` | PASS（2,551 B） |
| `docs/` 四份文档 | PASS |
| `tools/session/` 7 个 .mjs | PASS |
| `.secrets/` 两个令牌 | PASS |

### B. platform 仓库 — 全部通过

| 项 | 结果 |
| --- | --- |
| HEAD tree `2f4f69be…` 与旧仓库一致 | PASS |
| `git fsck` | PASS（exit 0） |
| tracked 差异 | PASS（0；401 个文件） |
| tag 数 | PASS（11） |
| 工作区干净 | PASS（dirty 0） |
| 远端 | 无（本地仓库，符合预期） |

### C. product 仓库 — 全部通过

| 项 | 结果 |
| --- | --- |
| HEAD tree `f2a70db4…` 与旧仓库一致 | PASS |
| `git fsck` | PASS（exit 0） |
| tracked 差异 | PASS（0；60 个文件） |
| 远端 `TwinsEarth/IM2A` | PASS |

### D. 发布产物 — 1 处既存不一致

| 版本 | 结果 | 说明 |
| --- | --- | --- |
| v3.3.0 | ⚠ 1 项 | `SHA256SUMS` 中 `RELEASE-NOTES.md` 行过期（源目录同样过期）；tarball 行正确 |
| v3.3.1 | PASS | 全部匹配 |
| v3.3.3 | PASS | 全部匹配 |

三个版本的新旧文件**逐字节相同**（`RELEASE-NOTES.md`、`SHA256SUMS`、tarball 哈希两两相等），
所以是源目录的校验文件在发布后被编辑过，不是迁移损坏。

### E. platform 质量门（新位置）

```text
scripts/validate.mjs                  exit 0
scripts/check-bridge-contracts.mjs    pass
scripts/check-layer-contracts.mjs     pass
check-package.mjs                     passed (3.3.3, engines 0.2.2-rc.5, 6 modules, 31 aliases)
node --test tests/*.test.mjs          148 passed / 0 failed
```

### F. 体积

| 目录 | 大小 |
| --- | --- |
| sessions | 111.0 MB |
| platform | 6.7 MB |
| product | 0.8 MB |
| docs | 0.2 MB |
| archive | 0.1 MB |
| **合计** | **118.9 MB / 3,368 文件** |

### G. 临时目录

`scratch/` 已清空（0 文件）。

## 3. 结论

1. 代码、历史、发布产物、文档、工具、凭据**完整**，无丢失；
2. 会话快照完整可还原；但**当前会话绑定在 `E:\DSH\DS`**，不在 `E:\DSH\DS\IM2A`；
3. 唯一告警是 v3.3.0 的 `SHA256SUMS` 对 `RELEASE-NOTES.md` 过期——**源目录即如此**，可选择性重算。

## 4. 可选的后续动作

```powershell
# 若希望会话也绑定到项目根（DSH 运行时用 --force-running，或先关闭 DSH 后去掉它）
node tools/session/restore-session.mjs --snapshot sessions\current --workspace E:\DSH\DS\IM2A --force-running

# 若希望修正 v3.3.0 校验文件（会改变归档内容，谨慎）
# 重算 RELEASE-NOTES.md 行并写回 SHA256SUMS
```
