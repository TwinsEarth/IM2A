# IM2A v2.6.4 — Drive Calibration

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Scope

`drive_calibrate` is a read-only calibration pass over an Agent's local goal
history. It produces suggestions; it never edits `drives/*.md`.

```text
goal_buffer history + goal_executions + goal_proposals
  -> per-drive metrics
  -> calibration_suggestions
  -> (Agent must use drive_manage + skill_guard_check to apply anything)
```

## Tool

```json
{
  "agent_did": "did:im2a:...",
  "lookback_days": 14
}
```

`lookback_days` defaults to 14 and is bounded to 1..90.

Output:

```json
{
  "schema_version": "1.0",
  "agent_did": "...",
  "lookback_days": 14,
  "window": { "from": "...", "to": "..." },
  "drive_count": 1,
  "calibration_suggestions": [
    {
      "drive_id": "stale_skill_caretaker",
      "priority_adjustment": "increase",
      "priority_delta": 0.05,
      "trigger_hint_refinement": "...",
      "evidence": [
        { "goal_id": "goal_...", "status": "completed", "confidence_delta": 0.3, "execution_yield": true }
      ],
      "metrics": {
        "goal_count": 10,
        "completed_count": 8,
        "expired_count": 2,
        "execution_count": 10,
        "proposal_acceptance_rate": 0.8,
        "execution_yield": 0.8,
        "expiry_rate": 0.2,
        "mean_confidence_delta": 0.175,
        "confidence_delta_distribution": [0.4, 0.35, "..."]
      }
    }
  ],
  "read_only": true,
  "drives_modified": false
}
```

## Metrics

```text
proposal_acceptance_rate = adopted proposals / proposals
execution_yield          = executions with draft_patch or valid reflection_notes / executions
expiry_rate              = expired goals / goals
mean_confidence_delta    = mean of execution confidence_delta values
```

## Adjustment rule

```text
decrease  if expiry_rate >= 0.40
          or mean_confidence_delta < 0
          or proposal_acceptance_rate < 0.30
increase  if goal_count > 0
          and completed_count >= 3
          and execution_yield >= 0.50
          and mean_confidence_delta >= 0.15
          and (proposal_acceptance_rate is null or >= 0.30)
unchanged otherwise
```

`priority_delta` is `-0.05`, `+0.05`, or `0`. The suggestion is explanatory,
not an automatic priority mutation.

## Evidence and application boundary

Every suggestion carries up to 20 `evidence` entries with exact `goal_id`,
status, execution `confidence_delta`, and `execution_yield`. The evidence is
traceable to the local goal buffer.

Applying a suggestion is a separate write:

```text
drive_manage -> skill_guard_check(operation=patch, target_skill_id=drive_id)
```

Only an L2 `skill:write` session can apply it. `drive_calibrate` does not call
`skill_guard_check`, write a drive, or change a goal.

## Telemetry added

The local goal-buffer SQLite database now has:

```text
goal_proposals   proposal_goal_id, drive_id, proposed_at, intent_summary, adopted_goal_id
goal_executions  goal_id, drive_id, status, confidence_delta, draft_patch, reflection_notes,
                 recommended_next, execution_yield
```

`drive_propose` records proposals; `goal_buffer put` may link adoption through
`proposal_id`; `goal_schedule` records the execution result. None of this is
written to Memory Hub.

## Acceptance

```text
at least 10 goal_buffer entries
at least one priority_adjustment != unchanged
evidence contains goal_id and confidence_delta
no drives/ file modified
read_only=true, drives_modified=false
```

## Boundaries

- Metrics are local-window telemetry, not organization-wide truth.
- Thresholds are fixed policy constants in v2.6.4; no LLM calibration.
- `proposal_acceptance_rate` is null when there were no proposals.
- A suggestion is not a drive mutation.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
