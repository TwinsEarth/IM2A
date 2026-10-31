# IM2A v2.6.5 — Goal Priority Arbitration and Intent Deduplication

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Scope

v2.6.5 filters goals at the `goal_buffer` admission boundary. It does not add a
new MCP tool; it changes `goal_buffer put`.

```text
signal
  -> priority_score = drive.priority × signal_strength × recency_factor
  -> duplicate check against active goals (threshold 0.85)
  -> merge or insert
  -> capacity arbitration (default 8 active goals)
```

## Priority score

`drive_propose` now returns:

```text
priority_score
signal_strength
recency_factor
```

The server computes `recent_intent_count` per drive from active goals and
passes it into proposal generation:

```text
recency_factor = 1 / (1 + recent_intent_count * 0.25)
signal_strength = 0.55
                + (use_count_30d == 0 ? 0.35 : 0.10)
                + (patch_count == 0 ? 0.10 : 0)
priority_score  = drive.priority × signal_strength × recency_factor
```

The goal buffer trusts the supplied `priority_score` for arbitration; it does
not recompute drive policy.

## Capacity

```text
IM2A_GOAL_BUFFER_CAPACITY default 8 (1..100)
```

Active statuses are `pending`, `scheduled`, `executing`. Completed and expired
goals do not consume capacity.

When admission would exceed capacity, the lowest-priority `pending` or
`scheduled` goal is transitioned to `expired`. The goal row retains its
`source_signals`; the `goal_events` expiry record also stores:

```json
{
  "details": {
    "source_signals": [{ "type": "skill", "id": "..." }],
    "priority_score": 0.2,
    "capacity": 8
  }
}
```

No signal is silently dropped.

## Deduplication

At admission, the new `intent_summary` is compared with active goals through
`MemoryHub.textSimilarity` (a local token embedding fallback; no external
embedding model is configured in this version).

```text
similarity >= 0.85
  -> merge into the most similar active goal
  -> source_signals = union(existing, incoming)
  -> priority_score = max(existing, incoming)
  -> goal_merges row + goal_events actor=goal_dedup
  -> no second goal row
```

The merge is auditable through `goal_buffer action=audit`; the merged goal
keeps one `goal_id`, while all distinct source signals are retained.

## Tools

No new tool. Surface remains 27.

```text
goal_buffer put -> admit (dedup + capacity)
goal_buffer query / audit -> inspect retained goals/events
```

## Acceptance

```text
capacity 3 with four intents
  -> one expired
  -> expired goal source_signals retained in goal_events.details
two overlapping intents
  -> one active goal
  -> source_signals union count equals distinct pre-merge sum
  -> merged goal_merges row == 1
```

## Boundaries

- Similarity is a deterministic local token embedding, not a model embedding
  service; 0.85 is a policy threshold.
- Capacity applies to active goals only.
- Expiring a goal preserves its row and audit event but does not execute it.
- Applying a proposal still requires `goal_buffer put`; applying a drive change
  still requires `drive_manage` + `skill_guard_check` L2.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
