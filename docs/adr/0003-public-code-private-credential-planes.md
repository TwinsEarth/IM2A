# ADR 0003: Separate public code addressing from private credentials

## Status

Proposed (2026-10-30). Review with the identifier-service design in
`docs/design/IM2A-CODE-SERVICE-ARCHITECTURE.md`.

## Context

IM2A needs several user-visible codes: IM2A numbers, user handles, Agent
subcodes, optional public aliases ("明码"), and short-lived authorization
codes. They look similar enough that implementations tend to collapse them
into one table and one lookup endpoint. That creates a direct path from
"knowing someone's public code" to "acting as them", and it makes revocation,
enumeration control, and audit ambiguous.

The public code must be safe to print on a poster, put in a QR code, and
resolve anonymously. The authorization code must never be resolvable and must
grant only a narrow, short-lived action.

## Decision

1. Public addressing and private credentials are two separate planes:
   - Public: `IM2A_NUMBER`, `USER_HANDLE`, `AGENT_SUBCODE`, `PUBLIC_ALIAS`.
   - Private: authorization codes and binding tokens only.
2. The planes have separate database schemas, database roles, encryption keys,
   API listeners, and network policies. The public resolver has no permission
   on the credential schema and does not connect to the primary database.
3. A public code is never accepted as a bearer credential or as the `sub` of a
   JWT. Authorization codes are opaque, high-entropy, server-side HMAC-hashed,
   short-lived, single-use, scoped, and immediately revocable. They are shown
   once and never appear in URLs, logs, events, or caches.
4. Public resolution returns a fixed public projection only. Internal IDs,
   owner IDs, tier, trust level, quota, and PII are never returned.
5. IM2A numbers are never recycled through revocation, deletion, or release.
   Revocation and retirement are tombstones. An explicit transfer with both
   parties' consent and dual approval is the only exception, and the number
   itself does not change. Other public codes (handles, Agent subcodes,
   aliases) may be reused only after a quarantine period, with a tombstone, an
   audit record, and the applicable owner restriction; they are never silently
   reassigned. Only never-activated reservations may return to a pool
   immediately.
6. Tier entitlements (how many codes, vanity, TTL, transfer) are separate from
   trust levels (who may contact whom). Tier is data, versioned and
   grandfathered; it is not a hard-coded enum in request handlers.

## Consequences

- Two schemas and two operational surfaces instead of one; the resolver can be
  scaled and protected independently.
- Public resolution is eventually consistent (bounded by cache TTL and a
  revocation epoch); private verification is strongly consistent and uncached.
- Changing a code's ownership or state requires a domain API and an audit
  event; direct SQL fixes are forbidden.
- The first release can keep registry and credential modules in one deployable
  unit, but the public resolver must be a separate deployment from day one.
- Any proposal that adds a public lookup endpoint returning credential status,
  or that uses a public code as an auth subject, is rejected by this ADR and
  must go through a new ADR.
