# IM2A WorkBuddy Connector

WorkBuddy **MCP + Skill** connector for the IM2A Harness Bus.

## Structure

```text
im2a-workbuddy-connector/
├── connector-meta.json
├── mcp.json
├── token-schema.json
├── icon.svg
└── skills/im2a-connector/SKILL.md
```

## What it exposes

The connector launches the IM2A stdio MCP binary and lets WorkBuddy call:

```text
im2a_harness_list
im2a_whoami
im2a_presence
im2a_send
im2a_recv
im2a_history
```

## Install

1. Pack this directory as a connector zip.
2. Open WorkBuddy's connector/MCP entry.
3. Import or submit the connector.
4. Fill the token form:
   - `IM2A_MCP_BIN`: absolute path to `im2a-mcp.exe`
   - `IM2A_HARNESS_URL`: default `http://127.0.0.1:18080`
   - `IM2A_HARNESS_TOKEN`: gateway bearer token
5. Verify the connector is green, then ask:

```text
列出当前接入 IM2A 的 Harness 和在线 Agent。
```

## Requirements

```text
WorkBuddy >= 4.23.0 (token auth mode)
IM2A gateway reachable
im2a-mcp stdio binary
```

## Security

The connector contains only `${VAR}` placeholders. No token, path, or private
endpoint is embedded. WorkBuddy stores the user-entered values locally.

## Validation

```powershell
node scripts/validate-workbuddy-connector.mjs
```
