# DSH live HTTP overlay upgrade

Date: 2026-10-31

## Root cause

The live profile was still running:

```text
@twinsearth/im2a-dsh-plugin      0.1.1
@twinsearth/im2a-dsh-plugin-live 0.1.1
```

The 0.2.0 HTTP overlay had not been installed. The old stdio row had no
`IM2A_MCP_BIN`, producing:

```text
transport: stdio
command: missing
ValidationError
```

## Applied

```text
host:     @twinsearth/im2a-dsh-plugin 0.2.0
overlay:  @twinsearth/im2a-dsh-plugin-live 0.2.0
transport: streamable-http
url:      http://127.0.0.1:18082/mcp
```

The HTTP MCP service is running with 13 tools and is registered at user
logon via `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`.

## Required action

Restart desktop DSH. The current process still holds the old failed stdio
row; package installation alone cannot hot-replace an already failed MCP row.

After restart verify:

```text
mcp__im2a__skills_list
mcp__im2a__memory_search
```

## Backup

```text
C:\Users\fangw\.dsh\profiles\desktop\.im2a-http-upgrade-20261031073924
```

## Security

18082 is loopback-only. The HTTP MCP has no inbound TLS or authentication;
do not expose it publicly.