# IM2A v2.6.6 — Goal Buffer Persistence and Restart Recovery

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Scope

`goal_buffer` is persisted in an Agent-local SQLite database separate from
Memory Hub. Intent metadata survives process restart, plugin hot update and
device migration. Draft bodies do not enter SQLite.

```text
goal-buffer.db   intent metadata + status/audit + execution telemetry
drafts/          candidate patch bodies (filesystem)
Memory Hub       untouched by goal buffer persistence
```

## Persisted intent metadata

The `goals` table stores:

```text
goal_id
drive_id
proposed_at
intent_summary
source_signals_json
priority_score
status
created_at
updated_at
```

`goal_executions` stores only draft metadata (draft_id, target_skill_id, path,
sha256, bytes, applied). `sanitizeDraftPatch` strips any `content`/body field
before writing. `draft_patch`正文 never enters the SQLite file.

## Startup recovery

On `GoalBuffer` construction:

```text
executing -> pending     execution context was lost
scheduled -> scheduled   scheduler will allocate reserved budget later
pending   -> pending
completed -> unchanged   not restored as active
expired   -> unchanged   not restored as active
```

Each downgrade writes a `goal_events` row:

```text
actor=startup_recovery
reason=execution_context_lost
details={ source_signals, previous_status: "executing" }
```

`GoalBuffer.lastRecovery` and the `goal_buffer action=recover` tool action
return:

```json
{
  "downgraded_goal_ids": ["goal_..."],
  "pending_count": 4,
  "scheduled_count": 0,
  "completed_count": 3,
  "expired_count": 0,
  "executing_count": 0,
  "completed_or_expired_restored": false
}
```

## Recovery semantics

- A restarted Agent can query all pending/scheduled goals from SQLite.
- `executing` is never silently resumed; it returns to `pending` for a new
  scheduling decision.
- Completed/expired rows remain for audit but are not active/restored.
- Draft files remain the source of candidate patch bodies.

## Acceptance

```text
before restart: 3 pending + 1 executing
after restart:  4 pending, 0 executing
recovery report contains downgraded goal_id
recovery audit contains source_signals
draft body marker absent from goal-buffer.db
draft body marker present in drafts/
completed/expired not restored as active
```

## Boundaries

- Persistence is local SQLite, not Memory Hub.
- Draft body encryption/ACL is outside this version; the database excludes it.
- `goal_buffer` remains a local state tool without caller authentication.
- A scheduled goal survives restart, but the scheduler's in-memory external
  queue does not (v2.6.2 boundary).
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
