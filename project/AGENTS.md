# AGENTS.md — IM2A 项目工作守则

> 本文件是本项目（`E:\DSH\DS\IM2A`）的最高优先级目录规则。
> 任何 Agent 接手任务前先读本文件与 `docs/PROJECT-LAYOUT.md`。

## 1. 唯一根目录

`E:\DSH\DS\IM2A` 是**唯一写入点**。代码、文档、脚本、产物、临时文件只放这里。

| 目录 | 职责 |
| --- | --- |
| `platform/` | Node 平台仓库（插件 + MCP 桥），分支 `soul/6l-framework`，含 `.git` |
| `product/` | Go 产品仓库，分支 `main`，远端 `TwinsEarth/IM2A` |
| `docs/` | 项目级文档：布局、会话、迁移记录、工作守则 |
| `tools/session/` | DSH 会话快照/还原/导入工具（零依赖 Node） |
| `sessions/` | 会话快照与官方导出 ZIP |
| `archive/` | 发布产物解包（`releases/vX.Y.Z`） |
| `scratch/` | **唯一**临时目录，收尾清空 |
| `runtime/` | 本机运行数据（不进 git、不进发布） |
| `.secrets/` | 凭据（永不提交、永不打包） |

禁止在根目录随手新建散文件；临时产物一律进 `scratch/`。

## 2. 两个仓库，两条发布线

- `platform/`：`@twinsearth/dsh-im2a-plugin`（npm）+ im2a-http-mcp 桥。
- `product/`：Go 产品树（TwinsEarth/IM2A main）。
- **禁止**把平台代码推进产品仓库 `main`：两者差异数百个路径，覆盖不可逆。
  平台代码只走 `platform/latest` / `platform/vX.Y.Z` 分支。

## 3. 发布纪律

未经用户当次明确要求，禁止执行：`npm publish`、创建 GitHub Release、
推送 tag、推送远端分支。准备完成后只汇报「发布准备完毕」。

## 4. Git 规则

- 允许本地提交；推送远端与打 tag 需明确要求。
- 不 force push、不改写他人仓库历史。
- 提交前跑质量门。

## 5. 质量门（在 `platform/` 下执行）

```text
node scripts/validate.mjs
node scripts/check-bridge-contracts.mjs
node scripts/check-layer-contracts.mjs
node packages/dsh-im2a-plugin/scripts/check-package.mjs
node --test tests/*.test.mjs
npm run pin:check      # DSH 版本 pin 是否还匹配
```

全绿才算完成。测试失败不得用「跳过」掩盖。

## 6. 会话规则

本项目的 DSH 会话通过 `tools/session/` 管理；恢复/导入不改原会话（一律分配新 id）。
细节见 `docs/SESSIONS.md`。DSH 运行时**不要**手改 `workspace.json`。

## 7. 旧目录

`E:\GPT\IM2A`、`E:\GPT\im2a-v2.3.45`、`E:\GPT\release` 等为迁移前位置，
**只读保留**，确认不再需要后由用户决定删除。不得在旧目录继续写入。
