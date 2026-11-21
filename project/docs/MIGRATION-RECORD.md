# 迁移记录 — 2026-11-21

执行：本会话（lead）。目标根：`E:\DSH\DS\IM2A`。旧目录**全部保留只读**。

## 1. 来源 → 去向

| 来源 | 去向 | 内容 | 校验 |
| --- | --- | --- | --- |
| `E:\GPT\IM2A\im2a-v2.3.45` | `platform/` | Node 平台仓库（含 `.git`） | tree `2f4f69be…` 一致 |
| `E:\GPT\IM2A`（排除子目录） | `product/` | Go 产品仓库（含 `.git`） | tree `f2a70db4…` 一致 |
| `E:\GPT\release\im2a-v3.3.0` | `archive/releases/im2a-v3.3.0` | 发布产物 | 目录一致 |
| `E:\GPT\release\im2a-v3.3.1` | `archive/releases/im2a-v3.3.1` | 发布产物 | 目录一致 |
| `E:\GPT\release\im2a-v3.3.3` | `archive/releases/im2a-v3.3.3` | 发布产物 | 目录一致 |
| `E:\GPT\.secrets\npm-token.txt` | `.secrets/` | npm 令牌 | 大小一致 |
| `E:\GPT\.secrets\github-token.txt` | `.secrets/` | GitHub 令牌 | 大小一致 |
| `…\SESSION-MIGRATION-KIT\session-tools\*.mjs` | `tools/session/` | 7 个会话工具 | 7 个文件 |
| DSH 会话 `session-1ed0e3a7…` | `sessions/current/`（快照）+ DSH 存储（还原副本） | 118 个会话，110.9 MB | 见 §3 |

## 2. 有意排除

| 排除项 | 原因 |
| --- | --- |
| `platform/.tmp`（357 MB） | 缓存/临时产物 |
| `node_modules` / `.pnpm-store` | 有 lockfile 可重装 |
| `E:\GPT` 下其他项目（`dsh-*`、`w2m`、`2Xray`、`Test`、`AU4A`…） | 不属于本项目 |
| 其他项目的 secrets（`w2m-*`、`2xray-*`、`wechat-*`、`vultr-*`） | 不属于本项目 |
| 系统临时目录 / 旧目录本身 | 规则要求 |

## 3. 校验结果

### 仓库

| 项 | platform | product |
| --- | --- | --- |
| HEAD | `475ba6d`（soul/6l-framework） | `0d61de3`（main） |
| HEAD tree | 旧 `2f4f69be…` = 新 `2f4f69be…` | 旧 `f2a70db4…` = 新 `f2a70db4…` |
| `git fsck` | exit 0 | exit 0 |
| tracked 差异 | 0 | 0（仅 `AGENTS.md` 未跟踪，与源一致） |
| tag 数 | 11 = 11 | — |
| 工作区 | 干净 | 干净（1 个未跟踪 AGENTS.md） |

### 质量门（新位置 `platform/`）

```text
scripts/validate.mjs                     exit 0
scripts/check-bridge-contracts.mjs       exit 0
check-package.mjs                        passed (3.3.3, engines 0.2.2-rc.5, 6 modules, 31 aliases)
node --test tests/*.test.mjs             148 passed / 0 failed
```

### 会话

| 项 | 结果 |
| --- | --- |
| 快照 | 118 个会话，110.9 MB（`sessions/current`） |
| 还原 | 118 个新 id 写入 `--E-DSH-DS-IM2A--`，29.5 秒 |
| 新根会话 | `session-4c497ceb-5de7-4c51-841b-8bdd4b279cf4`（50,452 事件） |
| 工作区 | `39c1a6fe-db27-4ad5-a3d8-a03a5722cca2`，path `E:\DSH\DS\IM2A`，已挂载 |
| 全库重复 id | 518 个日志，0 重复 |
| ZIP 导入能力 | 合成官方 ZIP 在隔离 DSH_HOME 中导入成功（root + 子代理 + 1 媒体 + 1 附件 + 自动挂载） |

## 4. 遗留事项

1. 旧目录 `E:\GPT\IM2A`、`E:\GPT\release`、`E:\GPT\.secrets` **未删除**，按用户要求保留只读。
2. `sessions/current` 为 110.9 MB，占项目体积 93%；如不需要跨机恢复可删除，不影响现行会话。
3. `.secrets` 只迁了两个令牌；其他项目凭据留在原处。
4. `product/` 里的 `AGENTS.md` 是产品仓库自己的未跟踪文件，原样保留。
5. 迁移时 DSH 正在运行，挂载用 `--force-running` 强行写入注册表；若日后发现工作区记录丢失，
   按 `docs/SESSIONS.md` 用 `--attach-only` 补挂（工具会自动备份 registry）。
