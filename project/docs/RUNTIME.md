# 运行时：插件 + MCP 桥 + 网关

## 版本对照（2026-11-21）

| 组件 | 版本 | 来源 |
| --- | --- | --- |
| DSH 运行时 | `0.2.2-rc.5` | `dsh --version` |
| 插件（已安装） | `@twinsearth/dsh-im2a-plugin@3.3.3` | profile `desktop` 依赖 |
| 插件 `engines.dsh` | `0.2.2-rc.5` | 与运行时精确匹配 |
| MCP 桥 | `platform/packages/im2a-http-mcp` | 本地进程 :18082 |
| IM2A 网关 | `runtime/im2a/bin/im2a-gateway.exe`（Windows 构建，go1.27.3） | 本地进程 :18080 |
| GitHub 插件仓库 | `main` = `73e4a62` = tag `v3.3.3` | 与本地 subtree `7400d918…` 一致 |

## 三个进程

```text
DSH ──MCP──▶ 桥 (127.0.0.1:18082) ──HTTP──▶ 网关 (127.0.0.1:18080)
                    │                              └─ harness 总线（33 harnesses）
                    └─ 27 个本地工具（skills/memory/trace/drives/goal/scheduler/governance/kernel）
```

| 进程 | 缺失时表现 |
| --- | --- |
| 桥 | MCP 行 `server is disconnected`，32 工具全不可用 |
| 网关 | 桥正常，5 个总线工具（whoami/presence/send/recv/history）报错 |

## 启动全部

```powershell
cmd /c E:\DSH\DS\IM2A\runtime\im2a\gateway.cmd    # 网关 :18080
cmd /c E:\DSH\DS\IM2A\runtime\im2a\http-mcp.cmd   # 桥   :18082
```

两者都用 `Start-Process cmd /c ... -WindowStyle Hidden` 脱离会话启动，不随对话结束回收。

- 网关令牌：`runtime/im2a/gateway-token.txt`（48 hex，本地生成；launcher 用 `set /p` 读入
  `GATEWAY_TOKEN` 与 `IM2A_ADMIN_TOKEN`，不复制明文到脚本）
- 桥令牌：同一文件，`set /p IM2A_HARNESS_TOKEN=<token文件`
- 数据：`runtime/im2a/data`（桥）、网关数据在内存
- 日志：`runtime/im2a/logs/gateway.log`、`runtime/im2a/logs/http-mcp.log`

### 验证

```text
网关日志   [harness] bus ready: 33 supported harness(es)
           gateway listening on :18080
桥         GET /healthz → 200
mcp__im2a__im2a_whoami      → session hs_b84e1cb1f269（2026-11-21 10:26 实测）
mcp__im2a__im2a_presence    → count 1，本会话在线
mcp__im2a__im2a_harness_list→ 33 个 harness
```

## 网关源码与重建

**公开仓库没有 harness 路由**（已逐分支核实：`IM2A` main 只有 `cmd/conn-gateway`，
`im2agent` 的 `services/gateway` 无 harness 接口，组织内代码搜索 `harness/sessions` 命中 0）。

可用的源码快照来自本机非 Git 目录，已复制进项目：

| 路径 | 内容 |
| --- | --- |
| `runtime/im2a/gateway/src/` | `im2agent` 源码快照 4.4 MB（含 `internal/api/harness.go`、`internal/procgroup` Windows 补丁） |
| `runtime/im2a/bin/im2a-gateway.exe` | 预构建 Windows 二进制 21.5 MB（构建信息：`human-agent-im/services/gateway`, go1.27.3） |

重建（本机已有 Go 1.27）：

```powershell
cd E:\DSH\DS\IM2A\runtime\im2a\gateway\src
$env:GOPROXY='https://goproxy.cn,direct'
$env:GOCACHE='E:\DSH\DS\IM2A\runtime\im2a\gateway\gocache'
& 'E:\ToFuture\.tools\go\bin\go.exe' build -trimpath -o '..\..\bin\im2a-gateway.exe' ./services/gateway
```

Windows 可编译的原因：快照里 `internal/procgroup` 把 POSIX 进程组调用换成了 `taskkill /T`，
上游公开源码仍是 `Setpgid`/`syscall.Kill`，在 Windows 下编不过。

## 已知限制

- 网关 `-port 18080` 监听**所有网卡**（无 `-host` 参数）；本机使用请依赖防火墙。
- 网关数据在内存，重启即丢会话；重启网关后需重启桥重新注册（否则 `401 invalid session capability`）。
- 隧道方案（`ssh -N -L 18080:127.0.0.1:18080 ubuntu@134.175.243.146`，令牌取自远端
  `/etc/im2a/http-mcp.env`）仍可用作备选，但已改为本地网关，不再依赖外网。
- 插件安装后若 MCP 行仍 `disconnected`：确认桥已启动，然后在插件管理器 `禁用 → 启用` 该 bundle。
