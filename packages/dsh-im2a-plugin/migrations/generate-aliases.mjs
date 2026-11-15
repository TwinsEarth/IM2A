#!/usr/bin/env node
// Generates migrations/3.2.1-tool-aliases.json from the running IM2A MCP bridge.
// The old -> new mapping is frozen here; schema hashes come from tools/list so the
// alias table can detect silent schema drift.

import { createHash } from "node:crypto";
import { writeFile, mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const OUT = join(HERE, "3.2.1-tool-aliases.json");
const ENDPOINT = process.env.IM2A_HTTP_MCP_URL ?? "http://127.0.0.1:18082/mcp";

// module -> { legacy tool name: public suffix inside that module }
export const TOOL_SUFFIX = {
  memory: {
    memory_search: "search",
    memory_get: "get",
    memory_propose: "propose",
    memory_recall_v2: "recall",
  },
  asset: {
    skill_view: "view",
    skill_manage: "manage",
    skill_claim: "claim",
    skills_list: "list",
    contribution_audit: "audit",
    skill_guard_check: "guard_check",
  },
  autonomy: {
    drive_list: "drive_list",
    drive_manage: "drive_manage",
    drive_propose: "propose",
    drive_calibrate: "calibrate",
    goal_buffer: "buffer",
    goal_schedule: "schedule",
    scheduler_cycle: "scheduler_cycle",
    scheduler_audit: "scheduler_audit",
  },
  evolution: {
    trace_ingest: "ingest",
    reflect_trigger: "reflect",
    curator: "curator",
    curator_audit: "curator_audit",
  },
  fleet: {
    im2a_harness_list: "harness_list",
    im2a_send: "send",
    im2a_recv: "recv",
    im2a_presence: "presence",
    im2a_history: "history",
  },
  observability: {
    evolution_pipeline_dryrun: "pipeline_dryrun",
    autonomy_pipeline_dryrun: "pipeline_dryrun",
  },
  kernel: {
    im2a_whoami: "whoami",
  },
};

// Two legacy dry-runs collapse into one public tool, split by a fixed argument.
export const MERGED_ALIASES = {
  evolution_pipeline_dryrun: "evolution",
  autonomy_pipeline_dryrun: "autonomy",
};

// Tools that were born on the canonical side: they have no legacy name to migrate
// from, so the alias table must not invent an entry for them. The generator still
// knows them, which keeps the "tool set drift" check honest instead of loosening it.
export const CANONICAL_ONLY = new Set([
  "governance_manage",
  "im2a_kernel_status",
]);

export function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") {
    const keys = Object.keys(value).sort();
    return `{${keys.map((k) => `${JSON.stringify(k)}:${canonical(value[k])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

export function schemaHash(tool) {
  const shape = {
    name: tool.name,
    description: tool.description ?? "",
    inputSchema: tool.inputSchema ?? tool.input_schema ?? {},
  };
  return `sha256:${createHash("sha256").update(canonical(shape)).digest("hex")}`;
}

export function publicName(module, oldName) {
  const suffix = TOOL_SUFFIX[module]?.[oldName];
  if (!suffix) throw new Error(`no mapping for ${module}/${oldName}`);
  return `im2a_${module}_${suffix}`;
}

export function declaredLegacyNames() {
  return Object.values(TOOL_SUFFIX).flatMap((tools) => Object.keys(tools));
}

async function rpc(method, params = {}, { url = ENDPOINT } = {}) {
  const res = await fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json, text/event-stream" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }),
  });
  if (!res.ok) throw new Error(`${method}: HTTP ${res.status}`);
  const body = await res.json();
  if (body.error) throw new Error(`${method}: ${body.error.message}`);
  return body.result;
}

export async function buildAliasTable({ url = ENDPOINT } = {}) {
  await rpc(
    "initialize",
    {
      protocolVersion: "2025-06-18",
      capabilities: {},
      clientInfo: { name: "alias-generator", version: "0" },
    },
    { url },
  );

  const { tools } = await rpc("tools/list", {}, { url });
  const byName = new Map(tools.map((t) => [t.name, t]));

  const declared = declaredLegacyNames();
  const missing = declared.filter((n) => !byName.has(n));
  const missingCanonical = [...CANONICAL_ONLY].filter((n) => !byName.has(n));
  const extra = [...byName.keys()].filter((n) => !declared.includes(n) && !CANONICAL_ONLY.has(n));
  if (missing.length || extra.length || missingCanonical.length) {
    throw new Error(
      `tool set drift: missing=[${missing}] extra=[${extra}] missingCanonicalOnly=[${missingCanonical}]`,
    );
  }

  const entries = [];
  for (const [module, toolsByName] of Object.entries(TOOL_SUFFIX)) {
    for (const oldName of Object.keys(toolsByName)) {
      const entry = {
        old_name: oldName,
        new_name: publicName(module, oldName),
        module,
        since: "3.2.1",
        deprecated_after: "3.4.0",
        removed_in: null,
        status: "active",
        schema_hash: schemaHash(byName.get(oldName)),
      };
      if (MERGED_ALIASES[oldName]) entry.fixed_args = { pipeline: MERGED_ALIASES[oldName] };
      entries.push(entry);
    }
  }

  entries.sort((a, b) => a.old_name.localeCompare(b.old_name));
  return {
    schemaVersion: "1.0",
    generatedFrom: {
      server: "im2a-http-mcp",
      protocolVersion: "2.0",
      toolCount: tools.length,
    },
    deprecatedAfter: "3.4.0",
    entries,
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const table = await buildAliasTable();
  await mkdir(dirname(OUT), { recursive: true });
  await writeFile(OUT, `${JSON.stringify(table, null, 2)}\n`, "utf8");
  console.log(`wrote ${OUT} (${table.entries.length} entries)`);
}
