# IM2A v2.6.3 — Goal Schedule and Draft-Only Execution

Date: 2026-11-01
Status: implemented; deployment/evidence recorded in the v2.6.3 evidence files.

## Scope

v2.6.3 connects a pending goal to a bounded execution. Execution may read
signals and write a candidate draft, but it cannot write the skill library.

```text
pending -> scheduled -> executing -> completed
                         \-> expired (failure)
```

The `pending -> scheduled` transition is the v2.6.3 goal_schedule seam that
v2.6.2 previously left open.

## Tool

`goal_schedule` (surface 25 -> 26):

```json
{
  "agent_did": "did:im2a:...",
  "goal_id": "goal_<24 hex>",
  "budget_tokens": 2000
}
```

Budget source is `reserved_pool`; the scheduler rejects the call when the
external queue is non-empty or the requested budget exceeds the reserved
limit.

Output:

```json
{
  "goal": { "status": "completed", "...": "..." },
  "execution_result": {
    "draft_patch": {
      "draft_id": "draft_...",
      "target_skill_id": "stale-skill-a",
      "path": "/opt/im2a/v2.6.3/data/drafts/goal_.../stale-skill-a.patch.md",
      "sha256": "...",
      "bytes": 812,
      "applied": false
    },
    "reflection_notes": "...",
    "confidence_delta": 0.38,
    "recommended_next": "crystallize",
    "recommendation_reason": "...",
    "budget": { "requested": 2000, "used": 250, "source": "reserved_pool" },
    "read_only_tools": ["curator_audit", "memory_recall_v2"],
    "drafted_skill_ids": ["stale-skill-a"]
  }
}
```

## Execution boundary

Allowed:

```text
curator_audit
memory_recall_v2
read-only goal/skill/memory lookups
restricted skill_patch_draft -> drafts/
```

Not allowed:

```text
skillStore.manage
skillStore.stage
skillStore.promote
skillStore.rollback
skills/ file writes
Memory Hub writes
```

The draft is written under:

```text
IM2A_DRAFTS_DIR   default <DB_DIR>/drafts
```

The file is a candidate artifact with `applied=false`; v2.7.x ownership decides
whether it can become a crystallized skill.

## Recommendation rule

```text
confidence_delta < 0                 -> discard
confidence_delta >= 0.15 + draft     -> crystallize (recommendation only)
otherwise                            -> patch
```

A negative delta can never recommend `crystallize`. The delta is derived from
goal priority and whether the source skill was matched by curator_audit:

```text
delta = round((priority_score - 0.55) * 0.8 + signal_boost)
```

## Acceptance

```text
missing goal / unavailable reserved pool   fail closed
pending goal                               -> scheduled -> executing -> completed
draft_patch                                exists under drafts/
skills/ write                              none
active skill package sha                   unchanged
goal event statuses                        pending, scheduled, executing, completed
positive confidence delta                  crystallize (draft only)
negative confidence delta                  discard
```

## Boundaries

- `session_search` is listed as an allowed read-only capability but is not
  wired in this bridge version; the executor records the read-only tools it
  actually used.
- The draft is not a valid unified diff and is not applied.
- `crystallize` is a recommendation label only; no skill is crystallized.
- Reserved allocation is bounded by the scheduler's reserved limit but is not
  a metered model execution.
- `goal_schedule` is a local state tool and does not yet authenticate the
  caller.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
