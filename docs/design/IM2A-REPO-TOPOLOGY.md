# IM2A 仓库拓扑与分支策略

状态：2026-11-16 由用户确认。本文固定"哪套代码住哪个仓库"，避免再次把 Node 平台推覆盖 Go 产品树。

## 三个仓库

| 仓库 | 内容 | 默认分支 | 角色 |
| --- | --- | --- | --- |
| `TwinsEarth/IM2A` | Go 产品（`go.mod`、`cmd/`、`internal/`、`services/`、`proto/`） | `main` | **主仓**，产品主线 |
| `TwinsEarth/IM2A`（分支 `platform/*`） | Node 平台（`packages/im2a-http-mcp`、`packages/dsh-im2a-plugin`、`docs/contracts`、`tests`） | 分支 | 服务端平台代码，长期驻留分支 |
| `TwinsEarth/dsh-im2a-plugin` | 插件分发包（`package.json`、`plugin.json`、`kernel/`、`locale/`、`migrations/`） | `main` | npm 插件源码与 Release |

## 分支策略（用户 2026-11-16 裁定）

```text
main             Go 产品树。禁止用 Node 平台覆盖。
platform         Node 平台滚动分支，跟随最新平台提交。
platform/vX.Y.Z  每个平台版本的不可变快照，用于回溯与发布对账。
```

规则：

1. **不把 Node 平台推入 `main`。** 两者差异 943 个路径，覆盖即不可逆。
2. `platform` 是滚动分支；`platform/vX.Y.Z` 是发布快照，打完不再移动。
3. 插件分发包单独放 `dsh-im2a-plugin`，`main` 的根就是包本身（与 npm 产物一致）。
4. 插件版本与平台版本各自独立：插件 `3.3.0`，平台 package.json 仍为 `2.7.2`。不要为了"看起来一致"去改其中一个。

## 当前指针

```text
IM2A main                   6e78a6b  (Go 产品, 未动)
IM2A platform/v3.3.0        31ac4ed  (Node 平台快照)
dsh-im2a-plugin main        9c8f86c  (插件树)
dsh-im2a-plugin tag         v3.3.0   -> 9c8f86c
```

## 为什么当初差点推错

平台仓库与产品仓库在顶层有同名目录（`packages/`、`docs/`、`scripts/`、`tests/`），
`git diff --name-status origin/main..HEAD` 会显示大量"删除"，看起来像正常的合并差异，
实际是两套不相干代码。**判断依据是 `go.mod`/`cmd/` 是否存在，不是目录名。**
