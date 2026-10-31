# SSE session isolation, capability binding, and v2.5.3 security status

Date: 2026-10-31
Status: security patch source, not deployed

## Finding

`internal/api/harness.go` used a global subscription:

```go
events, cancel := d.HarnessBus.Subscribe()
```

`HarnessBus.fanout` delivers every event to every subscriber. The SSE handler
wrote every event without checking whether it belonged to the requested
session. Any authenticated client could receive messages addressed to another
session.

Affected endpoint:

```text
GET /v1/harness/stream?session=<id>
```

## Fix 1: event filtering

- Require a non-empty existing session (400 / 404).
- Send only the current session's presence snapshot.
- Add `harness.Bus.EventVisibleTo(sessionID, event)`:
  - direct message: sender or recipient only;
  - broadcast: all sessions;
  - `harness:` / `agent:` target: matching session metadata;
  - other session/presence events: denied.
- Filter every live event before writing SSE.

## Fix 2: per-session capability

Registration now returns:

```json
{
  "session": { "id": "hs_...", "harness": "...", "agent": "..." },
  "token": "<opaque session capability>"
}
```

The token is stored only as a SHA-256 hash inside the bus and is never returned
by presence/whoami. It is required for:

```text
GET    /v1/harness/sessions?session=<id>       X-IM2A-Session-Token
GET    /v1/harness/messages?session=<id>       X-IM2A-Session-Token
POST   /v1/harness/messages                     X-IM2A-Session-Token
GET    /v1/harness/stream?session=<id>          X-IM2A-Session-Token
DELETE /v1/harness/sessions/<id>                X-IM2A-Session-Token
```

An EventSource client that cannot set headers may use the
`session_token` query fallback; that fallback has URL/log leakage risk and
should be replaced by a header-capable client where possible.

Re-registering the same harness+agent without a matching capability creates a
**new session**; it does not return the existing session's events or token.
Successful capability re-registration rotates the token.

`/v1/harness/sessions` no longer enumerates all sessions; it returns only the
authenticated session.

## Regression tests

```text
internal/harness TestEventVisibleToIsSessionScoped          PASS
internal/harness TestRegisterWithoutCapabilityCreatesNewSession PASS
internal/api     TestHarnessStreamFiltersOtherSessions       PASS
cmd/im2a-mcp     tests                                       PASS
Node MCP session capability smoke test                        PASS (13 tools)
go test ./...     73 packages ok / 0 FAIL
go build ./...    PASS
go vet ./...      PASS
```

## v2.4.6 rollback verification

Verified:

```text
tag ref object:        bd4e61584cedffa988743719cbf364dfa2c2dd85
tag target commit:     1fff366305e06d67315f6ed8d7be25489d0d8560
source harness.go:     404 (v2.4.6 has no harness SSE endpoint)
release assets:        6
```

But the published asset integrity check failed:

```text
expected SHA256SUMS:
5ebb4dc0038406c2f0cda31097a6500e060ef57eaddaadf306b025067da0e181
actual twinsearth-im2a-dsh-plugin-0.1.1.tgz:
6402028aa7b7e415b5b8c1ac4965e5e33a945c6e19f512162618231fdea455a4
```

Therefore v2.4.6 is only a **rollback candidate pending asset repair**, not a
fully verified safe baseline.

## Residual risk

This is not a complete tenant authorization boundary:

- The gateway bearer token still has no per-user/per-tenant identity.
- The session token is a bearer capability; registration is available to any
  gateway-token holder.
- The query fallback can leak through URLs/logs.
- HTTP deployment still has no TLS/multi-tenant claim.

## Release decision

```text
v2.5.2: NO-GO for production
v2.4.6: rollback candidate only after asset integrity repair
v2.5.3: security patch candidate; not deployed, not production-ready
```

Do not describe v2.5.3 as complete SSE authorization until sessions are bound
to a caller identity and the deployment has TLS/tenant isolation.
