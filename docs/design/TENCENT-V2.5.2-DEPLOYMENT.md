# Tencent Cloud v2.5.2 Deployment

Date: 2026-10-31
Resource: `ins-j3ou8dmy`
Host: `134.175.243.146` (public) / `192.168.0.5` (internal)
OS: Ubuntu Server 24.04 LTS

## Deployed layout

```text
/opt/im2a/v2.5.2/
  bin/im2a-mcp                    # Go stdio MCP, 13 tools
  packages/im2a-http-mcp/server.mjs
  packages/skill-runtime/src/
  packages/memory-hub/src/
  data/skills.db                  # SQLite
  data/memory.db                  # SQLite FTS5
  systemd/im2a-http-mcp.service
  deploy.sh
/opt/im2a/node/                   # Node.js 24.16.0
/etc/im2a/http-mcp.env            # root:root 0600
```

## Runtime

```text
im2a-http-mcp.service: active
bind: 127.0.0.1:18082 only
healthz: 200
tools: 13
Go stdio MCP: 13 tools
skills_list: real SQLite
memory_search: real FTS5
public 18082: closed
existing services:
  im2a-gateway: active
  im2a-vultr-tunnel: active
  openclaw-gateway: active
```

## Verification

```text
HTTP healthz: 200
tools/list: 13
skills_list: PASS
memory_search: PASS (seeded smoke record)
Go MCP → HTTP MCP → FTS5: PASS
gateway 18080: 200
service active: PASS
```

Evidence:

```text
tests/TENCENT-V252-HTTP-EVIDENCE.json
tests/TENCENT-V252-GO-EVIDENCE.json
```

Smoke data was removed after verification; data directory contains empty
initialized DBs.

## Backup / rollback

```text
/opt/backups/im2a-pre-v2.5.2.tgz  (89 MB; pre-deploy /opt/im2a minus v2.5.2)
/opt/backups/im2a-v2.5.2.tgz      (5.6 MB; deployed tree)
```

Rollback:

```bash
sudo systemctl disable --now im2a-http-mcp.service
sudo rm -rf /opt/im2a/v2.5.2
# existing im2a-gateway / tunnel / openclaw services are untouched
```

Do not publicly expose 18082. The service is loopback-only and intended to be
reached through the existing tunnel/private API surface.

## Not claimed

```text
TLS termination
multi-tenant authentication
public endpoint
production HA/backup automation
```

The server has a single local tenant (`tencent-dev`); gateway tools depend on
the existing local tunnel/token and were not changed.
