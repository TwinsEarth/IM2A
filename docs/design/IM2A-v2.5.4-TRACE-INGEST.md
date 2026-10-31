# IM2A v2.5.4 — Execution Trace Foundation

Date: 2026-10-31
Status: implemented and deployed to the Tencent dev host; not production-ready.
Scope owner: the v2.5.4 slice defined by the user; this is the first step toward
the v3.0.3 "Agent soul" program.

## Why this version exists

The v2.6.x autonomous-goal work needs a reliable answer to "what did this Agent
actually do?" Without a trace contract, `drive_propose`, skill ownership and
credit attribution would be built on logs that were never designed as evidence.
v2.5.4 introduces only the write path: structured trace ingestion, immutable
storage, and schema validation. It deliberately does not add RL, reflection or
online analysis.

## MCP surface

`trace_ingest` is exposed by both the HTTP MCP and the Go stdio MCP. The tool
surface is now **14**:

```text
IM2A bus (6) + skills (4) + memory (3) + trace (1)
```

Input:

```json
{
  "session_id": "hs_...",
  "agent_did": "did:im2a:...",
  "events": [
    {
      "span_type": "tool_call",
      "name": "im2a_send",
      "input_digest": "sha256:<64 hex>",
      "output_digest": "sha256:<64 hex>",
      "duration_ms": 12.5,
      "token_cost": 7,
      "success": true,
      "error_class": null,
      "skill_version": "1.0.0",
      "occurred_at": "2026-10-31T10:14:01.620Z"
    }
  ]
}
```

`span_type` is one of `tool_call`, `llm_call`, `retrieval`, `skill_load`.
`skill_version` is optional for other spans and defaults to `none`;
`skill_load` requires a real version. `error_class` is normalized to `unknown`
for a failed span that omitted it, and is rejected when a successful span
supplies one.

Output:

```json
{
  "trace_id": "<32 lowercase hex>",
  "ingested_count": 12,
  "content_sha256": "sha256:<64 hex>"
}
```

## Storage contract

```text
/opt/im2a/v2.5.4/data/traces/<tenant>/trajectory.jsonl
```

- Append-only; there is no update or delete API in this version.
- One canonical JSON record per line; append is serialized and `fsync`ed.
- Tenant comes from `IM2A_TENANT_ID`, never from the tool argument.
- Data directory is `0750`; file is `0640` and owned by the service account.
- Raw prompts, tool arguments, tool outputs, credentials and free-form memory
  are structurally rejected. Only SHA-256 digests are accepted.

The JSON Schema is
[execution-trace-1.0.schema.json](../contracts/execution-trace-1.0.schema.json).
Each event carries OpenTelemetry GenAI-style attributes:

```text
gen_ai.operation.name
gen_ai.tool.name          (tool_call / skill_load)
gen_ai.usage.total_tokens (when token_cost > 0)
im2a.agent_did
im2a.skill_version
```

## Acceptance evidence

Local Node tests:

```text
12-event schema acceptance                  PASS
append-only two-line preservation           PASS
raw content / unknown fields rejected       PASS
digest, DID, span type, size bounds         PASS
skill_load version requirement              PASS
tenant-separated trajectory files           PASS
6 tests, 6 passed, 0 failed
```

Go:

```text
go build ./...                              PASS
go vet ./...                                PASS
go test ./...                               all test packages ok / 0 failed
cmd/im2a-mcp trace delegation test          PASS
```

Live Tencent dev:

```text
HTTP MCP tools/list                         14
trace_ingest 12 events                      PASS
JSON Schema validation                      PASS
all events carry im2a.agent_did             PASS
all events carry im2a.skill_version         PASS
raw-content negative control rejected       PASS
raw-content negative control did not append PASS
second ingest appends, does not overwrite   PASS
Go MCP 0.4.0 tools/list                     14
Go MCP trace_ingest                         PASS
Windows Go MCP tools/list                   14, trace_ingest present
```

Evidence:

```text
tests/V254-TRACE-UNIT-EVIDENCE.json
tests/TENCENT-V254-TRACE-EVIDENCE.json
tests/TENCENT-V254-DEPLOYMENT-EVIDENCE.json
tests/V254-WINDOWS-MCP-EVIDENCE.json
```

## Deployment

```text
gateway:          v2.5.3 (unchanged; this slice is an MCP/client change)
HTTP MCP root:    /opt/im2a/v2.5.4
HTTP MCP:         Node 0.4.0, 14 tools, loopback 127.0.0.1:18082
Go MCP:           /opt/im2a/v2.5.4/bin/im2a-mcp and stable v2.5.2 path, 0.4.0
trace directory:  /opt/im2a/v2.5.4/data/traces
backup:           /opt/backups/im2a-mcp-pre-v2.5.4-20261031181246
```

The unit now sets `IM2A_TRACE_DIR=/opt/im2a/v2.5.4/data/traces` and keeps
`ReadWritePaths=/opt/im2a/v2.5.4/data`.

## Rollback

```bash
sudo systemctl stop im2a-http-mcp.service
sudo install -m 0644 /opt/backups/im2a-mcp-pre-v2.5.4-20261031181246/im2a-http-mcp.service /etc/systemd/system/im2a-http-mcp.service
sudo install -m 0600 /opt/backups/im2a-mcp-pre-v2.5.4-20261031181246/http-mcp.env /etc/im2a/http-mcp.env
sudo install -m 0755 /opt/backups/im2a-mcp-pre-v2.5.4-20261031181246/im2a-mcp /opt/im2a/v2.5.2/bin/im2a-mcp
sudo systemctl daemon-reload
sudo systemctl start im2a-http-mcp.service
```

The old v2.5.3 tree and its data are untouched; the backup contains the
pre-cutover unit, env, Go binary and data tarball.

## Scope boundary and open P0s

This version follows the owner's trace-first v2.5.4 specification. The expert
architecture review also identified two items that must not be mistaken for
done:

- `curator` still shares the main Agent identity; curator/operator separation
  remains open.
- The harness message ordering domain still uses the legacy id path; the
  per-conversation `seq` domain remains open.

Both are prerequisites for later attribution and governance work. They are not
claimed as fixed by v2.5.4.

Additional limits:

- `agent_did` and `session_id` are asserted by the caller, not
  cryptographically bound to the gateway session.
- The trace file is local to the Tencent dev host; there is no distributed
  trace bus or object-store anchor.
- No RL, reflection, promotion or credit calculation consumes this file yet.
- v2.5.4 is a dev deployment, not a production/TLS/multi-tenant claim.
