# Changelog

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
- DSH plugin remains 0.1.1 for DSH 0.2.1-rc.5.
- Healthz remains excluded from this release.
- Tests: ident-code 16/16; 20-run stability gate; repository validation; allow-list/gate checks.
- Fixed release-blocking ident-code issues: numbers now start 1-9, deterministic Luhn negative test, credential IDs no longer expose the stored HMAC verifier, released Agents cannot mint or verify credentials, and released subcodes are tombstoned rather than silently reused.
- The ident-code registry remains an in-memory reference implementation; production DB/recovery work is tracked for a follow-up release.
