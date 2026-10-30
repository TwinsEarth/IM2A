# Changelog

## v2.4.3 — 2026-10-30

Six-party release candidate.

- Added `@twinsearth/im2a-ident-code` 0.1.0: 25-char user code, 35-char public UID, 128-char auth code, 12-digit IM2A number, 10-digit Agent subcode.
- Added ident-code 1.0 schema and cross-end contract; production owners must integrate it.
- Separated public addressing from private credentials: the 93-char suffix is an HMAC-fingerprinted opaque secret, not a private key.
- Added release allow-list and version consistency checks.
- Added WorkBuddy MCP connector 0.1.0.
- DSH plugin remains 0.1.1 for DSH 0.2.1-rc.5.
- Healthz remains excluded from this release.
- Tests: ident-code 14/14; repository validation; allow-list/gate checks.
