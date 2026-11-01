# 本地 IM2A 网关的 Windows 构建与验证

状态：本机已跑通，供 DSH MCP 桥做真实 L3 验证。不是发布产物。

## 为什么需要这一步

`im2a-http-mcp` 的 30 个工具里有一组是跨 Harness 总线工具
（`im2a_whoami` / `im2a_presence` / `im2a_send` / `im2a_recv` / `im2a_history` /
`im2a_harness_list`），它们不在桥内实现，而是转发到网关：

```text
DSH ──MCP──▶ im2a-http-mcp (127.0.0.1:18082) ──HTTP──▶ IM2A gateway (127.0.0.1:18080)
```

网关缺失时的表现是 `Error: fetch failed`——工具挂载成功、每次调用失败，
和 2.x 文档里 B6 要求的「失败必须可归因」正好相反。

## 为什么本机原来起不来

1. 发布物只有 Linux 网关：`release/w2m-im2a-2.0.3-rc3/go/im2a-gateway`（ELF）。
2. 源码 `E:\GPT\im2a-work\im2agent` 直接用了 POSIX 进程组 API：
   `SysProcAttr{Setpgid:true}` 与 `syscall.Kill(-pid, SIGTERM/SIGKILL)`，
   分布在三个文件里，Windows 下无法编译。
3. 本机没有 Go、没有 WSL、没有 Docker。

## 做了什么（仅本地构建，未回写上游）

新增平台封装 `internal/procgroup`：

| 文件 | 内容 |
| --- | --- |
| `procgroup_unix.go` | `Detach` = Setpgid；`Terminate`/`Kill` = 对 `-pid` 发 SIGTERM/SIGKILL |
| `procgroup_windows.go` | `Detach` 空操作；`Terminate`/`Kill` = `taskkill /PID <pid> /T [/F]` |

替换三处调用：

- `internal/openclaw/sender.go`
- `internal/plugin/openclaw/cli.go`
- `internal/plugin/runtime/supervisor.go`

语义不变：Unix 仍是整组信号；Windows 用 `taskkill /T` 结束进程树，
对 openclaw CLI 拉起的孙进程同样有效。

## 工具链与构建

```text
Go 1.27.3  windows/amd64   E:\GPT\tools\go
GOPROXY=https://goproxy.cn,direct
GOTMPDIR/GOCACHE/GOPATH    E:\GPT\.tmp\go*
```

```powershell
$env:GOROOT='E:\GPT\tools\go'; $env:GOTMPDIR='E:\GPT\.tmp\gobuild'
$env:GOCACHE='E:\GPT\.tmp\gocache'; $env:GOPATH='E:\GPT\.tmp\gopath'
$env:GOPROXY='https://goproxy.cn,direct'
cd E:\GPT\im2a-work\im2agent
& 'E:\GPT\tools\go\bin\go.exe' build -trimpath -o 'dist\im2a-gateway.exe' ./services/gateway
```

产物：`E:\GPT\im2a-work\im2agent\dist\im2a-gateway.exe`（22.5 MB）。

## 启动

```text
C:\Users\fangw\.dsh\im2a\gateway.cmd     # 读 gateway-token.txt，-port 18080，日志追加 gateway.log
C:\Users\fangw\.dsh\im2a\http-mcp.cmd    # 桥，18082
```

两者都用 `Start-Process cmd /c ... -WindowStyle Hidden` 脱离会话启动，
不随对话结束而被回收。

## 验证证据（2026-11-02 01:08–01:13）

```text
gateway 启动日志      [harness] bus ready: 33 supported harness(es)
                      gateway listening on :18080
GET /v1/harness/registry  200, 8534 bytes
mcp__im2a__im2a_whoami    {"harness":"deepseek-harness","agent":"dsh",
                           "session":{"id":"hs_d21b25eb1ede",...},"transport":"streamable-http"}
mcp__im2a__im2a_presence  count=1, session hs_d21b25eb1ede
mcp__im2a__im2a_harness_list 33 harnesses, stats.sessions=1
mcp__im2a__skills_list    {"items":[],"count":0}    （本地工具，不依赖网关）
```

重启网关后桥的旧会话会失效：`presence` 返回 `401 invalid session capability`。
重启桥（进程重新注册）后恢复，新会话号即上面的 `hs_d21b25eb1ede`。

## 已知限制

- Windows 网关是本地开发构建，未跑上游 Linux 全量测试；`taskkill /T`
  与 POSIX 整组信号在极端竞态下不完全等价。
- 网关数据在内存，重启即丢会话；桥需要重启才能重新注册。
- 网关 `-port 18080` 监听 `:18080`（所有网卡）。仅本机使用时应加防火墙或改为回环绑定；
  当前版本没有 `-host` 参数。
- 这套改动属于 `im2a-work/im2agent` 源码快照，该目录不是 Git 仓库，未提交。
