# IM2A v2.6.7 — Joint Drive Proposal and Trace Reflection

Date: 2026-11-01
Status: implemented; deployment/evidence accompany the release.

## Scope

`drive_propose` is no longer evaluated only from network/curator signals. For
each proposed skill intent it cross-checks recent skill-use traces and applies
a suppression factor when trace evidence does not support improvement.

```text
curator_audit stale signal
  -> drive_propose candidate
  -> recent skill_load traces (up to 5)
  -> assessTrace(focus=success_pattern)
  -> priority_score = drive.priority × signal_strength × recency_factor × suppression
```

This is not a veto.

## Joint trigger

For each curator-backed candidate whose `source_signals` contains a skill:

```text
traceStore.recentForSkill(skill_id, limit=5)
```

Only traces containing a `skill_load` span whose name equals the skill id are
considered. Each trace is assessed with `focus=success_pattern`.

```text
positive: at least one trace worth_reflecting=true
          or distilled_candidates non-empty
          -> suppression_factor = 1.0

negative: all recent traces worth_reflecting=false
          and all distilled_candidates empty
          -> suppression_factor = 0.5

no traces / trace lookup unavailable
          -> suppression_factor = 1.0 (no evidence, no suppression)
```

Output per candidate includes:

```json
{
  "priority_score": 0.1225,
  "joint_reflection": {
    "available": true,
    "trace_count": 1,
    "trace_ids": ["..."],
    "worth_reflecting": false,
    "candidate_count": 0,
    "suppression_factor": 0.5,
    "reason": "trace_evidence_does_not_support_improvement"
  }
}
```

## Ordering consequence

Two stale skills under the same drive have the same base priority. The one
with negative trace evidence gets `priority_score × 0.5`; the one with positive
trace evidence keeps its score. Putting both proposals into `goal_buffer`
therefore ranks the positive candidate above the negative one.

## Read-only behavior

The joint trigger only reads:

```text
drives/
recent traces
skill curator audit
memory recall
```

It does not write traces, skills, drives, goals, or memory. It does not call
`skill_guard_check` and does not apply a patch.

## Acceptance

```text
stale-only candidate + negative trace reflection
  -> suppression_factor 0.5
  -> priority_score lower than a positive-evidence candidate
```

Goal-buffer dedup also requires overlapping source signals. Two different
skills with nearly identical wording are not merged; only overlapping
skill/memory signals can merge. This keeps joint-reflection ordering meaningful
when one drive proposes two different stale skills.

## Boundaries

- `success_pattern` reflection is the existing lightweight v2.5.4 assessor,
  not a separate LLM call.
- Traces without a matching `skill_load` span produce no suppression evidence.
- The factor is 0.5, not a veto; a high-priority drive can still surface.
- Recent-trace lookup is tenant-scoped and read-only.
- Curator/operator separation, per-conversation seq, full auth-code exchange,
  TLS and multi-tenant identity remain open P0s.
- Not a production/TLS/multi-tenant claim.
