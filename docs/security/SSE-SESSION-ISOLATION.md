# SSE session isolation — security finding and fix

Date: 2026-10-31
Status: source fix, not released

## Finding

`internal/api/harness.go` opened a global subscription:

```go
events, cancel := d.HarnessBus.Subscribe()
```

`HarnessBus.fanout` sends every event to every subscriber. The SSE handler
wrote every event without checking whether it belonged to the requested
session. Any authenticated client could receive messages addressed to other
sessions.

Affected endpoint:

```text
GET /v1/harness/stream?session=<id>
```

## Fix

- Require a non-empty, existing `session` query value (`400` / `404`).
- Send an isolated presence snapshot containing only the current session.
- Add `harness.Bus.EventVisibleTo(sessionID, event)`:
  - direct message: sender or recipient only;
  - broadcast: all sessions;
  - `harness:` / `agent:` target: matching session metadata;
  - other session/presence events: denied.
- Filter every live event before writing it.

## Regression tests

```text
internal/harness TestEventVisibleToIsSessionScoped       PASS
internal/api     TestHarnessStreamFiltersOtherSessions    PASS
go test ./...    73 packages ok / 0 FAIL
go build ./...   PASS
go vet ./...     PASS
```

The API test opens a stream for session A, publishes B -> C and B -> A, and
asserts that A receives only B -> A.

## Residual risk — do not overclaim

This is not yet a complete tenant authorization boundary:

- The session id is still a bearer-like identifier.
- `/v1/harness/sessions` still enumerates sessions for an authenticated caller.
- There is no per-session secret/token binding the stream to its owner.
- The HTTP deployment still has no TLS/multi-tenant auth claim.

Therefore:

```text
v2.5.2: NO-GO for production
v2.4.6: temporary rollback baseline only after artifact/tag verification
next patch: v2.5.3, with this fix and an explicit residual-risk note
```

Do not describe the fix as complete SSE authorization until sessions are
bound to a caller identity or per-session capability token.
