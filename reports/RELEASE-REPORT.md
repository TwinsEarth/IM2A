# 发布检查报告 · FAIL

- 版本：0.1.1
- 契约：R22
- 数据 schema：1
- 开始：2026-10-21T13:58:36.663Z
- 结束：2026-10-21T13:58:46.158Z
- 结论：禁止发布

| 步骤 | 命令 | 结果 | 耗时 |
|---|---|---|---|
| 全量测试 | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/test.mjs` | PASS | 7796ms |
| 一致性验收 | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/verify.mjs` | PASS | 866ms |
| 自检 quickstart | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/quickstart.mjs --check` | PASS | 546ms |
| 部署产物检查 | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/deploy-lint.mjs` | PASS | 84ms |
| 内置实例验收 Gate | `AcceptanceRunner(DEFAULT_GATES) @ lan/jsonfile` | PASS | 129ms |
| 版本 ↔ tag 一致性 | `git tag --points-at HEAD` | FAIL | 68ms |

> 本报告由 scripts/release-check.mjs 生成；检查只读，使用内存存储与随机端口。
