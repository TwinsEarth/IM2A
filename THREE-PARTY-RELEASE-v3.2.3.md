# Three-Channel Release — v3.2.3

Date: 2026-11-13
Branch: `soul/v3.2.3` (from `soul/v3.2.2`)
Tag: `v3.2.3` (local)
Package: `@twinsearth/dsh-im2a-plugin@3.2.3`
Scope: DSH runtime pin `0.2.2-rc.1` -> `0.2.2-rc.2`.

This release is a single-package compatibility patch. No product party changes.

## Channels

| Channel | Status | Evidence |
| --- | --- | --- |
| npm | PASS | `3.2.3` published 2026-11-12T17:03:21.582Z; `latest` moved to 3.2.3; integrity `sha512-0WPIT+Dd2IimPxQIv35iE1cfpiYh8LwEfoWoh1953wGCZTZiSqW8B0kww4Wq/pC4djfkbgJeMck9hLV77lzFsg==` |
| git tag | PASS (local) | annotated tag `v3.2.3`; not pushed (see below) |
| GitHub Release | BLOCKED | destination repository created, but the supplied token cannot write contents |

## Why the third channel is blocked

Destination repository was created as instructed: **`TwinsEarth/dsh-im2a-plugin`**
(public, empty, default branch `main`).

Writing to it is refused by the supplied fine-grained token:

```text
POST /repos/TwinsEarth/dsh-im2a-plugin/git/trees
  403 {"message":"Resource not accessible by personal access token"}
  x-accepted-github-permissions: metadata=read

git push (token as password)
  remote: Permission to TwinsEarth/dsh-im2a-plugin.git denied to TwinsEarth.
  403
```

The same token can create repositories and read repository metadata, so the gap
is specifically **Contents: write**. Verified token inventory (15 repositories,
2026-11-13) also contains no existing platform repository, and
`TwinsEarth/IM2A` is the product tree (943 paths different), so it was not used.

To finish the third channel, either:

1. issue a token with **Contents: Read and write** on `TwinsEarth/dsh-im2a-plugin`
   (cleanest: fine-grained, single repository, contents write only), or
2. push `im2a-plugin-split` from a machine with normal git credentials.

The plugin-only history is ready locally as branch `im2a-plugin-split`
(3 commits: v3.2.1 branding, v3.2.2, v3.2.3), tree root = the package itself.

## Compatibility hardening added in this release

Two consecutive cycles shipped a stale `engines.dsh` pin that the plugin manager
then refused at install time. `scripts/validate.mjs` now fails locally when:

- `packages/dsh-im2a-plugin/package.json` `engines.dsh` != `version-matrix.json`
  `dsh-runtime.target`;
- `plugin.json` `engines.dsh` != `package.json` `engines.dsh`;
- the two manifests disagree on `version`;
- the bundle patch is not an `insert:` of exactly one `im2a-mcp` row.

Negative control: setting the pin back to `0.2.2-rc.1` makes the gate exit 1;
restoring it returns exit 0.

## Verification

```text
node --test tests/v321-plugin-shell.test.mjs        9/9 PASS
scripts/validate.mjs                                PASS
plugin_manager install_bundle (3.2.3)               application: applied
profile composed MCP rows                           exactly one im2a-mcp
bridge tools/list                                   30
im2a_whoami (bridge -> gateway)                     PASS  hs_cfd35b481e44
im2a_presence (bridge -> gateway)                   PASS  count=1
```

## Rollback

```text
plugin_manager remove_bundle @twinsearth/dsh-im2a-plugin
plugin_manager install_bundle E:\GPT\im2a-v2.3.45\packages\dsh-im2a-plugin   # after `git checkout v3.2.2`
npm dist-tag add @twinsearth/dsh-im2a-plugin@3.2.2 latest                    # if 3.2.3 must be withdrawn
```
