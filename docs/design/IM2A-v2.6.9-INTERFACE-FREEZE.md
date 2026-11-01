# IM2A v2.6.9 — v2.7.x Interface Freeze

Date: 2026-11-01
Status: frozen by v2.6.9; v2.7.x skill_claim and contribution_log consume these
interfaces without changing v2.6.x schemas.

## Frozen enums

`goal_buffer.status` (verbatim, order included):

```text
pending, scheduled, executing, completed, expired
```

`execution_result.recommended_next` (verbatim, order included):

```text
crystallize, patch, discard
```

The machine-readable copy is
`docs/contracts/v2.7-interface-freeze-1.0.json`; the implementation constants
are `GOAL_STATUSES` in `goal-buffer.mjs` and `RECOMMENDED_NEXT` in
`goal-executor.mjs`.

## Frozen handoff

```text
goal_buffer            docs/contracts/goal-buffer-1.0.schema.json
execution_result       docs/contracts/execution-result-1.0.schema.json
skill_claim input      docs/contracts/skill-claim-1.0.schema.json
contribution_log ref   goal_id
```

v2.7.x `skill_claim` may consume:

```text
execution_result.draft_patch
execution_result.recommended_next
execution_result.goal_id
goal_buffer.source_signals
goal_buffer.drive_id
```

`contribution_log` may cite `goal_id` as the provenance of an autonomous
improvement intent.

## Autonomy dry-run

`autonomy_pipeline_dryrun` runs:

```text
drive_propose
  -> goal_schedule in a temporary SQLite buffer and temporary drafts/
  -> simulated skill_claim schema validation
```

No production goal, skill, drive or draft is written.

`autonomy_report` records:

```text
per-step duration
goal_buffer / execution_result / skill_claim schema validation
signal_traceability = full | partial
frozen enums
under_45_seconds
```

## Boundary

This milestone freezes interfaces, not autonomy completeness. v2.7.x still owns
claim, ownership, contribution accounting and the actual durable skill write.
Curator/operator separation, per-conversation seq, full auth-code exchange, TLS
and multi-tenant identity remain open P0s. Not a production/TLS/multi-tenant
claim.
