# IM2A HTTP MCP — Agent Plugins 2.0

Streamable HTTP MCP bridge used by DSH and by the Go stdio MCP for the
skill/memory surface.

## Tools (28)

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

Evolution dry run (1):
  evolution_pipeline_dryrun

Drives / goal buffer (4):
  drive_list, drive_manage, goal_buffer, drive_propose

Scheduler (2):
  scheduler_cycle, scheduler_audit

Goal execution (1):
  goal_schedule

Drive calibration (1):
  drive_calibrate

Autonomy handoff (1):
  autonomy_pipeline_dryrun
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
`evolution_pipeline_dryrun` composes the five evolution interfaces without
durable writes and returns per-step timings, schema checks and ID traceability.
`drive_list` reads optional `drives/*.md` declarations; `drive_manage` writes
one through `skill_guard_check`; `goal_buffer` is a local SQLite intent buffer,
never Memory Hub. Its `put` admission enforces capacity (default 8) and merges
highly similar intents at the 0.85 similarity threshold. The buffer persists
intent metadata only; startup downgrades `executing` goals to `pending` and
keeps `scheduled` goals. `goal_buffer action=recover` exposes the recovery
report. Draft bodies stay in `drafts/`, never in SQLite. `drive_propose` reads drives and current signals to return
proposed goals; it does not write the goal buffer. `scheduler_cycle` splits the
cycle budget into external_pool and reserved_pool; external demand cannot
borrow reserved_pool. `goal_schedule` executes one goal with read-only signals
and writes only a candidate draft under `drafts/`; it never writes a skill.
`drive_calibrate` reads goal/execution history and returns calibration
suggestions without modifying `drives/`.
`reserved_ratio` is a Harness-governed setting: scheduler cycles may adjust it
within ±0.05 of the Agent's base ratio using audited completion and capacity
metrics. Agent skill writes containing `reserved_ratio` are rejected.
Skill ownership uses the agentskills.io `metadata` map with `im2a.*` string
keys; `im2a.*` writes are rejected on `skill_manage` and reserved for the
v2.7.1 `skill_claim`/v2.7.3 `skill_patch` paths. `drive_propose` also reads up to five
recent `skill_load` traces for each candidate skill and runs a lightweight
`success_pattern` reflection; negative trace evidence halves the proposal's
`priority_score` (not a veto).

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
# goal admission defaults
$env:IM2A_GOAL_BUFFER_CAPACITY = '8'
$env:IM2A_INTENT_DEDUP_THRESHOLD = '0.85'
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

The Go stdio MCP registers the same 28 tools and forwards the 22
skill/memory/trace/reflection/curator-audit/guard/evolution/drives/goal/propose/scheduler/execute/calibrate/autonomy tools to this server via
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
