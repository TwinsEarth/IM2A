---
name: memory-sync
description: Read and write scoped fleet memory through the W2M Memory Hub without leaking device-specific execution details.
version: 0.1.0
agents:
  - dsh
  - openclaw
  - qwen-code
  - codex
  - claude
permissions:
  - read
  - propose-write
  - request-approval
tools:
  - w2m_memory_read
  - w2m_memory_write
  - w2m_session_ref_resolve
---

# Memory Sync

Use this skill when information must survive a session or move between harnesses.

## Workflow

1. Query memory by scope, topic, and source device.
2. Prefer a reference to an existing memory over duplicating content.
3. Write only durable facts, decisions, or project conventions.
4. Tag every write with source platform, device, and timestamp.
5. Route cross-agent sharing through the approval queue.

## Boundaries

- Never store tokens, passwords, cookies, or private keys.
- Do not write raw shell output or platform-specific paths as shared memory.
- Do not treat memory as a permission grant.
- Conflicting memories stay visible; do not silently overwrite.

## Failure handling

- Write rejected: keep the local draft and report `pending-approval`.
- Scope mismatch: return `not-authorized`, do not broaden the query.
- Stale reference: request a fresh write rather than guessing.
