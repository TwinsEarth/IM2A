# Three-Channel Release — v3.3.0

Date: 2026-11-15
Branch: `soul/6l-framework`
Tag: `v3.3.0`
Package: `@twinsearth/dsh-im2a-plugin@3.3.0`
Scope: kernel runtime modules registered into the module system; two new MCP tools.

## Channels

| Channel | Status | Evidence |
| --- | --- | --- |
| npm | PASS | published 2026-11-15T15:39:48.380Z; `latest` → 3.3.0; integrity recorded in the release notes |
| git tag | PASS | `v3.3.0` pushed to `TwinsEarth/dsh-im2a-plugin` (points at `9c8f86c`) |
| GitHub Release | PASS | https://github.com/TwinsEarth/dsh-im2a-plugin/releases/tag/v3.3.0 (tarball attached) |

Server-side platform code: pushed to `TwinsEarth/IM2A` as branch **`platform/v3.3.0`**.
`main` was deliberately left at `6e78a6b`.

**Decision recorded 2026-11-16 (user):** the Go product tree is the main repository and the
Node platform stays on a branch. It will not be merged into `main`. See
[IM2A-REPO-TOPOLOGY.md](IM2A-REPO-TOPOLOGY.md) for the full repository/branch policy.

## Surface changes

- MCP tools 30 → **32**: `governance_manage` (propose/status only) and
  `im2a_kernel_status`.
- Closed scope set gains `governance:propose` and `kernel:read`.
- Tool-name pattern accepts the `governance` prefix.
- Alias table 30 → **31** (`governance_manage` → `im2a_governance_manage`);
  `im2a_kernel_status` stays canonical-only.
- Six kernel runtime modules declared and attached; no capability module may claim
  `planned` any more.

## Module state after this release

```text
memory        available  tools=4  runtime=[]
evolution     available  tools=4  runtime=[]
asset         available  tools=6  runtime=[ui_artifact]
identity      partial    tools=0  runtime=[did_portability]
autonomy      available  tools=8  runtime=[]
economy       partial    tools=0  runtime=[model_registry,resource_meter]
fleet         available  tools=5  runtime=[]
governance    partial    tools=1  runtime=[scope_governance]
observability partial    tools=1  runtime=[topology_metrics]
```

## Verification

```text
node --test tests/*.test.mjs          129 passed / 0 failed
scripts/validate.mjs                  exit 0
scripts/check-bridge-contracts.mjs    exit 0
scripts/check-release-allowlist.mjs   exit 0
scripts/check-layer-contracts.mjs     exit 0
npm clean-room install                3.3.0, 16 files, 7 kernel files
live im2a_kernel_status               6 runtime modules, 0 missing, 32 tools
```

## How the third channel was finished

An earlier token could read metadata but not write (`403 Resource not accessible`), and
three replacement tokens returned `401`. The token supplied on 2026-11-16 authenticated as
`TwinsEarth` and could write, so the channel completed as:

```text
git subtree split --prefix=packages/dsh-im2a-plugin -b plugin-v330
git push <repo> plugin-v330:refs/heads/main
git push <repo> 9c8f86c:refs/tags/v3.3.0
POST /repos/TwinsEarth/dsh-im2a-plugin/releases        -> id 446178757
POST /uploads.github.com/.../assets?name=...tgz        -> uploaded
```

The release body had to be ASCII-only: the API rejected the UTF-8 notes with
`422 Unprocessable Entity`, so non-ASCII punctuation was normalized before the PATCH.

## Rollback

```text
plugin_manager remove_bundle @twinsearth/dsh-im2a-plugin
plugin_manager install_bundle E:\GPT\im2a-v2.3.45\packages\dsh-im2a-plugin   # after `git checkout v3.2.3`
npm dist-tag add @twinsearth/dsh-im2a-plugin@3.2.3 latest                    # if 3.3.0 is withdrawn
```
