# 工作规则（AGENTS.md 的展开版）

## 1. 目录职责

| 目录 | 允许写什么 | 禁止 |
| --- | --- | --- |
| `platform/` | 插件、桥、契约、测试、文档 | 放入临时文件、把 `product/` 内容复制进来 |
| `product/` | Go 产品代码 | 写入平台代码、覆盖 main 历史 |
| `docs/` | 项目级说明 | 放代码 |
| `sessions/` | 会话快照、导出 ZIP | 放代码或凭据 |
| `archive/` | 发布产物、历史版本解包 | 放仍在开发的文件 |
| `scratch/` | 任何临时文件 | 长期保留（收尾清空） |
| `runtime/` | 本机运行数据 | 提交或打包 |
| `.secrets/` | 凭据 | 提交、打包、截图、贴到聊天 |

## 2. 发布线

| 产物 | 源 | 去向 |
| --- | --- | --- |
| `@twinsearth/dsh-im2a-plugin` | `platform/packages/dsh-im2a-plugin` | npm（`latest`） |
| GitHub Release | 同一目录的 subtree | `TwinsEarth/dsh-im2a-plugin` |
| 平台快照分支 | `platform/` 整个仓库 | `TwinsEarth/IM2A` 的 `platform/*` 分支 |

硬规则：

1. 发布必须**当次明确授权**；未授权只做到「准备完毕」。
2. 发布前必过质量门（见 §3）。
3. 每次发布留证据：产物 sha256、npm integrity、tag、Release 链接。
4. 拿不到的一方记 `blocked`，不得写成「已发布」。

## 3. 质量门（`platform/` 下）

```text
node scripts/validate.mjs
node scripts/check-bridge-contracts.mjs
node scripts/check-layer-contracts.mjs
node scripts/check-release-allowlist.mjs
node packages/dsh-im2a-plugin/scripts/check-package.mjs
node --test tests/*.test.mjs
npm run pin:check
```

DSH 运行时版本变化时：`npm run pin:check` 返回 1 即需发新版（脚本会给出建议版本与四处待改文件）。

## 4. Git

- 本地提交允许；推送与 tag 需授权。
- 不 force push、不改写历史。
- 提交信息写清「做了什么 + 为什么」，不写「update」。

## 5. 会话

见 `docs/SESSIONS.md`。核心三条：还原用新 id；DSH 运行时不改注册表；快照可跨机恢复。

## 6. 换机清单

- [ ] 复制整个 `E:\DSH\DS\IM2A`
- [ ] Node ≥ 22.15、Git 就位
- [ ] `platform/`：`git fsck`、`node --test tests/*.test.mjs` 全绿
- [ ] `product/`：`git fsck`、`go build ./...`（如需要）
- [ ] 会话：`restore-session.mjs --snapshot sessions\current --workspace <新根>`
- [ ] 关闭/重开 DSH 后确认会话可见
