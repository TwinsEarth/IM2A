---
name: session-ref
description: Create and resolve opaque dsh-session references across agents without exposing conversation content.
version: 0.1.0
agents:
  - dsh
  - openclaw
  - codex
  - claude
  - gemini-cli
permissions:
  - create-ref
  - resolve-ref
tools:
  - w2m_session_ref_create
  - w2m_session_ref_resolve
  - w2m_session_ref_list
---

# Session Reference

Use this skill when one agent needs another agent to continue or inspect a session.

## Workflow

1. Create a reference with a named scope, expiry, and intended consumer.
2. Pass only `dsh-session:<id>`; never paste transcript content.
3. Resolve with the caller's scope; deny on mismatch or expiry.
4. Revoke when the handoff is complete.

## Boundaries

- A session reference is not a bearer token.
- Do not put message text, file contents, or credentials inside the reference.
- Cross-organization references are denied by default.
- Resolution never grants write access.

## Failure handling

- Expired or revoked: return `not-found`; do not auto-recreate.
- Scope mismatch: return `not-authorized`.
- Ambiguous consumer: require a human to bind the reference.
