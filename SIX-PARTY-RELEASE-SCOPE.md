# Six-Party Release Scope — v2.3.45

Status: **release candidate scope lock; no tag/npm/Release yet**

## In scope

```text
Browser extension   PASS  P0-BROWSER-E2E.json
WeChat/chatops      PASS  prior real inbound/outbound confirmation
IM Bot              PASS  real LLM reply
OpenClaw            PASS  gateway/channel evidence
Agent experts       PASS  real summon + team evidence
Ops / tofuture      PASS  static UI evidence (real backend refresh pending)
```

## Explicitly out of this release gate

```text
Qwen Code adapter
MiMo Code adapter
Remaining unverified harness adapters
```

They are **post-release** work. The six-party scope does not claim the 12-adapter
compatibility matrix is complete.

## Nine release gates

```text
commit          baseline commit required
tag             only after all gates pass
Release         only from the tag
npm             package contents verified
CI              ci.yml green
Compat          six-party scope + DSH evidence green
Release workflow release.yml dry-run green
npm-publish     OIDC/provenance dry-run green
healthz         deployment health evidence
```

## Current blockers

```text
DSH live HTTP healthz: 401/404; endpoint/auth not verified
nine-gate workflows: being added
Qwen/MiMo/Rest: out of scope
```

Any blocker keeps `six-party release = NO-GO`.

## Release exception (2026-10-30)

healthz is explicitly excluded from this release. The DSH Web health endpoint
remains authenticated (`/api/health` 401, `/healthz` 404); no healthz claim is
made for v2.3.45.