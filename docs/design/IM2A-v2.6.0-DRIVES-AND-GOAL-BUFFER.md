# IM2A v2.6.0 — Durable Drives and Local Goal Buffer

Date: 2026-11-01
Status: implemented; deployment/evidence recorded in the v2.6.0 evidence files.

## Scope

v2.6.0 installs the physical container for autonomous intent, but does not
generate intent. There is no `drive_propose`, no scheduler and no automatic
skill write in this version.

```text
drives/*.md          durable concern declarations (local files)
goal_buffer          local SQLite intent objects + append-only transition log
Memory Hub           deliberately untouched by the goal buffer
```

## Drives declaration

Each optional `drives/<drive_id>.md` uses YAML frontmatter:

```yaml
---
drive_id: stale_skill_caretaker
description: "scope == self_owned_skills AND lifecycle_state == stale AND patch_count == 0 AND ageDays >= 14"
priority: 0.7
trigger_hint: "curator_audit.lifecycle_state == stale"
scope: self_owned_skills
---
```

Rules:

```text
drive_id       ^[a-z][a-z0-9_]{2,63}$
description    filter expression over curator_audit / memory_recall_v2 output, not a slogan
priority       0..1
trigger_hint   scheduling hint, never an execution instruction
scope          self_owned_skills
```

The loader rejects unknown keys, duplicate keys, invalid IDs, invalid priority,
invalid scope and malformed frontmatter. Duplicate `drive_id` across files is
rejected.

Example checked in at
`packages/agent-plugins/source/drives/stale_skill_caretaker.md`.

Directory selection:

```text
IM2A_DRIVES_DIR   defaults to <DB_DIR>/drives
```

## Drive write gate

`drive_manage` is the only drive write path. At handler entry it calls the
v2.5.7 guard:

```text
operation          patch
target_skill_id    drive_id
session            verified x-im2a-skill-session
```

Public or read-only sessions are denied with `insufficient_scope`; the guard
appends the normal hash-chained audit event. Only a session with `skill:write`
and authorization level L2 can reach the atomic file write.

## Goal buffer

The goal buffer is a separate local SQLite database
(`IM2A_GOAL_BUFFER_DB`, default `<DB_DIR>/goal-buffer.db`). It is not a
Memory Hub table and never enters the network-level memory namespace.

Goal object:

```json
{
  "goal_id": "goal_<24 hex>",
  "drive_id": "stale_skill_caretaker",
  "proposed_at": "2026-11-01T00:00:00.000Z",
  "intent_summary": "Review stale self-owned skills.",
  "source_signals": [{ "type": "skill", "id": "stale-skill-a" }],
  "priority_score": 0.8,
  "status": "pending",
  "created_at": "...",
  "updated_at": "..."
}
```

Status machine:

```text
pending   -> scheduled | expired
scheduled -> executing | expired
executing -> completed | expired
completed -> terminal
expired   -> terminal
```

Every create and transition writes a `goal_events` row with from/to status,
actor, reason and timestamp. `goal_buffer action=audit` is the external audit
query. There is no silent status mutation.

## Tools

```text
19 -> 22
drive_list      read-only
drive_manage    guarded write (skill_guard_check)
goal_buffer     action = put | transition | query | audit
```

The goal buffer itself is local state, so `goal_buffer` does not call the skill
guard. It also does not write Memory Hub.

## Persistence

```text
drives/              files, atomic replace
goal-buffer.db       SQLite goals + goal_events
Memory Hub           unchanged
```

## Acceptance (local)

```text
tool surface                       22
drive sample loads                 PASS
drive writes public                denied insufficient_scope
denied drive audit                 persisted, hash chain valid
L2 drive write                     allowed + audit_event_id
drive_list                         1 declaration
goal buffer put                    pending
pending -> scheduled -> executing -> completed  PASS
goal audit                         4 events
goal query completed               1
memory rows before/after           0 / 0
public drive file                   absent
```

Live Tencent evidence is recorded after deployment.

```text
drives dir:  /opt/im2a/v2.6.0/data/drives
goal DB:     /opt/im2a/v2.6.0/data/goal-buffer.db
backup:      /opt/backups/im2a-mcp-pre-v2.6.0-20261101032423
```

Evidence:

```text
tests/TENCENT-V260-DRIVES-EVIDENCE.json
tests/TENCENT-V260-DEPLOYMENT-EVIDENCE.json
tests/V260-DRIVES-UNIT-EVIDENCE.json
tests/V260-WINDOWS-MCP-EVIDENCE.json
```

## Boundaries

- `description` is a declarative filter string; v2.6.0 does not evaluate it.
- `trigger_hint` is not executed; no scheduler is connected.
- No intent is generated automatically in this version.
- Goal IDs and drive IDs are local; no cross-agent federation.
- The goal buffer is writable through the local MCP tool; caller identity
  enforcement for local buffer writes is future work.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
