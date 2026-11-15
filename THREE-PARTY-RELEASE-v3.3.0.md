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
| git tag | PASS (local) | annotated tag `v3.3.0`; not pushed |
| GitHub Release | BLOCKED | token has `metadata:read`; `POST /git/blobs` and `git push` both 403 |

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

## Why the third channel is blocked

`TwinsEarth/dsh-im2a-plugin` exists but is empty (size 0). The supplied fine-grained
token can read repository metadata and create repositories, but every content write is
refused:

```text
POST /repos/TwinsEarth/dsh-im2a-plugin/git/blobs
  403 {"message":"Resource not accessible by personal access token"}
git push
  remote: Permission to TwinsEarth/dsh-im2a-plugin.git denied
```

A token with **Contents: Read and write** on that repository is enough to finish the
channel: push the branch, push the tag, create the Release and attach the tarball.

## Rollback

```text
plugin_manager remove_bundle @twinsearth/dsh-im2a-plugin
plugin_manager install_bundle E:\GPT\im2a-v2.3.45\packages\dsh-im2a-plugin   # after `git checkout v3.2.3`
npm dist-tag add @twinsearth/dsh-im2a-plugin@3.2.3 latest                    # if 3.3.0 is withdrawn
```
