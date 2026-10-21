# 发布检查报告 · PASS

- 版本：0.1.0
- 契约：R21
- 数据 schema：1
- 开始：2026-10-21T13:55:01.290Z
- 结束：2026-10-21T13:55:22.965Z
- 结论：允许发布

| 步骤 | 命令 | 结果 | 耗时 |
|---|---|---|---|
| 全量测试 | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/test.mjs` | PASS | 17377ms |
| 一致性验收 | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/verify.mjs` | PASS | 2769ms |
| 自检 quickstart | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/quickstart.mjs --check` | PASS | 1132ms |
| 部署产物检查 | `E:\Unity3D\Tuanjie Cowork\cli\bin\win32-x64\node.exe scripts/deploy-lint.mjs` | PASS | 131ms |
| 内置实例验收 Gate | `AcceptanceRunner(DEFAULT_GATES) @ lan/jsonfile` | PASS | 259ms |

> 本报告由 scripts/release-check.mjs 生成；检查只读，使用内存存储与随机端口。
