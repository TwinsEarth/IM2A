# Six-Party Release — v3.2.2 (DSH compatibility patch)

Date: 2026-11-04
Branch: `soul/v3.2.2` (from `soul/v3.2.1`)
Scope: `@twinsearth/dsh-im2a-plugin` 3.2.1 -> 3.2.2, DSB runtime pin
`0.2.1-rc.5` -> `0.2.2-rc.1`.

This release is a **single-package compatibility patch**. It does not change any
product party; it only makes the DSH plugin installable on DSH 0.2.2-rc.1.

## Six parties

| Party | Impact in v3.2.2 | Re-verified this release |
| --- | --- | --- |
| Browser extension | none | no (unchanged artifact 0.2.3) |
| WeChat / chatops | none | no (unchanged) |
| IM Bot | none | no (unchanged) |
| OpenClaw | none | no (unchanged) |
| Agent experts | none | no (unchanged) |
| Ops / tofuture | none | no (unchanged) |

No party is re-verified here. The previous six-party scope document
(`SIX-PARTY-RELEASE-SCOPE.md`) still governs those parties and its evidence is
unchanged. Do not read this table as "six parties passed for v3.2.2".

## Nine release gates

| Gate | Status | Evidence / reason |
| --- | --- | --- |
| commit | PASS | commit on `soul/v3.2.2` |
| tag | PASS | local tag `v3.2.2` |
| Release | BLOCKED | repository has no git remote; no GitHub Release possible |
| npm | PASS | `npm pack` 10 files, no node_modules; sha256 recorded |
| CI | BLOCKED | `.github/workflows/ci.yml` exists but was not run (no remote/runner) |
| Compat | PASS | install on DSH 0.2.2-rc.1 applied; one `im2a-mcp` row; 30 tools; `im2a_whoami` OK |
| Release workflow | BLOCKED | `release.yml` dry-run not executed in CI |
| npm-publish | BLOCKED | `npm whoami` -> ENEEDAUTH; no registry credentials |
| healthz | BLOCKED | known exception: DSH Web health requires auth (401/404) |

## Decision

```text
six-party release = NO-GO
```

Three gates are blocked by missing infrastructure, not by code:

1. **Release / CI / Release workflow** need a git remote (and a runner).
2. **npm-publish** needs an npm token with `@twinsearth` scope access.
3. **healthz** keeps the standing exception; it is not claimed.

The v3.2.2 artifact is a verified release candidate. Publishing it requires the
three items above; none of them can be manufactured locally.

## What was verified for this release

```text
node --test tests/v321-plugin-shell.test.mjs        9/9 PASS
plugin_manager install_bundle (3.2.2)               application: applied, version 3.2.2
profile composed rows                               exactly one im2a-mcp
bridge tools/list                                   30
im2a_whoami (bridge -> gateway)                     PASS  session hs_af7beba7d372
npm pack                                            10 files, 10,683 bytes
```

## Rollback

```text
plugin_manager remove_bundle @twinsearth/dsh-im2a-plugin
plugin_manager install_bundle E:\GPT\im2a-v2.3.45\packages\dsh-im2a-plugin   # after git checkout v3.2.1
```
