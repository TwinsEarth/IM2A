# im2agent (npm CLI)

IM2A gateway 的跨平台 npm 启动器。安装后可用 `im2a` 命令启动网关，
首次运行会从 GitHub Release 下载对应平台的 Go 二进制并缓存到 `~/.im2a/bin/<version>`。

```bash
npm install -g @twinsearth/im2agent
im2a -port 8080
curl http://127.0.0.1:8080/healthz
```

## 开发 / 离线测试

用本地已编译的二进制（跳过下载）：

```bash
export IM2A_BIN=/path/to/im2a-gateway_linux_amd64
im2a -port 8080
```

平台覆盖：linux / darwin / windows × amd64 / arm64。版本与 Go 后端同步（当前 1.8.8）。
