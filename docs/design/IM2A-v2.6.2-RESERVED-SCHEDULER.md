# IM2A v2.6.2 — Reserved Ratio and Idle Detection

Date: 2026-11-01
Status: implemented; deployment/evidence recorded in the v2.6.2 evidence files.

## Scope

v2.6.2 defines the scheduler-side hard constraint for autonomous work and a
structural idle test. It does not execute a skill patch or curate Memory Hub.

```text
cycle budget
  ├─ external_pool  = total - floor(total * reserved_ratio)
  └─ reserved_pool  = floor(total * reserved_ratio)
```

Default `reserved_ratio=0.15`; accepted range `0.10..0.30`. Configuration is
environment-driven:

```text
IM2A_RESERVED_RATIO             0.15
IM2A_CYCLE_BUDGET_TOKENS        100000
IM2A_RESERVED_TASK_TOKENS       2000
IM2A_SCHEDULER_AUDIT            <DB_DIR>/scheduler-audit.jsonl
```

## External pool

External requests are submitted with `{request_id, tokens}`. They are processed
in FIFO order while `external_used + tokens <= external_limit`. Any remainder
stays queued for a later cycle. External work can never consume
`reserved_pool`.

## Idle detection

Idle is structural, not a timer:

```text
idle = (queue length + incoming external requests) == 0 at cycle start
```

If idle and a pending goal exists, the scheduler selects:

```text
pending goals ordered by priority_score DESC, proposed_at ASC
```

It then transitions that goal `pending -> scheduled` and consumes a bounded
reserved allocation (default 2000 tokens). This transition is the temporary
v2.6.3 `goal_schedule` seam; v2.6.3 will own the richer scheduling contract.

Reserved priority order:

```text
1. highest-priority pending goal
2. skill patch validation
3. Memory Hub curation
```

v2.6.2 implements priority 1. Priorities 2 and 3 are represented as future
reserved task types but are not executed here.

## Audit

Every cycle appends one JSONL record with separate objects:

```json
{
  "cycle_id": "sched_...",
  "external": {
    "limit": 85000, "used": 80000, "remaining": 5000,
    "processed": [...], "queued": [...], "queue_depth": 2
  },
  "reserved": {
    "limit": 15000, "used": 0, "remaining": 15000, "tasks": []
  },
  "idle": false,
  "scheduled_goal_id": null
}
```

The external and reserved trajectories are never merged into a single usage
number. `scheduler_audit action=audit` reads the append-only log;
`scheduler_audit action=state` reads config, queue and next pending goal.

## Tools

```text
23 -> 25
scheduler_cycle   run one cycle
scheduler_audit   state or append-only audit
```

## Acceptance

```text
external-heavy cycle
  external limit              85000
  external used               80000
  queued                       2
  reserved used                0
  goal statuses                pending / pending

drain cycle
  external used               20000
  queue depth                  0
  reserved used                0

idle cycle
  idle                         true
  scheduled highest goal       0.9 priority
  reserved used                2000
  reserved remaining           13000
  audit events                 3
  external/reserved separate   true
```

## Boundaries

- External queue is in-memory for this version; it does not survive a
  scheduler restart.
- The reserved allocation is a bounded scheduling charge, not measured model
  execution cost.
- No skill patch validation or Memory Hub curation is executed in v2.6.2.
- The `pending -> scheduled` transition is a v2.6.3 seam.
- Scheduler tools are local state tools; caller identity enforcement for them
  remains future work.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
