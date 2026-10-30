# IM2A HTTP MCP — Agent Plugins 2.0

Streamable HTTP MCP bridge used by DSH and by the Go stdio MCP for the
skill/memory surface.

## Tools (13)

```text
IM2A bus (6):
  im2a_harness_list, im2a_send, im2a_recv,
  im2a_presence, im2a_whoami, im2a_history

Skills (4):
  skills_list, skill_view, skill_manage, curator

Memory (3):
  memory_search, memory_get, memory_propose
```

`skill_manage` only writes proposals. `curator` can stage but cannot promote
or rollback. `memory_propose` does not become searchable until operator
promotion.

## Persistence

```text
skills: SQLite (packages table + active pointer + ledger + idempotency)
memory: SQLite FTS5 (memory_fts + tenant isolation)
```

Database directory defaults to `.tmp/im2a-mcp`; override with:

```powershell
$env:IM2A_MCP_DB_DIR = 'C:\path\to\im2a-mcp-data'
$env:IM2A_TENANT_ID = 'tenant-a'
```

## Run

```powershell
$env:IM2A_HARNESS_URL = 'http://127.0.0.1:18080'
$env:IM2A_HARNESS_TOKEN = '<token>'
$env:IM2A_MCP_DB_DIR = 'C:\path\to\im2a-mcp-data'
node packages/im2a-http-mcp/server.mjs
```

Endpoint: `http://127.0.0.1:18082/mcp`.

The Go stdio MCP registers the same 13 tools and forwards the 7 skill/memory
tools to this server via `IM2A_MCP_HTTP_URL`.

```powershell
$env:IM2A_MCP_HTTP_URL = 'http://127.0.0.1:18082/mcp'
```

## Security

- Loopback only by default; no production TLS/auth claim.
- Tenant is server-side (`IM2A_TENANT_ID`), not a tool argument.
- No token or auth code is stored in skill/memory content by these tools.
- FTS5 snippets are returned; full bodies require `memory_get`.
