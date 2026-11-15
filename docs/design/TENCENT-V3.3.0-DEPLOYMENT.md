# Tencent dev host: v3.3.0 deployment

Date: 2026-11-15
Host: `ins-j3ou8dmy` / 134.175.243.146 (Ubuntu 24.04, Node v24.16.0)
Scope: replace the bridge at `/opt/im2a/v2.7.2` with `/opt/im2a/v3.3.0`.

## What moved

| Item | Before | After |
| --- | --- | --- |
| WorkingDirectory | `/opt/im2a/v2.7.2` | `/opt/im2a/v3.3.0` |
| Plugin | 2.7.2 | **3.3.0** (kernel 1.1.0) |
| MCP tools | 30 | **32** |
| Runtime modules | — | 6 (0 missing) |

Payload: `im2a-deploy-v330.tgz`, 121,859 bytes, sha256
`e57f6ec24dd3f5ae9133dbaa983399b6e802433244e25fd02e92affe00940a98`, 72 files:
`im2a-http-mcp`, `memory-hub`, `skill-runtime`, `dsh-im2a-plugin` (manifest + 6 kernel
modules + alias table), plus `docs/contracts`.

Data was migrated with `cp -a /opt/im2a/v2.7.2/data /opt/im2a/v3.3.0/data`
(skills.db, memory.db, goal-buffer.db, drives, traces, skill-guard, drafts).

## Verification (live)

```text
systemctl is-active im2a-http-mcp      active
healthz                                200
tools/list                             32
im2a_kernel_status                     dsh-im2a-plugin@3.3.0 kernel=1.1.0
                                       6 runtime modules, 0 missing, 9 modules
governance_manage action=propose       pending proposal created, effective_scopes []
governance_manage action=status        1 proposal, 0 effective scope
im2a_whoami                            session hs_659caf9aa9be
```

Module states observed: memory/evolution/asset/autonomy/fleet `available`;
identity/economy/governance/observability `partial` — matching the manifest.

The propose call proves the write path works under `ProtectSystem=strict` with
`ReadWritePaths=/opt/im2a/v3.3.0/data`: the proposal landed in
`data/governance/proposals.jsonl`. That smoke artifact was removed afterwards.

## Service unit

`/etc/systemd/system/im2a-http-mcp.service` was rewritten with the same shape as the
2.7.2 unit, only paths changed (`v2.7.2` → `v3.3.0`), still running as `ubuntu`, still
bound to `127.0.0.1:18082`.

## Rollback

```bash
sudo cp /tmp/unit.bak /etc/systemd/system/im2a-http-mcp.service
sudo systemctl daemon-reload && sudo systemctl restart im2a-http-mcp
# expect healthz 200 and tools/list = 30
```

`/opt/im2a/v2.7.2` was left untouched, so rollback is a unit swap.

## Not claimed

- production readiness; this is the dev host
- TLS or public exposure of 18082 (still loopback only)
- the gateway on 18080 was not upgraded
- data written after cutover is not mirrored back into the v2.7.2 tree
