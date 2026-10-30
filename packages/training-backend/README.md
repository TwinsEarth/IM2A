# @twinsearth/im2a-training-backend (v2.5.0 draft)

Contract and reference fake for GPU/training backends. **Draft only.** No
backend is enabled by default, and the fake backend cannot pass a clean-load
probe.

## Why this is not `harness-adapters`

`harness-adapters` models a local config mutation with `apply` / `rollback`.
A training job fails differently: partial artifacts plus money already spent.
The only honest terminal operations are `cancel` and quarantine; there is no
rollback of GPU hours.

## Shape

- `src/contract.ts` — `TrainingBackend` interface (`capabilities`, `estimate`,
  `submit`, `poll`, `cancel`, `collect`, `verify`). Types only.
- `src/plan.mjs` — content-addressed `planId`, pre-submit validation,
  `BackendRefusal`.
- `src/registry.mjs` — `register()` does **not** enable; `enable()` is separate.
  No default backend. `resolve()` fails closed.
- `src/fake-backend.mjs` — deterministic, no GPU, no network. `verify()` always
  returns `loadProbeOk: false`.

## Invariants enforced by `scripts/check-training-backend.mjs`

1. Empty registry by default; registered ≠ enabled.
2. A backend without `estimate()` is refused.
3. A plan without a positive cost/GPU upper bound is refused before submit.
4. `planId` must equal the content hash of the plan.
5. Cancellation is terminal; partial artifacts cannot be collected.
6. A succeeded job cannot be "rolled back".
7. Collection is not verification; the fake checkpoint fails the load probe.

Run:

```bash
node scripts/check-training-backend.mjs
```
