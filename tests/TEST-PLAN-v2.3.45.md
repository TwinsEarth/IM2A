# IM2A v2.3.45 测试计划

日期：2026-10-30
范围：`@twinsearth/im2a-dsh-plugin@0.1.1` + DSH 0.2.1-rc.5 + IM2A gateway + 浏览器扩展
环境：Windows 真机，Edge 156，NPM 公网包

## 1. 目标

验证已发布插件在真实环境中：

```text
可安装
可被 DSH 加载
可注册 6 个 MCP 工具
可完成真实读取调用
浏览器扩展可端到端流式对话
失败路径可识别且不误报 PASS
```

## 2. 不在本轮范围

```text
healthz：发布时明确排除
Qwen Code / MiMo Code adapter：post-release
生产负载/并发压力
跨公网安全渗透
其他 11 个 Harness adapter
```

## 3. 测试层级

```text
L0 环境与版本
L1 包安装/元数据
L2 DSH 加载与工具注册
L3 MCP 读工具真实调用
L4 浏览器扩展 E2E
L5 负向/受控写操作
L6 回滚与产物一致性
```

## 4. 通过标准

P0 用例全部 PASS；P1 用例 PASS 或带明确 BLOCKED 理由；任何 BLOCKED 不得写成 PASS。

## 5. 证据

```text
TEST-RESULTS.json
TEST-RESULTS.md
P0-BROWSER-E2E.json
P0-LIVE-DSH-TOOL-CALL.json
```

## 6. 风险控制

```text
不打印 token
不向 broadcast 发送测试消息
不修改 live profile 结构
写操作只允许受控目标，且先记录目标
```
