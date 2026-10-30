# Changelog

## v2.5.2 — 2026-10-31

Six-party release with the real MCP skill/memory surface.

- Registered 13 MCP tools: 6 IM2A bus, 4 skills, 3 memory.
- Added SQLite FTS5 Memory Hub with tenant-scoped search and proposal/promotion boundary.
- Go stdio MCP now registers the same 13 tools and forwards skills/memory to the HTTP MCP/FTS5 backend.
- Fixed Go vet defect in internal/memoryvault (self-assignment); full build/vet/test PASS (73 packages).
- Added real SQLite integration/zero-residue tests and performance evidence.
- Healthz remains excluded; production TLS/multi-tenant deployment is not claimed.
## v2.5.0 — 2026-10-30

Agent Plugins 2.0 and evolution contracts.

- Added Agent Plugins 2.0 source manifest, v2 schema, 1.0 projection check, agents/formations/learning declarations.
- Added proposal-only skill runtime with L0/L1/L2 progressive disclosure, staging/eval floor, operator promotion, pointer rollback, session pinning.
- Added realtime D/P/C contracts, trajectory/RL default-off boundaries, MCP bridge tool catalog, and negative-control checks.
- Healthz remains excluded; production RL and external endpoint wiring are not claimed.
- Tests: skill runtime 8/8; real SQLite integration 6/6; ident-code 16/16; contract/boundary checks PASS.\n- Real SQLite persistence, transaction, uniqueness and zero-residue tests added; Go gateway full build/vet/test verified on Linux after fixing memoryvault self-assignment.
## Unreleased — v2.5.0 realtime contract layer

Contract-level work only. No wire protocol, MCP method, or A2A extension was added.
Runtime is **not** ready: see `docs/adr/0006-realtime-session-semantics.md` §5.

- Added `packages/protocol/schemas/realtime-message.schema.json` (D/P/C). Presence and control variants have no payload/content field and `additionalProperties:false`, so they cannot carry text around the egress guard.
- Added 3 positive + 2 negative message fixtures; a `TEXT` message without `clientMsgId` is now invalid by construction.
- Added `scripts/validate-realtime.mjs` and wired it into `scripts/validate.mjs` (`schemaChecks` 6→7); the realtime profile schema was previously unexecuted.
- Added ADR 0006 (D/P/C, cursor 4-tuple, takeover/`SUPERSEDED`, drain via 503, per-channel backpressure, double rate limit).
- Added `tests/TEST-MATRIX-v2.5.0-REALTIME.md`: 25 cases across six parties; 0 complete real-machine passes.
- Found and recorded a current-code defect: the harness SSE fanout does not filter by session, so any authenticated subscriber receives every message (`im2agent/internal/api/harness.go:231-245`, ADR G3 / RT-21). Both repositories still need a fix; no test in this repo covers it.
- Tests: P0 4/4, validation PASS with 7 schema checks.

## v2.4.6 — 2026-10-30

Six-party release with the release-workflow and ident-code fixes.

- Fixed release allow-list/dry-run behavior for CI and external artifacts.
- Fixed deterministic Luhn test, 12-digit number leading-zero rule, credentialId leakage, and released-Agent credential revocation.
- Added DB reliability review; production DB/recovery remains follow-up.
- Healthz remains excluded from this release.
- Tests: ident-code 16/16, P0 3/3, validation/dry-run/allow-list PASS.
## v2.4.3 — 2026-10-30

Six-party release candidate.

- Added `@twinsearth/im2a-ident-code` 0.1.0: 25-char user code, 35-char public UID, 128-char auth code, 12-digit IM2A number, 10-digit Agent subcode.
- Added ident-code 1.0 schema and cross-end contract; production owners must integrate it.
- Separated public addressing from private credentials: the 93-char suffix is an HMAC-fingerprinted opaque secret, not a private key.
- Added release allow-list and version consistency checks.
- Added WorkBuddy MCP connector 0.1.0.
- DSH plugin 0.2.0 published to npm for DSH 0.2.1-rc.5.
- Healthz remains excluded from this release.
- Tests: ident-code 16/16; 20-run stability gate; repository validation; allow-list/gate checks.
- Fixed release-blocking ident-code issues: numbers now start 1-9, deterministic Luhn negative test, credential IDs no longer expose the stored HMAC verifier, released Agents cannot mint or verify credentials, and released subcodes are tombstoned rather than silently reused.
- The ident-code registry remains an in-memory reference implementation; production DB/recovery work is tracked for a follow-up release.
