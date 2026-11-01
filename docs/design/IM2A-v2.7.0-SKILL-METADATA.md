# IM2A v2.7.0 — Skill Metadata Ownership Namespace

Date: 2026-11-01
Status: metadata schema and claim seam implemented; the public `skill_claim`
tool remains v2.7.1 work.

## Standard-compatible placement

IM2A does not add private frontmatter keys. Ownership lives in the
agentskills.io `metadata` map, whose values are strings:

```yaml
metadata:
  im2a.origin_agent: "did:im2a:agent:abc123"
  im2a.origin_credit_at_claim: "0.72"
  im2a.contribution_log_ref: "skills/my-skill/.im2a/contributions.jsonl"
  im2a.skill_lineage: "[\"did:im2a:agent:abc123@v1\"]"
  im2a.patch_depth: "0"
```

A standard client that ignores unknown metadata loads `name`, `description`
and the body normally.

## Contribution log

The log is external:

```text
skills/<skill_id>/.im2a/contributions.jsonl
```

One JSON event per line:

```json
{
  "event_id": "contrib_...",
  "event_type": "skill_claim",
  "goal_id": "goal_...",
  "agent_did": "did:...",
  "draft_id": "draft_...",
  "draft_sha256": "64 hex",
  "target_skill_id": "...",
  "credit_at_claim": 0.72,
  "patch_depth": 0,
  "at": "..."
}
```

The log is not embedded in frontmatter.

## Read-only boundary

`skill_manage` with content or files containing an `im2a.*` metadata key is
rejected with:

```text
IM2A_METADATA_READ_ONLY
```

Ownership writes are reserved for the v2.7.1 `skill_claim` and v2.7.3
`skill_patch` tools. v2.7.0 provides the internal `claimSkill` seam and its
schema tests; it does not expose a public claim tool yet.

## Schema

```text
docs/contracts/skill-metadata-1.0.schema.json
```

Required metadata keys:

```text
im2a.origin_agent
im2a.origin_credit_at_claim
im2a.contribution_log_ref
im2a.skill_lineage
im2a.patch_depth
```

## Acceptance

```text
claim seam produces a SKILL.md with all five keys
contribution_log_ref points to an existing JSONL file
standard parser reads name/description/body while ignoring im2a.*
skill_manage metadata edit is rejected
```

## Boundaries

- Public `skill_claim` is not added in v2.7.0; v2.7.1 owns the tool and guard
  integration.
- `skill_lineage` is stored as a JSON string inside a string-valued metadata
  map.
- Contribution UUIDs and credit are fixture-level in this version.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
