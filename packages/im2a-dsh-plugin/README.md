# @twinsearth/im2a-dsh-plugin

DSH **bundle** that inserts one `@deepseek-ai/dsh-mcp-client` row for IM2A.

## Pinned runtime

```text
engines.dsh = 0.2.1-rc.3
```

This is a fixed-build policy. Do not widen the range to nightly or `^0.2.1-rc.3`.

## Required environment

```text
IM2A_MCP_BIN          absolute path to im2a-mcp(.exe)
IM2A_HARNESS_TOKEN    gateway bearer token
IM2A_HARNESS_URL      default http://127.0.0.1:18080
```

DSH scrubs ambient names matching `/TOKEN/i`; the token is mapped explicitly in
`cordis.patch.yml`. Missing values fail the row's config validation rather than
silently omitting tools.

## Install (sandbox first)

```text
plugin_manager install_bundle(file:<packed tgz>)
```

Confirm the bundle is selected, then check `--dump-config` for exactly one
`im2a-mcp` row with `insert` semantics. Live installation is not part of P0-1.

## L3 verification

A real tool call is required:

```text
mcp__im2a__im2a_harness_list
```

Text presence in `--dump-config`, `application: applied`, or a process being
alive is **not** verification.

## Rollback

Remove the bundle via `plugin_manager`; do not edit the live profile patch.
Verify the row disappears from a new session's tool list.
