/**
 * Minimal Streamable HTTP MCP server for IM2A.
 *
 * It replaces the stdio child process with an HTTP endpoint so a DSH profile
 * can exercise MCP without spawning a child (the DSH file sandbox denies
 * child_process pipes on this host).
 *
 * P0-6 scope: initialize, tools/list, tools/call for harness_list/presence.
 * im2a_send/recv/history remain stdio-only until session state is implemented.
 */
import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { SqliteSkillStore } from "../skill-runtime/src/sqlite-store.mjs";
import { MemoryHub } from "../memory-hub/src/index.mjs";

const PORT = Number(process.env.IM2A_HTTP_MCP_PORT ?? 18082);
const HOST = process.env.IM2A_HTTP_MCP_HOST ?? "127.0.0.1";
const GATEWAY = (process.env.IM2A_HARNESS_URL ?? "http://127.0.0.1:18080").replace(/\/$/, "");
const TOKEN = process.env.IM2A_HARNESS_TOKEN ?? "";
const SESSION_ID = randomUUID();
const PROTOCOL_VERSION = "2024-11-05";
const HARNESS_ID = process.env.IM2A_HARNESS_ID ?? "deepseek-harness";
const AGENT_NAME = process.env.IM2A_AGENT_NAME ?? "dsh";
const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const DB_DIR = process.env.IM2A_MCP_DB_DIR ?? join(ROOT, ".tmp", "im2a-mcp");
mkdirSync(DB_DIR, { recursive: true });
const skillStore = new SqliteSkillStore({ path: join(DB_DIR, "skills.db") });
const memoryHub = new MemoryHub({
  path: join(DB_DIR, "memory.db"),
  tenant: process.env.IM2A_TENANT_ID ?? "local",
});

let gatewaySession = null;
let gatewaySessionToken = "";
let lastSeen = 0;

const TOOLS = [
  {
    name: "im2a_harness_list",
    description: "List IM2A-supported harnesses and the current selection.",
    inputSchema: { type: "object", properties: {}, required: [] },
  },
  {
    name: "im2a_send",
    description: "Send a message to another harness/agent through the IM2A bus.",
    inputSchema: {
      type: "object",
      properties: {
        to: { type: "string", description: "broadcast | harness:<id> | agent:<name> | <session id>" },
        text: { type: "string", maxLength: 8000 },
        meta: { type: "object" },
      },
      required: ["text"],
    },
  },
  {
    name: "im2a_recv",
    description: "Read new IM2A bus messages for this HTTP bridge session.",
    inputSchema: {
      type: "object",
      properties: { limit: { type: "integer", minimum: 1, maximum: 200, default: 50 } },
      required: [],
    },
  },
  {
    name: "im2a_presence",
    description: "List online IM2A harness sessions.",
    inputSchema: { type: "object", properties: {}, required: [] },
  },
  {
    name: "im2a_whoami",
    description: "Show this HTTP bridge's harness, agent and session identity.",
    inputSchema: { type: "object", properties: {}, required: [] },
  },
  {
    name: "im2a_history",
    description: "Read IM2A bus history for this HTTP bridge session.",
    inputSchema: {
      type: "object",
      properties: {
        since: { type: "integer", minimum: 0, default: 0 },
        limit: { type: "integer", minimum: 1, maximum: 200, default: 50 },
      },
      required: [],
    },
  },
  {
    name: "skills_list",
    description: "L0 skill catalog: id, description, version and package hash; no skill body.",
    inputSchema: {
      type: "object",
      properties: {
        query: { type: "string" },
        limit: { type: "integer", minimum: 1, maximum: 200, default: 50 },
      },
      required: [],
    },
  },
  {
    name: "skill_view",
    description: "L1 skill body plus file list, or L2 one reference file with offset/limit.",
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string" },
        path: { type: "string" },
        offset: { type: "integer", minimum: 0, default: 0 },
        limit: { type: "integer", minimum: 1, maximum: 65536, default: 65536 },
        sessionId: { type: "string" },
      },
      required: ["id"],
    },
  },
  {
    name: "skill_manage",
    description: "Create a skill proposal; never mutates active. Promotion is operator-only.",
    inputSchema: {
      type: "object",
      properties: {
        action: { type: "string", enum: ["create", "patch", "archive"] },
        id: { type: "string" },
        baseSha: { type: "string" },
        content: { type: "string" },
        files: { type: "object" },
      },
      required: ["action", "id"],
    },
  },
  {
    name: "curator",
    description: "Curator status/scan/report or stage a proposal; cannot promote or rollback.",
    inputSchema: {
      type: "object",
      properties: {
        action: { type: "string", enum: ["status", "scan", "report", "stage"] },
        proposalId: { type: "string" },
        evidence: { type: "array", items: { type: "string" } },
        evalCases: { type: "array", items: { type: "string" } },
      },
      required: ["action"],
    },
  },
  {
    name: "memory_search",
    description: "Search the tenant-scoped FTS5 memory hub; returns snippets, not full bodies.",
    inputSchema: {
      type: "object",
      properties: {
        query: { type: "string" },
        limit: { type: "integer", minimum: 1, maximum: 100, default: 20 },
      },
      required: ["query"],
    },
  },
  {
    name: "memory_get",
    description: "Read one tenant-scoped memory document by id.",
    inputSchema: {
      type: "object",
      properties: { id: { type: "string" } },
      required: ["id"],
    },
  },
  {
    name: "memory_propose",
    description: "Propose a memory document; does not become searchable until operator promotion.",
    inputSchema: {
      type: "object",
      properties: {
        kind: { type: "string", default: "fact" },
        title: { type: "string" },
        body: { type: "string" },
        metadata: { type: "object" },
      },
      required: ["title", "body"],
    },
  },
];

