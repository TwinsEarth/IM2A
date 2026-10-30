# WorkBuddy Connector 验证结果

日期：2026-10-30
目标：`packages/im2a-workbuddy-connector`

## 结果

| 项 | 结果 |
|---|---|
| connector 结构 | PASS |
| connector-meta 字段 | PASS |
| mcp.json 单 Server + stdio | PASS |
| token-schema 三个字段 | PASS |
| 占位符与字段匹配 | PASS |
| 无硬编码凭证 | PASS |
| 真实 MCP stdio initialize | PASS |
| tools/list | PASS（6 个工具） |
| im2a_harness_list | PASS（33） |
| WorkBuddy 桌面 UI 安装 | **BLOCKED（本机未安装 WorkBuddy）** |

## 真实 MCP 测试

按 connector 的 `mcp.json` 等价配置启动 `im2a-mcp.exe`：

```text
IM2A_HARNESS_URL=http://127.0.0.1:18080
IM2A_HARNESS_ID=workbuddy
IM2A_AGENT_NAME=workbuddy
IM2A_HARNESS_TOKEN=<user token>
```

结果：

```text
serverInfo: im2a-harness-bridge
tools: im2a_harness_list, im2a_send, im2a_recv,
       im2a_presence, im2a_whoami, im2a_history
harnesses: 33
```

证据：`tests/workbuddy-connector-mcp.json`

## 未完成

```text
WorkBuddy 桌面端导入/连接器 UI 验证
WorkBuddy 市场提交
```

原因：本机没有 WorkBuddy 安装。

## 构建

```text
E:\GPT\release\im2a-workbuddy-connector-0.1.0.zip
```

安装时由用户在 WorkBuddy token 表单填写：

```text
IM2A_MCP_BIN
IM2A_HARNESS_URL
IM2A_HARNESS_TOKEN
```
