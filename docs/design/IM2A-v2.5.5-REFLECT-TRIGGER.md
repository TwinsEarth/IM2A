# IM2A v2.5.5 — Read-only Reflection Trigger

Date: 2026-10-31
Status: implemented and deployed to the Tencent dev host; not production-ready.

## Scope

v2.5.5 answers one question before any evolution write:

> Is this trace worth learning from, and what abstract principle might it
> contain?

It does **not** write a skill, memory, proposal or trace. The output is a
suggestion for the later `drive_propose` / `skill_crystallize` path, so "what is
worth learning" is decoupled from "what is actually learned".

## MCP surface

Tool `reflect_trigger` (surface 14 -> 15):

```json
{
  "trace_id": "32 lowercase hex",
  "agent_did": "did:im2a:...",
  "focus": "failure_pattern | success_pattern | bottleneck | coordination"
}
```

`focus` is optional; the objective assessment is still based on trace evidence.

Output:

```json
{
  "assessment": {
    "worth_reflecting": true,
    "reason_codes": ["tool_calls_at_or_above_min", "successful_terminal_span"],
    "complexity_score": 0.5
  },
  "distilled_candidates": [
    {
      "candidate_type": "skill_create",
      "summary": "For multi-step tasks, checkpoint intermediate results and validate the slowest step before adding more calls.",
      "source_trace_ids": ["<trace id>"],
      "confidence": 0.53
    }
  ]
}
```

`candidate_type` is one of `skill_create`, `skill_patch`, `principle_extract`.
Candidates carry a trace reference and a non-binary confidence; they are not
written anywhere.

## Configuration layer

Thresholds live in `IM2A_REFLECT_CONFIG` (inline JSON or a path to a JSON file)
and are loaded per call. The Agent loop does not own these values.

```json
{
  "minToolCalls": 5,
  "minUniqueSpans": 2,
  "complexityFloor": 0.25,
  "maxCandidates": 3,
  "requireSuccess": false,
  "enableFailureRecovery": true,
  "enableCoordination": true,
  "weights": {
    "toolCalls": 0.35,
    "uniqueSpans": 0.15,
    "failureRecovery": 0.2,
    "coordination": 0.2,
    "tokenCost": 0.1
  },
  "confidence": {
    "base": 0.45,
    "toolCalls": 0.03,
    "uniqueSpans": 0.05,
    "failureRecovery": 0.08,
    "coordination": 0.08,
    "max": 0.9
  }
}
```

The effective configuration is validated (integer bounds, booleans, weights
sum to 1, confidence bounds). Invalid configuration fails closed with
`REFLECT_CONFIG_INVALID`.

## Assessment logic

A trace is worth reflecting when at least one configured signal is present:

```text
tool calls >= minToolCalls
or non-trivial failure recovery (a failed span followed by a successful span)
or cross-agent coordination evidence
```

and its `complexity_score` is at or above `complexityFloor`, and the optional
`requireSuccess` gate is satisfied.

In v2.5.5, cross-agent coordination uses the available 1.0 trace evidence:
`im2a_send` / `im2a_recv` bus spans (or `agent:`-prefixed spans). This is a
documented heuristic, not peer-agent attribution; v2.6 adds explicit peer
metadata.

Complexity is a 0..1 weighted score over call count, unique span names, failure
recovery, coordination and token cost. The weights are configuration, not
hidden constants in the agent prompt:

```text
complexity = 0.35*min(calls/5, 1)
           + 0.15*min(uniqueNames/2, 1)
           + 0.20*failureRecovery
           + 0.20*coordination
           + 0.10*min(tokenCost/1000, 1)
```

Defaults are shown for readability; the effective values are loaded from
`IM2A_REFLECT_CONFIG`. Candidate confidence is the configured base plus
evidence-scaled increments, clamped to `[0.05, min(0.99, confidence.max)]`;
the default rich-trace value is `0.53`, not 0 or 1.

## Read-only proof

- `reflect_trigger` calls `TraceStore.get`, which only reads `trajectory.jsonl`.
- There is no write path to skill, memory, proposal or trace from reflection.
- It verifies the trace tenant and requires `record.agent_did === agent_did`.
- Tests compare the trajectory file bytes before and after reflection; they are
  identical.

## Acceptance

Local unit tests:

```text
5+ successful tool calls -> true + candidate       PASS
2-step simple query -> false + empty                PASS
failure recovery -> true + skill_patch candidate    PASS
cross-agent evidence -> true + principle candidate  PASS
strict minToolCalls threshold -> false              PASS
read-only bytes unchanged                           PASS
agent mismatch / trace not found / invalid focus    PASS
5 tests, 5 passed
```

Live Tencent dev:

```text
HTTP MCP tools/list                         15
rich trace: worth_reflecting                true
rich candidates                             1 (skill_create, confidence 0.53)
simple trace: worth_reflecting              false
simple candidates                           0
trajectory lines before/after reflection    11 / 11
Go MCP 0.5.0 tools/list                     15
Go MCP reflect_trigger                      true + 1 candidate
```

## Deployment

```text
HTTP MCP root:  /opt/im2a/v2.5.5
HTTP MCP:       Node 0.5.0, 15 tools, 127.0.0.1:18082
Go MCP:         /opt/im2a/v2.5.5/bin/im2a-mcp and stable v2.5.2 path, 0.5.0
trace dir:      /opt/im2a/v2.5.5/data/traces
backup:         /opt/backups/im2a-mcp-pre-v2.5.5-20261031185141
```

## Boundaries

- `reflect_trigger` is read-only; it cannot promote, patch or write.
- `agent_did` / `session_id` are asserted, not cryptographically bound.
- Cross-agent detection is a v2.5.5 heuristic based on bus spans.
- No RL or credit consumer yet; candidates are not training data.
- Curator/operator separation and per-conversation seq remain open P0s.
- Not a production/TLS/multi-tenant claim.
