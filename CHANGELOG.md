# Changelog

## v2.6.6 — 2026-11-01 goal buffer persistence and restart recovery

- Goal buffer is persisted in Agent-local SQLite, separate from Memory Hub.
- Startup recovery: executing -> pending, scheduled stays, pending stays;
  completed/expired remain audited but are not active.
- `goal_buffer action=recover` exposes the recovery report.
- Draft patch bodies remain in drafts/ and are stripped from SQLite execution
  telemetry.
- Added 1 persistence/recovery test.

## v2.6.5 — 2026-11-01 goal priority arbitration and dedup

- `goal_buffer put` now admits through capacity and deduplication.
- Default capacity 8; lowest-priority pending/scheduled goal expires on
  overflow, with source_signals preserved in goal_events.details.
- Similar intents merge at 0.85 local-embedding similarity; source_signals are
  unioned and priority is the max.
- Added `goal_merges` and `goal_events.details_json`.
- `drive_propose` now returns priority_score, signal_strength and
  recency_factor.
- Added 2 arbitration/dedup tests.

## v2.6.4 — 2026-11-01 drive calibration

- Added `drive_calibrate`; tool surface is 27.
- Added local `goal_proposals` and `goal_executions` telemetry.
- Per-drive metrics: proposal_acceptance_rate, execution_yield, expiry_rate,
  mean confidence delta.
- Returns priority_adjustment, trigger_hint_refinement and goal-level evidence.
- Read-only: never modifies drives/; application still requires
  `drive_manage` + `skill_guard_check` L2.
- Added 2 calibration tests.

## v2.6.3 — 2026-11-01 goal schedule and draft-only execution

- Added `goal_schedule`; tool surface is 26.
- Executes a pending/scheduled goal through scheduled -> executing ->
  completed/expired.
- Uses read-only curator_audit / memory_recall_v2 signals and a restricted
  skill patch draft under `drafts/`; never writes a skill.
- Added confidence_delta and recommended_next (crystallize / patch / discard);
  negative delta can never recommend crystallize.
- Added 3 goal schedule tests.

## v2.6.2 — 2026-11-01 reserved-ratio scheduler and idle detection

- Added `scheduler_cycle` and `scheduler_audit`; tool surface is 25.
- Cycle budget split into external_pool and reserved_pool; default
  reserved_ratio 0.15, bounded 0.10..0.30.
- External demand queues when external_pool is exhausted and never borrows
  reserved_pool.
- Structural idle detection: external queue empty at cycle start.
- Idle cycle schedules the highest-priority pending goal and records reserved
  allocation; temporary v2.6.3 goal_schedule seam.
- Append-only scheduler audit keeps external and reserved consumption
  trajectories separate.
- Added 4 scheduler tests.

## v2.6.1 — 2026-11-01 read-only drive proposal

- Added `drive_propose`; tool surface is 23.
- Reads drives and current `curator_audit` or `memory_recall_v2` signals,
  matches the drive description predicates, and returns `proposed_goals`.
- Never writes the goal buffer; adoption and scheduling remain future work.
- Added `recall_history` local telemetry and median `budget_used` token-cost
  basis for proposals.
- Added a safe predicate parser for drive descriptions and 4 tests.

## v2.6.0 — 2026-11-01 durable drives and local goal buffer

- Added `drive_list`, `drive_manage` and `goal_buffer`; tool surface is 22.
- Added optional `drives/*.md` durable concern declarations with a strict
  YAML frontmatter schema and a checked-in sample.
- `drive_manage` calls `skill_guard_check` with operation=patch and
  target_skill_id=drive_id before writing; public sessions are denied and
  audited.
- Added a local goal buffer in a separate SQLite database, never Memory Hub.
- Goal status machine: pending -> scheduled -> executing -> completed/expired;
  every transition is append-only auditable.
- Added drive and goal-buffer JSON Schemas plus 5 tests.

## v2.5.9 — 2026-10-31 evolution pipeline dry run and interface freeze

- Added `evolution_pipeline_dryrun`; tool surface is now 19.
- Runs trace_ingest -> reflect_trigger -> curator_audit -> skill_guard_check
  (simulated) -> memory_recall_v2 with zero durable writes.
- Returns per-step timings, schema checks, `would_write` summaries and
  `id_traceability`.
- Added explicit `trace_id` to reflect assessment and stable `candidate_id` to
  distilled candidates.
- Froze the five tool input/output schemas in
  `docs/contracts/frozen-evolution-tools-2.5.9.json` with SHA-256 manifest and
  `evolution-pipeline-1.0.schema.json`.
- Added tests for report schema, frozen output schemas, freezes hashes and
  forged-schema rejection.

## v2.5.8 — 2026-10-31 layered memory recall

- Added `memory_recall_v2` to the HTTP MCP and Go stdio MCP; tool surface is
  now 18.
- Memory rows now carry layer/updated_at/source_trace_id/confidence/
  conflict_key; existing databases migrate in place.
