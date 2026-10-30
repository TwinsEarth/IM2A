# IM2A v2.3.45 真机测试结果

执行时间：2026-10-30 14:40 +08:00
机器：Windows 10.0.26300

## 环境

```text
DSH：0.2.1-rc.5
插件：@twinsearth/im2a-dsh-plugin@0.1.1
Node：24.16.0
Edge：156.0.4314.50
Gateway：http://127.0.0.1:18080 -> 200
```

## 汇总

```text
总计：14
PASS：13
BLOCKED：1
FAIL：0
```

## 结果

| 用例 | 结果 | 证据 |
|---|---|---|
| TP-001 环境预检 | PASS | rc.5 / Edge / gateway 200 / 0.1.1 |
| TP-002 npm 公网安装 | PASS | npm view + temp profile install + dump |
| TP-003 DSH 兼容加载 | PASS | live 工具命名空间可用 |
| TP-004 harness_list | PASS | 33 harnesses |
| TP-005 whoami | PASS | agent=dsh, session=hs_45c557370376 |
| TP-006 presence | PASS | count=1 |
| TP-007 history | PASS | count=0（空历史合法） |
| TP-008 popup | PASS | 标题/3 buttons/baseUrl |
| TP-009 options | PASS | 36 inputs / 4 buttons |
| TP-010 SSE | PASS | content=OK, model=deepseek-flash |
| TP-011 负向鉴权 | PASS | invalid/none token 均 401 |
| TP-012 受控 send/recv | BLOCKED | 无批准目标，禁止 broadcast |
| TP-013 临时 profile 回滚 | PASS | refs=0，dump 无 im2a-mcp |
| TP-014 发布产物校验 | PASS | 6 assets，5 校验项，0 mismatch |

## 排除项

```text
healthz：本次发布明确排除
Qwen/MiMo：post-release
```

## 证据

- `tests/evidence/browser-stream.json`
- `tests/evidence/browser-ui.json`
- `P0-LIVE-DSH-TOOL-CALL.json`
- `P0-LIVE-ROLLBACK-DRILL.json`
- `tests/TEST-RESULTS.json`

## 结论

```text
已发布插件的核心链路：PASS
浏览器 E2E：PASS
npm 公网安装：PASS
回滚：PASS
受控写操作：BLOCKED（未执行）
healthz：excluded
```
