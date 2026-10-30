# Protocol pins

| Protocol | Pin | Source | Status |
|---|---|---|---|
| A2A (user alias: ACA) | protocol 1.0, release v1.0.1 | https://github.com/a2aproject/A2A/releases/tag/v1.0.1 | pinned |
| MCP | 2026-07-28 | https://modelcontextprotocol.io/ | pending official confirmation |
| Agent Plugins | IM2A profile 1.0 | local schema | draft |
| dsh-session-ref | 1.0 | user-confirmed MMP | pinned |

## A2A rule

The normative A2A artifact is upstream `specification/a2a.proto` at commit
`3303592588e388e62e0f69f701af531d2f4e3991`. This repository does not
re-declare the A2A standard. `a2a-profile.schema.json` is only an adapter
profile used to validate the fields IM2A needs (Agent Card, skills,
capabilities).

## MCP rule

`mcp-profile.schema.json` is a policy profile, not the MCP specification:

- `stdio` requires `command`.
- `streamable-http` requires an HTTPS `url`.
- public plaintext HTTP is invalid by schema.

## Session-ref rule

`dsh-session:<id>` is opaque. It is not a bearer token and must never include
conversation content. Resolution requires a matching scope and an unexpired,
non-revoked reference.

## Realtime rule (v2.5.0)

`realtime-session-profile.schema.json` and `realtime-message.schema.json` are
policy/shape schemas, not a wire protocol:

- Three channel classes: `D` (sequenced, persisted, audited, may carry content),
  `P` and `C` (unsequenced, unpersisted, audited=false, `mayCarryContent:false`).
- Resume cursors are `(principalUserId, agentId, conversationId, lastAppliedSeq)`.
  `Mcp-Session-Id` and `dsh-session:` are never cursors.
- `P`/`C` carry no payload/content/text at all; the message schema makes that
  structural, because the egress guard only inspects `payload.content`.
- No new MCP method (`noNewMcpMethods`) and no new A2A extension
  (`noNewA2aExtensions`); drain uses HTTP 503 + `Retry-After`.
- Both schemas are executed against fixtures by `scripts/validate-realtime.mjs`,
  which `scripts/validate.mjs` calls.
