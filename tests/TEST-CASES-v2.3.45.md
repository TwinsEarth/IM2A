# IM2A v2.3.45 测试用例

## TP-001 环境预检

优先级：P0

步骤：

1. 读取 DSH 版本。
2. 读取 Node/Edge 版本。
3. 检查 gateway `GET /v1/plugins`。
4. 检查 live profile 中插件版本。

期望：

```text
DSH = 0.2.1-rc.5
Node >= 22
Edge 可启动
gateway = 200
插件 = 0.1.1
```

## TP-002 npm 公共包可见性

优先级：P0

步骤：

1. 匿名查询 `npm view @twinsearth/im2a-dsh-plugin@0.1.1`。
2. 检查 tarball URL。
3. 在临时 DSH profile 从 npm 安装（不使用本地 tgz）。
4. 验证 `--dump-config` 中出现 `im2a-mcp`。

期望：版本 0.1.1；安装成功；配置行存在。

## TP-003 DSH 兼容加载

优先级：P0

步骤：

1. 确认 `engines.dsh=0.2.1-rc.5`。
2. 检查 live bundle 选中。
3. 重启/热加载后确认无 incompatible 警告。

期望：无 IM2A incompatibility；工具命名空间出现。

## TP-004 MCP 读工具：harness_list

优先级：P0

步骤：调用 `mcp__im2a__im2a_harness_list`。

期望：`ok=true`，harness 数量 > 0，无 isError。

## TP-005 MCP 读工具：whoami

优先级：P0

步骤：调用 `mcp__im2a__im2a_whoami`。

期望：返回当前 DSH 身份/会话信息。

## TP-006 MCP 读工具：presence

优先级：P1

步骤：调用 `mcp__im2a__im2a_presence`。

期望：返回在线列表或明确空列表，不报错。

## TP-007 MCP 读工具：history

优先级：P1

步骤：调用 `mcp__im2a__im2a_history`。

期望：返回历史结构；空历史也是 PASS。

## TP-008 浏览器 popup

优先级：P0

步骤：Edge 加载解压扩展，CDP 打开 `popup.html`。

期望：标题“IM2A 浏览器终端”，按钮存在，配置 baseUrl 正确。

## TP-009 浏览器 options

优先级：P0

步骤：CDP 打开 `options.html`。

期望：标题“IM2A 浏览器终端 · 设置”，输入控件 > 0。

## TP-010 浏览器 SSE 对话

优先级：P0

步骤：通过扩展 background port 发送“只回复 OK，不要解释。”

期望：`ok=true`，content=OK，model 非空，收到 delta/done。

## TP-011 负向：无认证/错误 token

优先级：P1

步骤：使用错误 token 调用 gateway MCP 接口。

期望：被拒绝，不泄露数据，不误报 PASS。

## TP-012 受控 send/recv

优先级：P1（默认不执行）

步骤：

1. 选定一个明确测试目标。
2. `im2a_send` 只发测试消息。
3. `im2a_recv` 验证回执。

约束：禁止 broadcast；未指定目标时标记 BLOCKED。

## TP-013 回滚

优先级：P1

步骤：在临时 DSH profile 卸载 bundle。

期望：`im2a-mcp` 行消失；live 不受影响。

## TP-014 发布产物校验

优先级：P0

步骤：校验 GitHub Release 资产与 SHA256SUMS。

期望：6 个资产存在；哈希一致；npm tarball 可下载。