- Recall pipeline: parse -> route -> gather -> rerank -> diversify -> suppress
  -> budget. `budget_tokens` is a hard ceiling.
- Layer priority: skill_description > atom > entity_page > raw_event; raw
  events are capped to a small share and dropped first under pressure.
- Quality flags: stale atom (updated >30 days ago), conflicted (lower
  confidence than a same-conflict-key source), low_confidence, fresh.
- Stale atoms are suppressed unless `include_stale: true`.
- Added a 50+ namespace test proving budget compliance, stale suppression and
  nonzero suppressed_count.

## v2.5.7 — 2026-10-31 skill-write guard and audit chain

- Added `skill_guard_check` to the HTTP MCP and Go stdio MCP; tool surface is
  now 17.
- The guard runs at the MCP handler boundary: `skill_manage` and
  `curator.stage` call it before any write.
- Requested scope is derived from the verified server session, never accepted
  from the caller. Public/read-only sessions are denied; `skill:write` plus
  authorization level L2 is required.
- Denials: `insufficient_scope`, `scope_ceiling_exceeded`,
  `cross_agent_write_blocked`.
- Every check appends a hash-chained JSONL audit event with
  `request -> decision -> approval -> result` and returns `audit_event_id`.
- Sessions are HMAC-signed tokens in `x-im2a-skill-session`; Go MCP forwards
  `IM2A_SKILL_GUARD_SESSION_TOKEN`.
- Added unit tests for public denial, L2 allow, read-only denial, level
  ceiling, cross-agent denial, audit chain verification and tamper detection.

## v2.5.6 — 2026-10-31 read-only curator lifecycle audit

- Added `curator_audit` to the HTTP MCP and Go stdio MCP; tool surface is now
  16.
- Audits active skills by network or agent ownership, with optional `since`
  filtering: lifecycle state, last use, 30-day use count, patch count, L0 token
  estimate and advisory recommended action.
- Lifecycle rules: 30 days unused -> stale; 90 days unused -> archive candidate;
  promoted archive -> archived. No hard delete; no automatic archive, merge or
  delete path.
- Added `skill_usage` and `skill_owners` tables; `skill_view` records usage
  telemetry. Audit itself performs SELECTs only.
- Added tests for 6-skill distribution, L0 estimate within 10%, read-only
  no-write proof, agent-owned scope, since filter and invalid inputs.
- Deferred: automatic curator execution remains a v3.0.x governance action or
  a v2.9.x Skill Node owner action.

## v2.5.5 — 2026-10-31 read-only reflection trigger

- Added `reflect_trigger` to the HTTP MCP and Go stdio MCP; tool surface is now
  15.
- `reflect_trigger` reads one tenant-scoped trace by id and agent DID, then
  returns `worth_reflecting`, `reason_codes`, `complexity_score` and abstract
  `distilled_candidates`.
- Thresholds live in `IM2A_REFLECT_CONFIG` (inline JSON or a file), not in the
  Agent loop; defaults are tool calls >=5, non-trivial failure recovery, or
  cross-agent bus evidence.
- Reflection is read-only: candidates are suggestions, never skill/memory/
  proposal/trace writes. It verifies the trace owner and refuses agent mismatch.
- Added 5 reflection tests: 5+ successful trace true with candidates, 2-step
  simple trace false and empty, failure recovery, cross-agent evidence,
  config override, read-only and ownership checks.
- Deferred: curator/operator separation, per-conversation seq domain and
  cryptographic caller identity remain open.

## v2.5.4 — 2026-10-31 execution trace foundation

- Added `trace_ingest` to the HTTP MCP and Go stdio MCP; tool surface is now 14.
- Added a strict `execution-trace 1.0` contract: span metadata plus SHA-256
  digests only; raw prompts, arguments, outputs and credentials are rejected.
- Added tenant-scoped, append-only `trajectory.jsonl` storage with serialized
  append and fsync; no update or delete API.
- Every event carries `im2a.agent_did`; `skill_load` requires an explicit
  `skill_version`, other events record `none`.
- Added JSON Schema validation, 12-event acceptance, negative controls and
  tenant-isolation tests.
- Deferred: curator/operator identity separation, per-conversation seq domain,
  and authenticated caller-to-agent binding remain W1/security follow-ups.
- Healthz remains excluded; production TLS/multi-tenant deployment is not
  claimed.

## v2.5.3 — 2026-10-31 security patch

- Fixed SSE cross-session event fanout: stream now requires a valid session capability and filters every event.
- Added per-session capability tokens for stream/history/send/presence/unregister; unbound registrations create a new session instead of reusing an existing one.
- Scoped /v1/harness/sessions to the caller's own session.
- Added Go and Node MCP session-capability handling and regression tests.
- Verified v2.4.6 tag target and source; its GitHub asset SHA256SUMS does not match the published dsh plugin asset, so it is a rollback candidate pending asset repair, not a fully verified safe baseline.
- Residual risk: HTTP TLS/multi-tenant auth and agent-plugins draft are still not production claims.
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
