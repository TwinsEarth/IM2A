# 项目目录指南

根：`E:\DSH\DS\IM2A`

| 路径 | 是否进 git | 职责 | 备注 |
| --- | --- | --- | --- |
| `platform/` | 是（独立仓库） | Node 平台：插件 `packages/dsh-im2a-plugin`、桥 `packages/im2a-http-mcp`、契约 `docs/contracts`、测试 `tests/` | 分支 `soul/6l-framework`，11 个 tag |
| `product/` | 是（独立仓库） | Go 产品树 | 分支 `main`，远端 `TwinsEarth/IM2A` |
| `docs/` | 否（根目录非仓库） | 项目级文档 | 本目录 |
| `tools/session/` | 否 | 会话工具（零依赖 Node） | 来自 SESSION-MIGRATION-KIT |
| `sessions/` | 否 | `current/`（118 个会话快照）、官方导出 ZIP | 约 111 MB |
| `archive/releases/` | 否 | `v3.3.0` / `v3.3.1` / `v3.3.3` 发布产物 | tarball + NOTES + SHA256SUMS |
| `scratch/` | 否 | 唯一临时目录 | 收尾清空 |
| `runtime/` | 否 | 本机运行数据 | 当前为空 |
| `.secrets/` | 否 | `npm-token.txt`、`github-token.txt` | 永不提交/打包 |

## 为什么根目录不是 git 仓库

两个仓库各自独立（历史、远端、tag 都不同）。根目录只做容器与文档，
避免「仓库套仓库」——迁移前平台仓库被嵌在产品仓库工作树里，正是混乱的来源。

## 与旧位置的关系

| 旧位置 | 新位置 | 状态 |
| --- | --- | --- |
| `E:\GPT\IM2A\im2a-v2.3.45` | `E:\DSH\DS\IM2A\platform` | 已复制，旧目录保留只读 |
| `E:\GPT\IM2A` | `E:\DSH\DS\IM2A\product` | 已复制，旧目录保留只读 |
| `E:\GPT\release\im2a-v3.3.*` | `E:\DSH\DS\IM2A\archive\releases` | 已复制 |
| `E:\GPT\.secrets\{npm,github}-token.txt` | `.secrets/` | 已复制（仅本项目两个） |
| `E:\DSH\DS\2Xray\share\SESSION-MIGRATION-KIT\session-tools` | `tools/session/` | 已复制 |
