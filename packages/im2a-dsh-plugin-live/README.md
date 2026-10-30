# IM2A DSH live HTTP overlay

This bundle only overrides the `im2a-mcp` row for a live DSH profile:

```text
transport: streamable-http
url: http://127.0.0.1:18082/mcp
```

It carries no token, no `im2a-mcp` binary path, and no launcher. The local
Agent Plugins 2.0 HTTP MCP service owns SQLite/FTS5 and exposes 13 tools.

## Prerequisite

Run the HTTP MCP service on loopback:

```powershell
$env:IM2A_MCP_DB_DIR = "C:\Users\fangw\.dsh\im2a\data"
$env:IM2A_TENANT_ID = "desktop"
$env:IM2A_HARNESS_URL = "http://127.0.0.1:18080"
$env:IM2A_HARNESS_TOKEN = "<gateway token>"
node packages/im2a-http-mcp/server.mjs
```

Verify:

```powershell
curl.exe http://127.0.0.1:18082/healthz
```

## Install after the host bundle

```powershell
dsh plugin --profile desktop add file:E:/GPT/im2a-v2.3.45/packages/im2a-dsh-plugin-live
```

The loader target `im2a-mcp` must already exist from
`@twinsearth/im2a-dsh-plugin`.

## Security

- 18082 must stay bound to `127.0.0.1`.
- For a remote DSH, use an SSH tunnel; do not expose 18082 publicly.
- The HTTP MCP currently has no inbound auth/TLS; it is a local service.
- Skill/memory proposal and promotion boundaries remain server-side.
