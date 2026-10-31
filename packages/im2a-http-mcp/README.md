# IM2A HTTP MCP — Agent Plugins 2.0

Streamable HTTP MCP bridge used by DSH and by the Go stdio MCP for the
skill/memory surface.

## Tools (18)

```text
IM2A bus (6):
  im2a_harness_list, im2a_send, im2a_recv,
  im2a_presence, im2a_whoami, im2a_history

Skills (4):
  skills_list, skill_view, skill_manage, curator

Memory (4):
  memory_search, memory_recall_v2, memory_get, memory_propose

Trace / reflection (2):
  trace_ingest, reflect_trigger

Curator audit (1):
  curator_audit

Skill write guard (1):
  skill_guard_check
```

`skill_manage` only writes proposals. `curator` can stage but cannot promote
or rollback. `memory_propose` does not become searchable until operator
promotion. `trace_ingest` appends one immutable execution-trace batch; it
accepts span metadata and SHA-256 digests only. `reflect_trigger` reads a trace
and returns an assessment plus abstract candidates; it never writes a skill,
memory, proposal or trace. `curator_audit` is a read-only lifecycle audit; its
`recommended_action` values are advisory and never executed. `skill_guard_check`
is a server-side gate: `skill_manage` and `curator.stage` call it before any
write, and it appends a hash-chained audit event. A public or read-only session
is always denied. `memory_recall_v2` is a layered, quality-gated recall with a
hard token budget; it suppresses stale atoms unless `include_stale` is true.

## Persistence

```text
skills: SQLite (packages table + active pointer + ledger + idempotency)
memory: SQLite FTS5 (memory_fts + tenant isolation)
trace:  append-only JSONL (trajectory.jsonl, tenant-scoped, SHA-256 digests)
```

Database directory defaults to `.tmp/im2a-mcp`; override with:

```powershell
$env:IM2A_MCP_DB_DIR = 'C:\path\to\im2a-mcp-data'
$env:IM2A_TENANT_ID = 'tenant-a'
# optional; defaults to $IM2A_MCP_DB_DIR/traces
$env:IM2A_TRACE_DIR = 'C:\path\to\im2a-traces'
# optional reflection thresholds, inline JSON or a JSON file path
$env:IM2A_REFLECT_CONFIG = 'C:\path\to\reflect-config.json'
# optional skill-write guard key and audit path
$env:IM2A_SKILL_GUARD_KEY = '<32+ byte HMAC key>'
$env:IM2A_SKILL_GUARD_AUDIT = 'C:\path\to\skill-guard\audit.jsonl'
```

The guard session is sent as `x-im2a-skill-session`, an HMAC-signed,
short-lived token containing the agent DID, authorization level and scopes.
A request without a valid token gets the public session and cannot write.

## Run

```powershell
$env:IM2A_HARNESS_URL = 'http://127.0.0.1:18080'
$env:IM2A_HARNESS_TOKEN = '<token>'
$env:IM2A_MCP_DB_DIR = 'C:\path\to\im2a-mcp-data'
node packages/im2a-http-mcp/server.mjs
```

Endpoint: `http://127.0.0.1:18082/mcp`.

The Go stdio MCP registers the same 18 tools and forwards the 12
skill/memory/trace/reflection/curator-audit/guard tools to this server via
`IM2A_MCP_HTTP_URL`. It forwards `IM2A_SKILL_GUARD_SESSION_TOKEN` as the
`x-im2a-skill-session` header when configured.

```powershell
$env:IM2A_MCP_HTTP_URL = 'http://127.0.0.1:18082/mcp'
```

## Security

- Loopback only by default; no production TLS/auth claim.
- Tenant is server-side (`IM2A_TENANT_ID`), not a tool argument.
- No token or auth code is stored in skill/memory/trace content by these tools.
- FTS5 snippets are returned; full bodies require `memory_get`.
- Trace events contain digests, not raw prompts, arguments, outputs or
  credentials; `trajectory.jsonl` has no update or delete API.
- `reflect_trigger` is read-only; its candidates are suggestions, never
  automatic skill/memory writes.
- `curator_audit` never archives, merges or deletes. It reads usage/telemetry
  and returns keep/flag/merge/archive recommendations only.
- `memory_recall_v2` enforces a hard token budget and suppresses stale atoms
  unless explicitly requested; it never writes memory.
- `skill_guard_check` requires `skill:write` and authorization level L2.
  `skill:read` and public sessions are denied; audit events are append-only
  and hash-chained. The guard is in the MCP handler, not in Hermes.
