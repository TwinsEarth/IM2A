# 发布密室：唯一安全版本

这是 v2.5.3 的固定新手 demo pack。它**不读取真实仓库、不访问网络**，只使用
IM2A 的 SKILL.md 和 FTS5 记忆。

## 准备

```bash
node scripts/seed-v253-demo.mjs --db-dir .tmp/v253-demo
IM2A_MCP_DB_DIR=.tmp/v253-demo \
IM2A_TENANT_ID=demo \
IM2A_HARNESS_TOKEN=<gateway-token> \
node packages/im2a-http-mcp/server.mjs
```

确认：

```bash
curl http://127.0.0.1:18082/healthz
```

## 给新手的输入

```text
只使用 IM2A 的技能和记忆，不要读仓库，不要上网。

从下面三个版本中选出唯一可发布版本：
A: v2.5.2-rc1, tag=a1b2c3, commit=a1b2c3, assets=5, healthz=401
B: v2.5.2-rc2, tag=d4e5f6, commit=999999, assets=6, healthz=401
C: v2.5.2, tag=f113192, commit=f113192, assets=6, healthz=401

只输出 5 行决策卡：结论 / 版本 / 三条证据 / 技能 / 记忆。
不要写分析过程。
```

## 期望

```text
GO C
tag=commit=f113192; assets=6; healthz 401=excluded
skill: release-gate@1.0.0
memory: rc1-missing-asset, rc2-tag-mismatch, healthz-excluded
```

- A：assets=5，NO-GO
- B：tag != commit，NO-GO
- C：GO

## 60 秒脚本

```text
0-5s    粘贴输入
5-20s   看决策卡
20-35s  追问为什么不选 A/B
35-50s  追问规则来自哪里
50-60s  要求把新规则写成 patch 提案，不启用
```

关键点：正确答案不在用户输入里，在 IM2A 的技能和记忆里。

## 第二幕

```text
把“rc 版本禁止发布，只有 final 可以发布”写成 release-gate 的 patch 提案。
不要启用。
```

期望 `status=proposed`、`activeChanged=false`。

## 约束

- 18082 只绑定 loopback。
- 不读取真实仓库，不访问网络。
- 不把 token 写入技能、记忆或截图。
- 本 demo 不声称生产 TLS/多租户能力。
