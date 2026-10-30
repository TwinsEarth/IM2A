# Harness Adapters (P0)

12 adapters, three tiers. A `planned` or `observed` adapter is **not** a supported adapter until a real MCP tool call and rollback have passed.

| Tier | Meaning | Adapters |
|---|---|---|
| T0 | Fully controllable now | DSH, Qwen Code, MiMo Code, OpenClaw |
| T1 | Ecosystem targets | Codex, Claude, Gemini CLI, Copilot, VS Code |
| T2 | Observe / manual first | WorkBuddy, Doubao, Muse |

Contract: `src/contract.ts`. Descriptor schema: `schemas/adapter.schema.json`.

Rules:

1. `detect()` and `plan()` never mutate.
2. `apply()` is atomic and creates backups.
3. `verify()` performs one real MCP tool call or returns an explicit unsupported result.
4. `rollback()` restores all backups.
5. A manifest entry may not be `verified` without evidence.