function sendJson(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json",
    "mcp-session-id": SESSION_ID,
    "content-length": Buffer.byteLength(raw),
  });
  res.end(raw);
}

function rpcError(id, code, message) {
  return { jsonrpc: "2.0", id, error: { code, message } };
}

async function gatewayRequest(pathname, { method = "GET", body } = {}) {
  if (!TOKEN) throw new Error("IM2A_HARNESS_TOKEN missing");
  const response = await fetch(`${GATEWAY}${pathname}`, {
    method,
    headers: {
      authorization: `Bearer ${TOKEN}`,
      ...(gatewaySessionToken ? { "x-im2a-session-token": gatewaySessionToken } : {}),
      ...(body === undefined ? {} : { "content-type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10000),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`gateway HTTP ${response.status}: ${text.slice(0, 200)}`);
  return text;
}

async function ensureSession() {
  if (gatewaySession) return gatewaySession;
  const text = await gatewayRequest("/v1/harness/sessions", {
    method: "POST",
    body: { harness: HARNESS_ID, agent: AGENT_NAME, displayName: "im2a-http-mcp" },
  });
  const parsed = JSON.parse(text);
  gatewaySession = parsed.session ?? parsed;
  gatewaySessionToken = String(parsed.token ?? "");
  if (!gatewaySession?.id) throw new Error("gateway did not return a harness session id");
  if (!gatewaySessionToken) throw new Error("gateway did not return a session capability token");
  return gatewaySession;
}

async function gatewayJson(pathname) {
  return gatewayRequest(pathname);
}

function resultText(text, isError = false) {
  return {
    resultType: "complete",
    content: [{ type: "text", text: typeof text === "string" ? text : JSON.stringify(text) }],
    ...(isError ? { isError: true } : {}),
  };
}

async function handleRpc(message) {
  const { id, method, params } = message;
  if (method === "server/discover") {
    return {
      jsonrpc: "2.0",
      id,
      result: {
        resultType: "complete",
        supportedVersions: ["2026-07-28"],
        capabilities: { tools: {} },
        _meta: {
          "io.modelcontextprotocol/serverInfo": {
            name: "im2a-http-mcp",
            version: "0.1.0",
          },
        },
        instructions: "IM2A Agent Plugins 2.0 bridge: thirteen tools (IM2A bus, skills, memory).",
        ttlMs: 3600000,
        cacheScope: "public",
      },
    };
  }
  if (method === "initialize") {
    return {
      jsonrpc: "2.0",
      id,
      result: {
        protocolVersion: params?.protocolVersion ?? PROTOCOL_VERSION,
        resultType: "complete",
        capabilities: { tools: {} },
        serverInfo: { name: "im2a-http-mcp", version: "0.1.0" },
        instructions: "IM2A Agent Plugins 2.0 bridge: thirteen tools (IM2A bus, skills, memory).",
      },
    };
  }
  if (method === "notifications/initialized") return null;
  if (method === "ping") return { jsonrpc: "2.0", id, result: { resultType: "complete" } };
  if (method === "tools/list") {
    return {
      jsonrpc: "2.0",
      id,
      result: {
        resultType: "complete",
        tools: TOOLS,
        ttlMs: 3600000,
        cacheScope: "public",
      },
    };
  }
  if (method === "tools/call") {
    const name = params?.name;
    const args = params?.arguments ?? {};
    try {
      if (name === "im2a_harness_list") {
        return { jsonrpc: "2.0", id, result: resultText(await gatewayJson("/v1/harness/registry")) };
      }
      if (name === "im2a_presence") {
        const session = await ensureSession();
        const text = await gatewayRequest(`/v1/harness/sessions?session=${encodeURIComponent(session.id)}`);
        return { jsonrpc: "2.0", id, result: resultText(text) };
      }
      if (name === "im2a_whoami") {
        const session = await ensureSession();
        return {
          jsonrpc: "2.0",
          id,
          result: resultText({
            harness: HARNESS_ID,
            agent: AGENT_NAME,
            session,
            transport: "streamable-http",
          }),
        };
      }
      if (name === "im2a_send") {
        if (typeof args.text !== "string" || args.text.length === 0) throw new Error("text is required");
        if (args.text.length > 8000) throw new Error("text exceeds 8000 bytes");
        const session = await ensureSession();
        const text = await gatewayRequest("/v1/harness/messages", {
          method: "POST",
          body: {
            from: session.id,
            to: typeof args.to === "string" && args.to ? args.to : "broadcast",
            text: args.text,
            meta: args.meta ?? {},
          },
        });
        return { jsonrpc: "2.0", id, result: resultText(text) };
      }
      if (name === "im2a_recv") {
        const session = await ensureSession();
        const limit = Math.min(Math.max(Number(args.limit ?? 50), 1), 200);
        const text = await gatewayRequest(`/v1/harness/messages?session=${encodeURIComponent(session.id)}&since=${lastSeen}&limit=${limit}`);
        const parsed = JSON.parse(text);
        for (const message of parsed.messages ?? []) {
          if (typeof message.id === "number" && message.id > lastSeen) lastSeen = message.id;
        }
        return { jsonrpc: "2.0", id, result: resultText({ messages: parsed.messages ?? [], count: parsed.count ?? parsed.messages?.length ?? 0 }) };
      }
      if (name === "im2a_history") {
        const session = await ensureSession();
        const since = Math.max(Number(args.since ?? 0), 0);
        const limit = Math.min(Math.max(Number(args.limit ?? 50), 1), 200);
        const text = await gatewayRequest(`/v1/harness/messages?session=${encodeURIComponent(session.id)}&since=${since}&limit=${limit}`);
        return { jsonrpc: "2.0", id, result: resultText(text) };
      }
      if (name === "skills_list") {
        const query = String(args.query ?? '').toLowerCase();
        const limit = Math.min(Math.max(Number(args.limit ?? 50), 1), 200);
        const items = skillStore.list()
          .filter((item) => !query || item.id.includes(query) || item.description.toLowerCase().includes(query) || item.tags.some((tag) => String(tag).toLowerCase().includes(query)))
          .slice(0, limit);
        return { jsonrpc: "2.0", id, result: resultText({ items, count: items.length }) };
      }
      if (name === "skill_view") {
        const result = skillStore.view(String(args.id), {
          path: args.path ? String(args.path) : undefined,
          offset: Math.max(Number(args.offset ?? 0), 0),
          limit: Math.min(Math.max(Number(args.limit ?? 65536), 1), 65536),
          sessionId: args.sessionId ? String(args.sessionId) : undefined,
        });
        return { jsonrpc: "2.0", id, result: resultText(result) };
      }
      if (name === "skill_manage") {
        const result = skillStore.manage({
          action: String(args.action),
          id: String(args.id),
          baseSha: args.baseSha ? String(args.baseSha) : undefined,
          content: args.content === undefined ? undefined : String(args.content),
          files: args.files && typeof args.files === 'object' ? args.files : undefined,
          proposer: AGENT_NAME,
        });
        return { jsonrpc: "2.0", id, result: resultText(result) };
      }
      if (name === "curator") {
        const action = String(args.action);
        const result = action === 'stage'
          ? skillStore.stage(String(args.proposalId), {
              evidence: Array.isArray(args.evidence) ? args.evidence.map(String) : [],
              evalCases: Array.isArray(args.evalCases) ? args.evalCases.map(String) : [],
              stagedBy: AGENT_NAME,
            })
          : skillStore.curator({ action });
        return { jsonrpc: "2.0", id, result: resultText(result) };
      }
      if (name === "memory_search") {
        if (typeof args.query !== 'string') throw new Error('query is required');
        const items = memoryHub.search(args.query, { limit: Math.min(Math.max(Number(args.limit ?? 20), 1), 100) });
        return { jsonrpc: "2.0", id, result: resultText({ items, count: items.length }) };
      }
      if (name === "memory_get") {
        const item = memoryHub.get(String(args.id));
        return { jsonrpc: "2.0", id, result: resultText({ found: Boolean(item), item }) };
      }
      if (name === "memory_propose") {
        const result = memoryHub.propose({
          kind: args.kind ? String(args.kind) : 'fact',
          title: String(args.title ?? ''),
          body: String(args.body ?? ''),
          metadata: args.metadata && typeof args.metadata === 'object' ? args.metadata : {},
        });
        return { jsonrpc: "2.0", id, result: resultText(result) };
      }
      return { jsonrpc: "2.0", id, result: resultText(`unknown tool: ${name}`, true) };
    } catch (error) {
      return { jsonrpc: "2.0", id, result: resultText(String(error.message), true) };
    }
  }
  return rpcError(id, -32601, `method not found: ${method}`);
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "localhost"}`);
  if (req.method === "GET" && url.pathname === "/healthz") {
    sendJson(res, 200, { ok: true, transport: "streamable-http", tools: TOOLS.map((tool) => tool.name) });
    return;
  }
  if (req.method === "GET" && url.pathname === "/mcp") {
    res.writeHead(200, {
      "content-type": "text/event-stream",
      "cache-control": "no-cache",
      connection: "keep-alive",
      "mcp-session-id": SESSION_ID,
    });
    res.write(": im2a http mcp ready\n\n");
    const timer = setInterval(() => res.write(": keep-alive\n\n"), 15000);
    req.on("close", () => clearInterval(timer));
    return;
  }
  if (req.method !== "POST" || url.pathname !== "/mcp") {
    sendJson(res, 404, { error: "not found" });
    return;
  }
  let raw = "";
  for await (const chunk of req) raw += chunk;
  let payload;
  try { payload = JSON.parse(raw); } catch {
    sendJson(res, 400, rpcError(null, -32700, "parse error"));
    return;
  }
  const messages = Array.isArray(payload) ? payload : [payload];
  for (const message of messages) {
    console.log(JSON.stringify({ event: "rpc", method: message.method ?? null, id: message.id ?? null }));
  }
  const responses = [];
  for (const message of messages) {
    const response = await handleRpc(message);
    if (response) responses.push(response);
  }
  if (responses.length === 0) {
    res.writeHead(202, { "mcp-session-id": SESSION_ID });
    res.end();
    return;
  }
  sendJson(res, 200, responses.length === 1 ? responses[0] : responses);
});

server.listen(PORT, HOST, () => {
  console.log(JSON.stringify({ ok: true, transport: "streamable-http", host: HOST, port: PORT, gateway: GATEWAY, tools: TOOLS.map((tool) => tool.name) }));
});

process.on("SIGTERM", () => server.close(() => { try { skillStore.close(); memoryHub.close(); } catch {} process.exit(0); }));
process.on("SIGINT", () => server.close(() => { try { skillStore.close(); memoryHub.close(); } catch {} process.exit(0); }));
