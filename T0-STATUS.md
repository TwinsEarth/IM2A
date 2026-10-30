# T0 Adapter Status — 2026-10-30

## Locked baseline

```text
DSH runtime: 0.2.1-rc.3
DSH host plugin: @twinsearth/im2a-dsh-plugin 0.1.0
```

## Verified

```text
dsh: verified (sandbox DSH ToolRuntime)
evidence:
  P0-EVIDENCE.json
  P0-DSH-TOOL-CALL.json
```

## Blocked / not run

| Adapter | Check | Status |
|---|---|---|
| Qwen Code | `qwen` / `qwen-code` command present | missing |
| MiMo Code | `mimo` / `mimo-code` command present | missing |
| OpenClaw | local `openclaw` command present | missing |

No Qwen Code, MiMo Code, or OpenClaw adapter can be truthfully verified on this
host until the target CLI/runtime is installed and a sandbox profile is
available. Do not substitute the DSH adapter result for those targets.

## Live

The DSH host bundle was briefly installed in the desktop profile during the
live attempt, then removed with `dsh plugin --profile desktop remove`.
The live profile currently has no `im2a` package reference.

Pre-install backup:

```text
C:\Users\fangw\.dsh\profiles\desktop\.im2a-live-backup-20261030081239
```

Live maintenance window, live healthz, and live rollback drill remain pending.

## Prepared in isolation (2026-10-30)

```text
profile: .tmp/dsh-tools-home/profiles/t0-tools
Qwen Code: 0.26.3
MiMo Code: 0.1.16
wrappers:
  node_modules/.bin/qwen.CMD
  node_modules/.bin/mimo.CMD
```

Install note: pnpm reported ignored build scripts and returned exit 1, but both
`qwen --version` and `mimo --version` execute successfully. Authentication and
adapter verification are not done; these are prepared tools, not verified T0
adapters.
## DSH compatibility incident (2026-10-30)

```text
DSH runtime: 0.2.1-rc.5
old host/live plugins: 0.1.0, engines.dsh 0.2.1-rc.3
action: removed live overlay + host bundle
new host plugin: 0.1.1, engines.dsh 0.2.1-rc.5
sandbox: install + --dump-config PASS, no incompatibility warning
live: not reinstalled (six-party release first)
```

Old P0-LIVE-DSH-TOOL-CALL evidence is rc.3-era and must not be presented as
current rc.5 live verification.