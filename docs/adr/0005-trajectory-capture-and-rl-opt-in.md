# ADR 0005: Trajectory capture and RL are opt-in and contract-first

## Status

Proposed (2026-10-30). Related: ADR 0003 (public code vs private credential
planes), `docs/privacy/IM2A-IDENTITY-PRIVACY-REVIEW-v2.3.45.md`,
`docs/design/IM2A-v2.5.0-TRAJECTORY-RL-BOUNDARY.md`.

## Context

IM2A wants Hermes-style trajectory data (batch-generated `trajectories.jsonl`
in ShareGPT shape, resumable via `checkpoint.json`) and, optionally, RL in the
style of Atropos (trainer / rollout / environment decoupled as services).

Three facts constrain that plan:

1. The data a trajectory needs is the data the v2.3.45 privacy review found
   unprotected: subject identity is taken from query parameters (F-03/F-04),
   `messages` / `im_messages` are outside `erasureSources` (F-07), there is no
   retention automation (F-08), and the static gateway token injects no subject
   (F-02). A collector reading those endpoints with a service token would
   reproduce the cross-user read and the "fake delete" inside a training set.
2. `users.identifier` is simultaneously the public code and the login
   credential (F-01), and its space is small enough to enumerate. Pseudonymizing
   it with a bare hash is reversible; a trajectory containing it is account
   takeover material.
3. The harness adapter contract (`apply` / `rollback`) models local config
   mutation. A training job fails differently: partial artifacts and money
   already spent. Reusing that contract would mis-model the failure.

## Decision

1. **No capture by default.** Trajectory capture runs only per dataset, with a
   named owner, an approved budget, and a satisfied preflight. Production
   capture is not a configuration default anyone can flip.
2. **Contract before pipeline.** `trajectory-record 1.0` and
   `trajectory-dataset-manifest 1.0` are frozen before any collector is
   written. Deny-list fields (auth codes, tokens, phone/email/id-card, wxid,
   device ids, raw IP/UA, raw session refs) are rejected structurally.
3. **Consent is per subject and revocable.** Withdrawal means: stop serving the
   affected checkpoint within 30 days, rebuild the dataset without that subject,
   and retrain or roll back. No "exact unlearning" claim is made to users.
4. **Split unit is `subjectRef`, never trajectory.** Eval is frozen by hash;
   any change to the eval set retires prior conclusions.
5. **Training backends implement a separate `TrainingBackend` contract**
   (`capabilities` / `estimate` / `submit` / `poll` / `cancel` / `collect` /
   `verify`). No backend is enabled by default; a backend without a cost upper
   bound is refused.
6. **Synthetic first.** The pipeline is proven on synthetic trajectories before
   any consented data enters. P1 ships no user data.
7. **RL is out of v2.5.0.** DPO requires proven pair integrity after
   tokenization; GRPO requires a validated reward and non-degenerate group
   variance. Neither exists yet, so neither ships.
8. **Promotion requires a checkpoint release record** (expected inventory,
   rank-local save evidence, hash manifest, clean-load probe, registration
   decision, recovery boundary) plus a matched held-out comparison.

## Consequences

- v2.5.0 ships plumbing and at most SFT; no RL, no production capture.
- Upfront cost moves earlier: schema, redaction tooling, consent ledger, and a
  tenth `privacy` release gate must land before the first training run.
- Some capabilities are accepted as permanently off for this data class
  (third-party training APIs, cross-border training, auto-labeling).
- Rejected alternatives:
  - *Capture now, redact later* — inherits F-03/F-07 and cannot honor deletion.
  - *Reuse the harness adapter contract for GPU backends* — wrong failure
    semantics (no rollback of spent compute or partial artifacts).
  - *Start with RL because it is the popular method* — no validated reward;
    approval rate is confounded by trust tier and payment.
