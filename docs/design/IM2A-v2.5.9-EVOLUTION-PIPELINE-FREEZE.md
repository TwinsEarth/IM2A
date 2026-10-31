# IM2A v2.5.9 — Evolution Pipeline Dry Run and Interface Freeze

Date: 2026-10-31
Status: implemented and deployed to the Tencent dev host; not production-ready.

## Scope

v2.5.9 introduces one orchestration tool, `evolution_pipeline_dryrun`, and
freezes the five v2.5.4-v2.5.8 interfaces. It does not introduce a new
business primitive.

```text
trace_ingest
  -> reflect_trigger
  -> curator_audit
  -> skill_guard_check   (simulated write)
  -> memory_recall_v2
```

The dry run proves that v2.6.x `drive_propose` can consume the existing
outputs without changing any v2.5.x input or output schema. All durable
writes are replaced by `would_write` summaries.

## Tool surface

```text
18 -> 19 tools
evolution_pipeline_dryrun   no input; returns pipeline_report
```

The orchestrator is read-only with respect to durable state:

```text
trace_ingest      normalize only; no trajectory append
reflect_trigger   assess the in-memory record; no writes
curator_audit     in-memory SQLite fixture; no writes
skill_guard_check evaluateGuard only; no audit append
memory_recall_v2  in-memory SQLite fixture; no writes
```

`writes` is always `0`; `would_write_count` is `2` (trace + guard audit).

## Pipeline report

```text
schema_version       "1.0"
run_id               "dryrun_<24 hex>"
mode                 "dryrun"
status               "pass" | "fail"
started_at/completed_at
duration_ms
dry_run              true
writes               0
would_write_count    2
steps[5]             step, index, status, duration_ms, schema_valid,
                     schema_errors, input_from, output, would_write, ids
schema_checks[5]     step, ok, errors
schema_valid         boolean
id_traceability      "full" | "partial"
id_traceability_checks[4]
recall               budget_tokens, budget_used, suppressed_count
```

Schema: `docs/contracts/evolution-pipeline-1.0.schema.json`.

## ID traceability

```text
trace_ingest.trace_id
  -> reflect_trigger.assessment.trace_id
  -> every candidate.source_trace_ids

reflect_trigger candidate_id
  -> memory_recall_v2 query/fixture
  -> recall item with the same trace_id and candidate_id content

curator_audit.skill_id
  -> skill_guard_check target skill

skill_guard_check.audit_event_id
  -> pipeline_report terminal ID (not a memory input)
```

`id_traceability` is `full` only when all four checks pass. The guard audit ID
is terminal by design; it is not accepted by `memory_recall_v2`.

## Additive fixes made during freeze

- `reflect_trigger.assessment.trace_id` is now explicit.
- Each distilled candidate now has `candidate_id` (`cand_` + 24 hex).
- Frozen output schemas include the actual v2.5.8 recall fields
  `namespace` and `agent_did`.

Input schemas were not changed except additive metadata-free fields already
present in the v2.5.4-v2.5.8 implementation.

## Frozen artifacts

```text
docs/contracts/frozen-evolution-tools-2.5.9.json
docs/contracts/FROZEN-EVOLUTION-TOOLS-2.5.9.sha256.json
docs/contracts/evolution-pipeline-1.0.schema.json
```

Frozen tools:

```text
trace_ingest
reflect_trigger
curator_audit
skill_guard_check
memory_recall_v2
```

Policy: v2.6.x consumes these schemas as-is. Additive changes require a new
freeze version; breaking changes require a versioned schema, not a silent
mutation.

## Acceptance

```text
five steps                 pass
schema checks              5/5 pass
id_traceability            full
duration                   <30s (local ~25ms)
writes                     0
would_write_count          2
stale candidate recall     candidate_id + trace_id present
```

Live Tencent dev evidence is generated after deployment.

## Deployment

```text
HTTP MCP root:  /opt/im2a/v2.5.9
HTTP MCP:       Node 0.9.0, 19 tools, 127.0.0.1:18082
Go MCP:         /opt/im2a/v2.5.9/bin/im2a-mcp and stable v2.5.2 path, 0.9.0
backup:         /opt/backups/im2a-mcp-pre-v2.5.9-20261031225245
```

Evidence:

```text
tests/TENCENT-V259-PIPELINE-EVIDENCE.json
tests/TENCENT-V259-DEPLOYMENT-EVIDENCE.json
tests/V259-FREEZE-UNIT-EVIDENCE.json
tests/V259-WINDOWS-MCP-EVIDENCE.json
```

## Boundaries

- The dry run uses deterministic synthetic fixtures, not production traces,
  skills or memories. It validates composition and schemas, not real data
  quality.
- `run_id` and generated fixture IDs are intentionally non-deterministic.
- Error envelopes are not part of the frozen output schemas; v2.6 in-process
  consumers should use the structured success objects. A machine-readable
  error envelope remains future work.
- `trace_ingest.content_sha256` is a record hash that includes the generated
  trace ID; it is not a stable idempotency key.
- `skill_manage -> curator.stage` owner identity and the full code-registry
  auth exchange remain open.
- Curator/operator separation, per-conversation seq, TLS and multi-tenant
  identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
