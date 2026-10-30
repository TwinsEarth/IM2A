---
name: fleet-control
description: Coordinate multiple IM2A agents across harnesses with explicit handoff and human approval.
version: 0.1.0
agents:
  - dsh
  - openclaw
  - qwen-code
  - codex
  - claude
  - gemini-cli
tools:
  - im2a_harness_list
  - im2a_send
  - im2a_recv
  - im2a_presence
permissions:
  - read
  - propose
  - handoff
---

# Fleet Control

Use this skill when a task must be split across more than one agent or harness.

## Workflow

1. List available harnesses and sessions.
2. Assign one owner per sub-task; never let two agents write the same scope.
3. Send a handoff with the goal, inputs, write scope, deadline, and expected output.
4. Wait for acknowledgement before treating the task as accepted.
5. Collect results, mark disagreements, and escalate final decisions to a human.

## Boundaries

- Do not invent a session id or a harness capability.
- Do not broadcast secrets, tokens, or raw private conversation content.
- A handoff is not approval; destructive actions still need explicit human approval.
- If a target harness is offline, report `blocked`; do not silently reroute.

## Failure handling

- Missing session: re-list presence and report the missing peer.
- Duplicate ownership: stop writes and escalate.
- Lost reply: retry once, then report `unverified`.
