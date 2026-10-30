# IM2A HTTP MCP (P0-6)

Minimal Streamable HTTP MCP bridge for DSH when stdio child processes are
blocked by the sandbox.

## Scope

```text
initialize
tools/list
tools/call (im2a_harness_list, im2a_presence)
GET /healthz
GET /mcp (SSE keep-alive)
```

`im2a_send`, `im2a_recv`, and `im2a_history` remain stdio-only until session
state is implemented on this transport.

## Run

```powershell
$env:IM2A_HARNESS_TOKEN = '<token>'
node packages/im2a-http-mcp/server.mjs
```

```text
http://127.0.0.1:18082/mcp
```

Local loopback only. Remote deployment requires HTTPS and an explicit access
policy; this P0 server is not a production endpoint.
