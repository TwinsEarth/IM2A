# IM2A v2.5.6 — Read-only Curator Lifecycle Audit

Date: 2026-10-31
Status: implemented and deployed to the Tencent dev host; not production-ready.

## Scope

v2.5.6 makes skill-library metabolism observable without giving the system
permission to prune it. It answers:

> Which skills are rotting, duplicated, expensive in context, or unused?

It never archives, merges, deletes or promotes. `recommended_action` is advice,
not an execution instruction. There is deliberately no `curator_execute` tool.

## MCP surface

Tool `curator_audit` (surface 15 -> 16):

```json
{
  "agent_did": "did:im2a:...",
  "scope": "agent_owned | network",
  "since": "2026-10-01T00:00:00Z"
}
```

All inputs are optional. `scope` defaults to `network`; `agent_owned` requires
`agent_did`. `since` keeps skills whose last use or active-version update is at
or after the timestamp.

Output:

```json
{
  "skills": [
    {
      "skill_id": "release-gate",
      "name": "release-gate",
      "lifecycle_state": "active | stale | archived",
      "last_used_at": "2026-10-26T12:00:00.000Z",
      "use_count_30d": 1,
      "patch_count": 0,
      "estimated_context_cost_tokens": 37,
      "recommended_action": "keep | flag_for_review | merge_candidate | archive_candidate"
    }
  ],
  "audit": {
    "generated_at": "2026-10-31T12:00:00.000Z",
    "scope": "network",
    "agent_did": null,
    "since": null,
    "thresholds": {
      "staleDays": 30,
      "archiveDays": 90,
      "mergeSimilarity": 0.8
    }
  }
}
```

## Lifecycle semantics

```text
explicit promoted archive       -> archived
unused for >= 90 days           -> stale + archive_candidate
unused for >= 30 days           -> stale + flag_for_review
otherwise                       -> active + keep
```

A 90-day skill is **not** automatically archived; it is a candidate. The
`archived` state is only reached by an explicit archive proposal that was
promoted. There is no hard-delete path and no automatic restore mutation.

`merge_candidate` is advisory and computed from description-token similarity
(default Jaccard >= 0.8), not from a write action. Archive candidates take
priority over merge candidates.

## Usage and cost

- `skill_usage` records `skill_view` loads with timestamp and agent reference;
  `use_count_30d` counts rows in the last 30 days.
- `last_used_at` is the latest usage timestamp, falling back to the active
  version's update time when the skill has never been viewed.
- `patch_count` counts patch proposals (including proposed/staged, not only
  promoted ones).
- `estimated_context_cost_tokens` is the UTF-8 byte length of the L0 disclosure
  object `{id, description, version, packageSha, tags}` divided by 4, rounded
  up. This is the same shape returned by `skills_list`; the test compares it
  with the actual serialized L0 item and requires <=10% deviation.

## Read-only proof

- `curator_audit` calls only `SELECT` statements.
- The test captures active skills, proposal count and ledger count before and
  after; all are identical.
- No `curator_execute` tool; no archive/merge/promote function is called.
- The existing skill write path remains proposal-only; promotion is still an
  operator action.

## Acceptance

```text
6-skill library lifecycle distribution:
  active-skill        active      keep
  stale-skill         stale       flag_for_review
  archive-skill       stale       archive_candidate
  archived-skill      archived    keep
  merge-a             active      merge_candidate
  merge-b             active      merge_candidate

use_count_30d / patch_count          PASS
L0 token estimate vs actual L0       within 10%
agent_owned scope                    only the owned skill
since filter                         only recent skill
read-only active/proposals/ledger    unchanged
invalid scope / missing DID / date   fail closed
2 tests, 2 passed
```

Live Tencent dev evidence is generated after deployment; see the evidence files
listed below.

## Deployment

```text
HTTP MCP root:  /opt/im2a/v2.5.6
HTTP MCP:       Node 0.6.0, 16 tools, 127.0.0.1:18082
Go MCP:         /opt/im2a/v2.5.6/bin/im2a-mcp and stable v2.5.2 path, 0.6.0
backup:         /opt/backups/im2a-mcp-pre-v2.5.6-20261031191556
```

Evidence:

```text
tests/TENCENT-V256-CURATOR-EVIDENCE.json
tests/TENCENT-V256-DEPLOYMENT-EVIDENCE.json
tests/V256-CURATOR-UNIT-EVIDENCE.json
tests/V256-WINDOWS-MCP-EVIDENCE.json
```

## Boundaries

- `recommended_action` is advisory only.
- Ownership is partial: `skill_owners` plus the last proposal proposer; the
  full v2.9.x Skill Node ownership model is not built yet.
- Cross-agent/peer ownership and cryptographic identity remain future work.
- Lifecycle state is based on local usage telemetry; a skill viewed outside
  this bridge may appear staler than it is.
- No automatic archive/merge/delete; no hard deletion.
- Curator/operator separation and per-conversation seq remain open P0s.
- Not a production/TLS/multi-tenant claim.
