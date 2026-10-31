# IM2A v2.5.8 — Layered Memory Recall with Hard Budget

Date: 2026-10-31
Status: implemented and deployed to the Tencent dev host; not production-ready.

## Scope

v2.5.8 turns memory retrieval from an unfiltered FTS lookup into a layered,
quality-aware, budgeted recall pipeline. It is read-only: no memory is created,
promoted or deleted by this path.

The v2.7.x recall formula (`usage_frequency`, `patch_depth`, `origin_credit`)
will consume these two foundations: layer routing and per-item quality signals.

## MCP surface

Tool `memory_recall_v2` (surface 17 -> 18):

```json
{
  "query": "release",
  "agent_did": "did:im2a:...",
  "budget_tokens": 2000,
  "layers": ["skill_description", "atom"],
  "include_stale": false
}
```

`layers` is optional; omitted means all layers. `include_stale` defaults to
false.

Output:

```json
{
  "items": [
    {
      "content": "title\nbody",
      "layer": "skill_description",
      "score": 0.82,
      "quality_flag": "fresh",
      "source_trace_id": "trc_..."
    }
  ],
  "budget_used": 1984,
  "suppressed_count": 31,
  "namespace": "tencent-dev",
  "agent_did": "did:im2a:..."
}
```

Each item has exactly one quality flag:

```text
fresh
stale          atom not updated for >30 days
conflicted     same conflict_key has a higher-confidence source
low_confidence confidence < 0.4
```

## Pipeline

```text
parse    normalize query, budget, layer set
route    select requested layers; all by default
gather   FTS5 query, lexical fallback, limit 200
rerank   layer priority + confidence + relevance + recency
diversify layer priority ordering (skill_description first)
suppress stale atoms (unless include_stale), raw_event cap, budget overflow
budget   hard sum of ceil(utf8_bytes(content)/4)
```

Score:

```text
score = 0.45*layerPriority
      + 0.25*confidence
      + 0.20*relevance
      + 0.10*recency
```

Layer priority:

```text
skill_description 1.00
atom              0.80
entity_page       0.55
raw_event         0.25
```

Raw events are capped at 15% of the token budget and are the first layer
suppressed when the budget is tight.

## Storage migration

`memory` and `memory_proposals` gained:

```text
updated_at       INTEGER
layer            TEXT NOT NULL DEFAULT 'atom'
source_trace_id  TEXT
confidence       REAL NOT NULL DEFAULT 0.5
conflict_key     TEXT
```

Existing rows are backfilled with `updated_at = created_at` and `layer = atom`.
`memory_fts` remains the retrieval index; no new FTS table is created.

## Acceptance

Local 50+ namespace test:

```text
memory count                        >=50
budget_tokens                       2000
budget_used                         <=2000
suppressed_count                    >0
stale in top-3                      none
include_stale=true                  stale visible
conflicted / low_confidence         marked
layer filter                        only requested layer
read-only                           memory count unchanged
```

Live Tencent dev evidence is generated after deployment; see the evidence
files listed below.

## Deployment

```text
HTTP MCP root:  /opt/im2a/v2.5.8
HTTP MCP:       Node 0.8.0, 18 tools, 127.0.0.1:18082
Go MCP:         /opt/im2a/v2.5.8/bin/im2a-mcp and stable v2.5.2 path, 0.8.0
backup:         /opt/backups/im2a-mcp-pre-v2.5.8-20261031210843
```

Evidence:

```text
tests/TENCENT-V258-RECALL-EVIDENCE.json
tests/TENCENT-V258-DEPLOYMENT-EVIDENCE.json
tests/V258-RECALL-UNIT-EVIDENCE.json
tests/V258-WINDOWS-MCP-EVIDENCE.json
```

## Boundaries

- Quality flags are metadata-derived; no LLM second pass.
- `conflicted` requires an explicit `conflict_key`; same-title inference is not
  used.
- `agent_did` is carried as context/audit data; memory rows remain tenant-scoped
  rather than agent-scoped.
- `source_trace_id` is optional for legacy rows.
- The token count is a deterministic byte/4 estimate, not a model tokenizer.
- Not a production/TLS/multi-tenant claim.
- Curator/operator separation and per-conversation seq remain open P0s.
