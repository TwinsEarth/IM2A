# IM2A v2.5.7 — Skill-Write Guard and Audit Chain

Date: 2026-10-31
Status: implemented and deployed to the Tencent dev host; not production-ready.

## Scope

v2.5.7 puts the write boundary at the MCP handler, before any future
`skill_crystallize`, `skill_claim` or `drive_propose` write path exists.

```text
public code / read-only session  -> never writes
skill:read                       -> read-only tools only
skill:write + authorization L2   -> write gate may pass
```

The gate is not in Hermes or any Python process. `skill_manage` and
`curator.stage` call `skill_guard_check` at the outer handler entry, before
calling the skill store.

## MCP surface

Tool `skill_guard_check` (surface 16 -> 17):

```json
{
  "agent_did": "did:im2a:...",
  "operation": "create | patch | archive",
  "target_skill_id": "skill-id"
}
```

**No `requested_scope` input.** The requested scope is derived from the
operation (`skill:write`); the granted scope and level come from a
server-verified session, never from the caller.

Output:

```json
{
  "allowed": false,
  "denied_reason": "insufficient_scope",
  "audit_event_id": "evt_..."
}
```

`denied_reason` is one of:

```text
insufficient_scope
scope_ceiling_exceeded
cross_agent_write_blocked
```

## Session model

The HTTP MCP reads `x-im2a-skill-session`, an HMAC-SHA256-signed,
short-lived session token. It carries:

```text
agent_did
authorization_level (L0/L1/L2/L3)
scopes (skill:read, skill:write, ...)
session_id
subject_kind
expires_at
```

Missing, invalid or expired tokens fall back to a **public session** and deny
all writes. The Go stdio MCP forwards its configured
`IM2A_SKILL_GUARD_SESSION_TOKEN` as the same header.

This version deliberately uses a signed local session token rather than
trusting a caller-provided scope or a public code. Public-code presentation is
the no-session/public path; full `CodeRegistry`/gateway auth code exchange is
future work.

## Decision rules

```text
public / L0 / empty scopes       -> insufficient_scope
missing skill:write              -> insufficient_scope
level below L2                   -> scope_ceiling_exceeded
agent DID mismatch               -> cross_agent_write_blocked
target skill owner mismatch      -> cross_agent_write_blocked
skill:write + L2 + owner match   -> allowed
```

For create, an unknown target owner is acceptable; for patch/archive, a known
foreign owner is blocked. Owner lookup uses `skill_owners`, falling back to the
latest proposal proposer.

## Audit chain

Every check appends one line to
`/opt/im2a/v2.5.7/data/skill-guard/audit.jsonl`:

```json
{
  "schema_version": "1.0",
  "audit_event_id": "evt_...",
  "request": {
    "agent_did": "...",
    "operation": "create",
    "target_skill_id": "...",
    "requested_scope": "skill:write",
    "session_id": "...",
    "subject_kind": "public_code | agent",
    "authorization_level": "L0 | L2"
  },
  "decision": {
    "allowed": false,
    "denied_reason": "insufficient_scope",
    "policy_version": "skill-guard-1.0"
  },
  "approval": { "required": false, "status": "not_required" },
  "result": { "status": "denied", "at": "..." },
  "prev_hash": "<hex>",
  "hash": "<sha256 over canonical event without hash>"
}
```

The four sections are `request -> decision -> approval -> result`. The file is
append-only and `fsync`ed; `verifyAuditChain` recomputes every hash and every
`prev_hash`. Tampering is detectable. No raw token or code is stored.

## Handler integration

```text
skill_guard_check  -> SkillGuard.check (audit write)
skill_manage       -> SkillGuard.check -> skillStore.manage
curator.stage      -> proposal lookup -> SkillGuard.check -> skillStore.stage
```

Read-only tools (`skills_list`, `skill_view`, `curator_audit`,
`reflect_trigger`) do not receive a write path.

## Acceptance

Local unit tests:

```text
public code -> insufficient_scope + persisted audit       PASS
L2 skill:write -> allowed + traceable audit_event_id       PASS
skill:read -> insufficient_scope                           PASS
L1 with skill:write -> scope_ceiling_exceeded              PASS
agent mismatch / foreign target -> cross_agent_write_blocked PASS
signed session forge/expiry                                PASS
audit hash chain + tamper detection                        PASS
invalid operation before audit append                      PASS
6 tests, 6 passed
```

Live Tencent dev:

```text
HTTP MCP tools/list                         17
public guard                                denied insufficient_scope
L2 guard                                    allowed
denied skill_manage                         isError=true, no proposal
allowed skill_manage                        proposal written once
audit chain                                 ok, 5 events verified
Go MCP 0.7.0 tools/list                     17
Go guard with session header                allowed
Windows Go MCP 0.7.0 tools/list             17
```

## Deployment

```text
HTTP MCP root:  /opt/im2a/v2.5.7
HTTP MCP:       Node 0.7.0, 17 tools, 127.0.0.1:18082
Go MCP:         /opt/im2a/v2.5.7/bin/im2a-mcp and stable v2.5.2 path, 0.7.0
guard audit:    /opt/im2a/v2.5.7/data/skill-guard/audit.jsonl
backup:         /opt/backups/im2a-mcp-pre-v2.5.7-20261031201610
```

Evidence:

```text
tests/TENCENT-V257-GUARD-EVIDENCE.json
tests/TENCENT-V257-DEPLOYMENT-EVIDENCE.json
tests/V257-SKILL-GUARD-UNIT-EVIDENCE.json
tests/V257-WINDOWS-MCP-EVIDENCE.json
```

## Boundaries

- The signed session token is a local trusted-session mechanism, not a full
  gateway/code-registry authorization exchange.
- A public code path is represented by the no-session/public session and is
  denied; public code resolution itself is not newly implemented here.
- `skill_crystallize` and `skill_claim` do not exist yet; this guard is the
  required preflight seam for them.
- No `curator_execute` or automatic archive/merge/delete path.
- Curator/operator separation, per-conversation seq, TLS and multi-tenant
  identity remain open.
- Not a production/TLS/multi-tenant claim.
