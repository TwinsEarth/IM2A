# Tencent Cloud v2.5.3 Deployment

Date: 2026-10-31
Resource: `ins-j3ou8dmy`
Host: `134.175.243.146` (public) / `192.168.0.5` (internal)
OS: Ubuntu Server 24.04 LTS

Status: **deployed on the Tencent dev host; not production-ready.**

## What was deployed

```text
gateway:      v2.3.25 -> v2.5.3
HTTP MCP:     0.2.x -> 0.3.0
Go stdio MCP: 0.2.x -> 0.3.0
```

The gateway was upgraded first, then the Go/Node MCP clients. The old clients
could not send a per-session capability, so session-scoped tools returned 401
between the two steps. The old HTTP MCP service was replaced in the same
maintenance window.

## Layout

```text
/opt/im2a/bin/im2a-gateway                 # v2.5.3
/opt/im2a/v2.5.3/                          # new HTTP MCP tree
  bin/im2a-mcp                             # Go 0.3.0
  packages/im2a-http-mcp/server.mjs        # Node 0.3.0
  packages/skill-runtime/
  packages/memory-hub/
  data/skills.db
  data/memory.db
/opt/im2a/v2.5.2/bin/im2a-mcp              # stable compatibility path, now 0.3.0
/etc/im2a/env                              # gateway secrets, root:root 0600
/etc/im2a/http-mcp.env                     # harness URL/token, root:root 0600
/etc/systemd/system/im2a-gateway.service
/etc/systemd/system/im2a-http-mcp.service
```

The HTTP MCP service runs as `ubuntu`, listens only on `127.0.0.1:18082`, and
uses `/opt/im2a/v2.5.3/data`. The gateway listens on `:18080`.

## Configuration correction

The pre-upgrade HTTP MCP environment had two defects:

```text
IM2A_HARNESS_ID=tencent-dev       # not a registered harness id
IM2A_HARNESS_TOKEN=<stale token>  # did not match gateway GATEWAY_TOKEN
```

They were corrected to:

```text
IM2A_HARNESS_ID=deepseek-harness
IM2A_HARNESS_TOKEN=<current gateway GATEWAY_TOKEN>
```

The token is stored only in `/etc/im2a/http-mcp.env` (0600 root:root) and is
never printed by the verification scripts.

## Verification

```text
gateway service                 active, 0 restarts
gateway /healthz                200, version v2.5.3
gateway -selftest               SELF-TEST PASS
HTTP MCP service                active, 0 restarts
HTTP MCP /healthz               200, 13 tools
im2a_whoami / im2a_presence     PASS
im2a_send / im2a_recv / history PASS
skills_list / memory_search     PASS (real SQLite/FTS5)
Go MCP initialize               im2a-harness-bridge 0.3.0
Go MCP tools/list               13
Go MCP whoami / presence        PASS
```

The real dual-session SSE test registered two sessions with separate capability
tokens and verified:

```text
A receives B -> A
A does not receive B -> B
A presence snapshot contains only A
A cannot read B metadata/history
stream without capability -> 401
```

Result: 17 checks, 17 passed, 0 failed.

Evidence:

```text
tests/TENCENT-V253-DEPLOYMENT-EVIDENCE.json
tests/TENCENT-V253-SSE-EVIDENCE.json
```

## Backups and rollback

```text
/opt/backups/im2a-gateway-pre-v2.5.3-20261031145338/
  im2a-gateway
  im2a-gateway.service
  env
  selfcheck.sh
  SHA256SUMS

/opt/backups/im2a-mcp-pre-v2.5.3-20261031151002/
  im2a-mcp
  server.mjs
  im2a-http-mcp.service
  http-mcp.env
  data-pre-v2.5.3.tar.gz
  SHA256SUMS
```

Gateway rollback:

```bash
sudo install -m 0755 /opt/backups/im2a-gateway-pre-v2.5.3-20261031145338/im2a-gateway /opt/im2a/bin/im2a-gateway
sudo systemctl restart im2a-gateway
```

HTTP MCP rollback:

```bash
sudo systemctl stop im2a-http-mcp.service
sudo install -m 0644 /opt/backups/im2a-mcp-pre-v2.5.3-20261031151002/im2a-http-mcp.service /etc/systemd/system/im2a-http-mcp.service
sudo install -m 0600 /opt/backups/im2a-mcp-pre-v2.5.3-20261031151002/http-mcp.env /etc/im2a/http-mcp.env
sudo install -m 0755 /opt/backups/im2a-mcp-pre-v2.5.3-20261031151002/im2a-mcp /opt/im2a/v2.5.2/bin/im2a-mcp
sudo systemctl daemon-reload
sudo systemctl start im2a-http-mcp.service
```

The old `http-mcp.env` restored by this procedure still has the stale harness
id/token; it is a byte-for-byte rollback, not a working v2.5.3 configuration.

## Not claimed

```text
production readiness
TLS termination
per-user / per-tenant authorization
public endpoint
multi-instance SSE bus
```

## Residual risk

- The Tencent host still authorized the retired SSH deploy key and rejected the
  rotated key; this deployment used the retired key. SSH key rotation on this
  host is pending.
- The gateway health reports `commit=dev`; the asset SHA-256 was verified, but
  the build commit is not embedded in the binary.
- The gateway bearer token has no per-user identity.
- The session capability is a bearer token.
- The `session_token` query fallback can leak through URLs/logs.
- The gateway listens on all interfaces. A public probe from the Windows host
  on 2026-10-31 found both 18080 and 18082 closed, but that is a point-in-time
  firewall observation, not a guarantee.
- Agent Plugins 2.0 is still a draft protocol.
- v2.5.2 remains NO-GO for production because of the SSE leak.
- v2.4.6 remains a rollback candidate only until its published plugin asset
  checksum is repaired.
