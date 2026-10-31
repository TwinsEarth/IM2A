# IM2A v2.6.8 — Dynamic Reserved Ratio and Governance Audit

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Scope

`reserved_ratio` is declared by Agent configuration and adjusted only by the
Harness scheduler governance layer. An Agent cannot write it through
`skill_manage`, `curator`, or scheduler tool arguments.

```text
base reserved_ratio       Agent config (bounded 0.10..0.30)
dynamic window            base ± 0.05
hard bounds               0.10..0.30
actor                     harness_scheduler_governance
```

## Metrics

`GoalBuffer.governanceMetrics()` provides:

```text
completion_rate =
  completed goal_events in the last 30 days
  / scheduled goal_events in the last 30 days

capacity_usage =
  pending + scheduled + executing goals / capacity

draft_count =
  goal_executions with a non-null draft patch metadata
```

`draft_to_skill_rate` is `null` and explicitly deferred to v2.7.x
`skill_claim`; v2.6.x only records draft telemetry.

## Rules

```text
sample gate:
  scheduled_count must be >= 5; otherwise unchanged

increase:
  completion_rate >= 0.80
  AND capacity_usage >= 0.95
  -> +0.05, capped at min(hard_max, base + 0.05)

decrease:
  completion_rate <= 0.30
  -> -0.05, capped at max(hard_min, base - 0.05)

otherwise:
  unchanged
```

The sample gate prevents a single scheduled cycle with no completions from
moving the ratio.

## Audit event

Every changed ratio appends a JSONL event to `scheduler-audit.jsonl`:

```json
{
  "schema_version": "1.0",
  "type": "reserved_ratio_adjustment",
  "at": "2026-11-01T...",
  "actor": "harness_scheduler_governance",
  "decision": "increase",
  "base_reserved_ratio": 0.15,
  "before_reserved_ratio": 0.15,
  "after_reserved_ratio": 0.2,
  "delta": 0.05,
  "metrics": {
    "completion_rate": 1,
    "scheduled_count": 8,
    "completed_count": 8,
    "active_goal_count": 8,
    "capacity": 8,
    "capacity_usage": 1,
    "draft_count": 0,
    "draft_to_skill_rate": null,
    "draft_to_skill_rate_note": "..."
  }
}
```

`SchedulerCycle` also returns `governance` and `base_reserved_ratio`, and
`scheduler_audit action=audit` reads the same log.

## Agent write refusal

`assertAgentReservedRatioWrite` recursively rejects any reserved_ratio key in
skill mutation or scheduler-governance arguments:

```text
RESERVED_RATIO_AGENT_WRITE_DENIED
```

The check runs before `skill_guard_check`, so an Agent cannot use a skill patch
to alter its own allocation.

## Acceptance

```text
seed 8 completed + 8 active scheduled at capacity 8
  -> completion_rate 1, capacity_usage 1
  -> ratio 0.15 -> 0.20
  -> audit event contains completion_rate and capacity_usage
agent skill_manage with reserved_ratio
  -> RESERVED_RATIO_AGENT_WRITE_DENIED
```

## Boundaries

- Dynamic adjustment is in-memory for the running scheduler; the audit log is
  durable. A restart returns to the configured base ratio.
- `draft_to_skill_rate` is not calculated until v2.7.x.
- The adjustment is not a network-wide allocation decision; it is local
  scheduler governance.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
