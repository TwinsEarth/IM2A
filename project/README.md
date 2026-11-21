# IM2A — 项目根目录

> 一个根目录，两个仓库，一套质量门，一份会话工具链。
> 迁移记录见 `docs/MIGRATION-RECORD.md`；目录职责见 `docs/PROJECT-LAYOUT.md`。

## 这是什么

IM2A（智能体宇宙 / Agent Universe）是一个面向 Agent 开发与开放的即时连接网络：
DSH 里跑一个插件（`@twinsearth/dsh-im2a-plugin`），MCP 桥提供 32 个工具，
内核负责生命周期、治理门控与六层契约。

## 目录

```text
E:\DSH\DS\IM2A\
├─ platform\      Node 平台仓库（插件 + MCP 桥）      .git · soul/6l-framework
├─ product\       Go 产品仓库                          .git · main
├─ docs\          布局 / 会话 / 迁移记录 / 工作守则
├─ tools\session\ 会话快照、还原、官方 ZIP 导入
├─ sessions\      会话快照与导出 ZIP
├─ archive\       发布产物（releases/v3.3.0 …）
├─ scratch\       临时目录（唯一）
├─ runtime\       本机运行数据
├─ .secrets\      凭据（不提交、不打包）
└─ AGENTS.md      工作守则（先读）
```

## 快速开始

```powershell
cd E:\DSH\DS\IM2A\platform

node scripts/validate.mjs
node --test tests/*.test.mjs
npm run pin:check
```

## 会话

当前会话已还原到本项目工作区（新 id）。换机或重开：

```powershell
node tools/session/restore-session.mjs --snapshot sessions\current --workspace E:\DSH\DS\IM2A
node tools/session/import-session.mjs --zip <dsh-session-*.zip> --workspace E:\DSH\DS\IM2A
```

细节与注意事项见 `docs/SESSIONS.md`。

## 换机

复制整个 `E:\DSH\DS\IM2A` 文件夹到新机器即可继续开发：

- 两个仓库自带完整 `.git` 历史与 tag；
- `sessions/current` 可还原本会话；
- 只需安装 Node ≥ 22.15（推荐 24+）与 Git，并按需安装 DSH。
