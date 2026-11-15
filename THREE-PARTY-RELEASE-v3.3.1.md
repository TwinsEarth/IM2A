# Three-Channel Release - v3.3.1

Date: 2026-11-16
Branch: `soul/6l-framework`
Tag: `v3.3.1`
Package: `@twinsearth/dsh-im2a-plugin@3.3.1`
Scope: DSH runtime pin `0.2.2-rc.2` -> `0.2.2-rc.3`. No behaviour change.

## Channels

| Channel | Status | Evidence |
| --- | --- | --- |
| npm | PASS | published 2026-11-15T16:20:02.307Z; `latest` -> 3.3.1 |
| git tag | PASS | pushed to `TwinsEarth/dsh-im2a-plugin` as `v3.3.1` |
| GitHub Release | PASS | release page for tag `v3.3.1`, tarball attached |

Platform branch: `TwinsEarth/IM2A` `platform/latest` and a `platform/v3.3.1` snapshot.
`main` stays on the Go product tree and is never touched.

## Verification

```text
node --test tests/*.test.mjs          129 passed / 0 failed
scripts/validate.mjs                  exit 0 (stale-pin negative control: exit 1)
scripts/check-bridge-contracts.mjs    exit 0
scripts/check-release-allowlist.mjs   exit 0
scripts/check-layer-contracts.mjs     exit 0
npm clean-room install                3.3.1, engines 0.2.2-rc.3
plugin_manager install                applied, restart-required (unmanaged-plugin)
bridge tools/list                     32
```

The install reports `restart-required` / `unmanaged-plugin` because the bundle reaches a
plugin outside the managed plugin list. DSH must be restarted for the change to take
effect; that is the normal path for this bundle, not a failure.

## Why this release exists

`@twinsearth/dsh-im2a-plugin@3.3.0` pinned `engines.dsh = 0.2.2-rc.2`, while the
installed runtime had moved to `0.2.2-rc.3`, so the plugin manager refused to load it.
This is the third occurrence of the same failure mode; the local gate now catches it
before install (`version-matrix.json` `dsh-runtime.target` must equal the plugin pin).

## Rollback

```text
plugin_manager remove_bundle @twinsearth/dsh-im2a-plugin
plugin_manager install_bundle @twinsearth/dsh-im2a-plugin@3.3.0   # only if the runtime returns to rc.2
```
