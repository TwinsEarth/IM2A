# IM2A v2.7.2 — Contribution Hash Chain and Audit Interface

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Frozen entry schema

Each `contributions.jsonl` line is frozen as:

```json
{
  "seq": 3,
  "prev_hash": "sha256:...",
  "entry_hash": "sha256:...",
  "agent_did": "did:im2a:agent:...",
  "action": "patch",
  "version_before": "0.2.0",
  "version_after": "0.3.0",
  "goal_id": "goal_...",
  "trace_id": null,
  "changed_sections": ["references/toolchain.md"],
  "validation_evidence": {
    "post_patch_success_count": 4,
    "post_patch_failure_count": 1,
    "verifier_agents": ["did:im2a:agent:..."]
  },
  "timestamp": "..."
}
```

Claim provenance fields (`claim_id`, `draft_id`, `draft_sha256`,
`source_signals`, `entry_id`) remain present and are included in the hash.

## Hash chain

```text
seq = previous seq + 1
prev_hash = previous entry_hash (null for seq 1)
entry_hash = sha256(canonical entry without entry_hash)
```

`verifyContributionChain` returns:

```text
ok
count
last_hash
broken_at_seq
errors
```

## Audit tool

`contribution_audit` is read-only:

```text
skill_id
agent_did?     optional contributor filter
```

Returns:

```text
entries                 filtered, seq-ordered
integrity_status        verified | broken_at_seq_N
broken_at_seq
unique_contributor_count
effective_patch_count   patch entries with valid validation_evidence
total_patch_count
chain_errors
```

`effective_patch_count` counts only patch entries whose
`validation_evidence` has non-negative success/failure counts and at least one
verifier whose DID differs from the patch author. Therefore it is always
`<= total_patch_count`.

## Tamper detection

A direct edit to any line changes its canonical hash without any ability to
rewrite the following `prev_hash` chain. Audit reports the first broken
sequence, e.g.:

```text
integrity_status = broken_at_seq_2
```

The tool never repairs or writes.

## Boundaries

- This is a lightweight local hash chain, not a consensus ledger.
- Full Shapley pricing belongs to v2.9.x; this version only records enough
  structure for it.
- Credit/reputation consumption is later work.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
