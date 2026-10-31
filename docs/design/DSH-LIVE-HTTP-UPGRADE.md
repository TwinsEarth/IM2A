# DSH live HTTP overlay upgrade

## Published package

```text
@twinsearth/im2a-dsh-plugin-live@0.3.0
private: false
registry: latest
```

## Runtime state

```text
host: @twinsearth/im2a-dsh-plugin 0.2.0
overlay: @twinsearth/im2a-dsh-plugin-live 0.3.0
transport: streamable-http
url: http://127.0.0.1:18082/mcp
HTTP MCP: 13 tools, health 200
live bundles: 24
```

## Installer incident

On this DSH build, `dsh plugin add @twinsearth/im2a-dsh-plugin-live@0.3.0`
rewrote the live profile bundle list down to three entries. The full list was
restored from the pre-upgrade backup and now contains both host and live.

Do not run another plugin add on the live profile without checking
`dsh.profile.bundles` before restart.

## Restart requirement

The DSH main process was started at 08:22, before the bundle restore at 08:29.
Reloading the window/renderer is insufficient. Fully quit DeepSeek Harness
(including tray processes) and reopen it.

After restart verify:

```text
mcp__im2a__skills_list
mcp__im2a__memory_search
```

## Backups

```text
.im2a-http-upgrade-20261031073924/package.json
.im2a-broken-bundles-20261031082909/package.json
```

## Security

18082 is loopback-only; no inbound TLS/auth. Do not expose it publicly.