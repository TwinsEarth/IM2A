# IM2A v2.6.1 — Read-Only Drive Proposal

Date: 2026-11-01
Status: implemented; deployment and evidence files accompany the release.

## Scope

v2.6.1 turns a drive declaration plus current network signals into proposed
goals. It does not adopt them.

```text
drives/*.md
  + curator_audit / memory_recall_v2 current signals
  -> proposed_goals
  -> (v2.6.2 decides whether to write goal_buffer)
```

There is no `goal_buffer.put` call in `drive_propose`, no scheduler and no
skill write.

## Tool

`drive_propose` (surface 22 -> 23):

```json
{
  "agent_did": "did:im2a:...",
  "drive_ids": ["stale_skill_caretaker"],
  "urgency_hint": "high"
}
```

Output:

```json
{
  "proposed_goals": [
    {
      "goal_id": "goal_<24 hex>",
      "drive_id": "stale_skill_caretaker",
      "intent_summary": "patch skill:windows-build ...",
      "source_signals": [{ "type": "skill", "id": "windows-build" }],
      "estimated_token_cost": 2000,
      "confidence": 0.89
    }
  ],
  "evaluated_drives": [{ "drive_id": "...", "route": "curator", "matched": 1 }],
  "estimated_token_cost": 2000,
  "token_cost_basis": "recall_history_median",
  "generated_at": "...",
  "agent_did": "..."
}
```

`goal_id` is synthesized for the response only; it is not inserted into the
goal buffer.

## Routing

```text
description/trigger_hint mentions curator fields
  -> curator_audit(scope=agent_owned, agent_did)
description/trigger_hint mentions memory fields
  -> memory_recall_v2 with a query derived from trigger_hint
```

Curator fields used by the predicate parser:

```text
scope, lifecycle_state, patch_count, ageDays,
use_count_30d, recommended_action, estimated_context_cost_tokens
```

Memory fields:

```text
quality_flag, layer, score, source_trace_id, confidence
```

The predicate grammar is deliberately small and safe:

```text
field == value
field != value
field >= number
field <= number
field > number
field < number
conditions joined by AND
```

No JavaScript evaluation is used. Unknown fields and malformed conditions
fail closed with `DRIVE_INVALID`.

## Token-cost estimate

`memory_recall_v2` records each call in the local `recall_history` table inside
the goal-buffer SQLite database (not Memory Hub):

```text
budget_used, budget_tokens, item_count, suppressed_count, agent_did, at
```

`drive_propose` uses the median of the latest 50 `budget_used` values. If no
history exists, it falls back to the current proposal's own estimate and
reports `token_cost_basis="no_recall_history_fallback"`.

Acceptance reference:

```text
history budget_used       1938, 2100, 2000
median                    2000
actual reference          1938
deviation                 3.2%
```

## Acceptance (local)

```text
tool surface                       23
drive_propose present              PASS
proposed_goals                     >=1
goal.drive_id                      stale_skill_caretaker
goal.source_signals[0]             skill:stale-skill-a
estimated_token_cost               2000
token_cost_basis                   recall_history_median
goal_buffer goals before/after     0 / 0
estimated vs actual deviation      3.2%
```

## Boundaries

- No goal-buffer write; adoption is v2.6.2.
- No scheduler and no execution.
- Memory-backed drives use a query derived from `trigger_hint`; this is
  best-effort until drives gain an explicit search field.
- The v2.5.8 recall output does not expose an `item_id`; memory-backed
  `source_signals` use `source_trace_id` when present, otherwise a derived
  `mem_...` ID. Curator-backed signals use the exact `skill_id`.
- `description` predicates are evaluated by a small parser, not a general
  expression language.
- `urgency_hint` only adjusts confidence; it does not schedule work.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS, multi-tenant identity and production readiness remain open.
