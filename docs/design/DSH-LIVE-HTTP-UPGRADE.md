# DSH live HTTP overlay upgrade

Date: 2026-10-31

## Root cause

Two separate problems:

1. The live profile was still on host 0.1.1 / overlay 0.1.1.
2. After installing 0.2.0, `@twinsearth/im2a-dsh-plugin-live` was missing from
   `dsh.profile.bundles`. The package was installed but never composed, so the
   host stdio row remained and failed with missing `command`.

## Applied

```text
host:     @twinsearth/im2a-dsh-plugin 0.2.0
overlay:  @twinsearth/im2a-dsh-plugin-live 0.2.0
transport: streamable-http
url:      http://127.0.0.1:18082/mcp
bundle order: host, live
```

HTTP MCP service: 13 tools, health 200, loopback-only.

## Required action

Restart desktop DSH again. The running process still contains the old failed
stdio row; only a restart recomposes the bundle list.

After restart verify:

```text
mcp__im2a__skills_list
mcp__im2a__memory_search
```

## Backups

```text
.im2a-http-upgrade-20261031073924/package.json
.im2a-bundles-fix-<timestamp>/package.json
```

## Security

18082 is loopback-only; no inbound TLS/auth. Do not expose publicly.