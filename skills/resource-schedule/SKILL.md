---
name: resource-schedule
description: Schedule agent work against declared budgets, quotas, and concurrency limits.
version: 0.1.0
agents:
  - dsh
  - openclaw
  - qwen-code
  - codex
permissions:
  - read-budget
  - propose-schedule
tools:
  - im2a_presence
  - im2a_send
  - w2m_memory_read
---

# Resource Schedule

Use this skill when work must be queued, throttled, or assigned to a specific harness.

## Workflow

1. Read declared quota, daily budget, and current presence.
2. Estimate cost and expected duration before scheduling.
3. Reserve capacity; do not commit work that has no budget.
4. Put overflow work in a queue with an owner and an expiry.
5. Release the reservation when the task completes or is cancelled.

## Boundaries

- Never raise a quota or bypass a paid ledger.
- Do not schedule unbounded retries.
- Do not assign destructive work to an agent without approval.
- A missing budget is a blocker, not an unlimited allowance.

## Failure handling

- Budget exhausted: return `blocked-budget`.
- Harness offline: queue with a bounded retry window.
- Reservation lost: re-read state before retrying.
