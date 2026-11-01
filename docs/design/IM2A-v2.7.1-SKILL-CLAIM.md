# IM2A v2.7.1 — skill_claim and Initial Lineage

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Tool

`skill_claim` is a new MCP tool (28 -> 29).

```text
agent_did
draft_id
target_skill_id       OR new_skill_name
claim_justification:
  goal_id
  source_signals
  trace_id?           // reflection-origin trace
  description?
  origin_credit_at_claim?
```

## Guard order

```text
1. resolve draft from drafts/<goal>/*.patch.md
2. verify claim_justification.goal_id exists in goal_buffer
3. verify source_signals are a subset of goal.source_signals
4. skill_guard_check(operation=create|patch), L2 skill:write
5. write SKILL.md metadata and append contribution JSONL
```

The guard denial is returned before any metadata or log write.

## Origin immutability

First claim writes:

```text
im2a.origin_agent = caller DID
im2a.origin_credit_at_claim = ...
im2a.contribution_log_ref = skills/<id>/.im2a/contributions.jsonl
im2a.skill_lineage = ["<caller>@0.1.0"]
im2a.patch_depth = "0"
```

Later patch claims:

```text
origin_agent       preserved exactly
origin_credit      preserved exactly
patch_depth        +1
skill_lineage      append "<caller>@<new version>"
version            0.1.0 -> 0.2.0 -> 0.3.0
```

## Contribution chain

Each JSONL entry contains:

```text
entry_id
claim_id
agent_did
action              create | patch
version_before      null | x.y.z
version_after       x.y.z
goal_id
trace_id            null | 32 hex
timestamp
draft_id
draft_sha256
source_signals
prev_hash           null | previous entry_hash
entry_hash          sha256(canonical entry without entry_hash)
```

`verifyContributionChain` independently checks links and hashes.

## Versioning

```text
new skill          0.1.0
patch claim        increment minor, reset patch
```

## Acceptance

```text
draft_patch claimed by L2 caller
origin_agent == caller DID
first contribution entry hash independently verifies
goal_id traces back to autonomous drive_propose output
second claim preserves origin and appends linked entry
```

## Boundaries

- Guard cross-agent rules still apply; a foreign owner cannot be patched by
  another agent in this version.
- `contribution_log` hash chain is lightweight, not a consensus ledger.
- Credit/reputation consumer formula is later work.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
