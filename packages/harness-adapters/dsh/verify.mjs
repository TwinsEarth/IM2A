/**
 * DSH/MCP verification helper.
 *
 * The model-facing truth is the JSON-RPC response for each request id, not a
 * substring in a combined stdout. This module keeps the three phases separate:
 * initialize -> tools/list -> tools/call.
 */
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";

export const TOOL_NAME = "im2a_harness_list";

export function classifyMcpFailure(text = "") {
  if (/HTTP\s+(401|403)\b/i.test(text)) return { status: "failed", code: "AUTH_INVALID" };
  if (/HTTP\s+404\b/i.test(text)) return { status: "failed", code: "ROUTE_NOT_FOUND" };
  if (/HTTP\s+5\d\d\b/i.test(text)) return { status: "failed", code: "GATEWAY_5XX" };
  if (/dial tcp|connection refused|connectex|no such host|network is unreachable/i.test(text)) {
    return { status: "blocked", code: "GATEWAY_UNREACHABLE" };
  }
  if (/timeout|timed out|deadline exceeded/i.test(text)) return { status: "failed", code: "TIMEOUT" };
  return { status: "failed", code: "TOOL_FAILED" };
}

export async function verifyMcp({
  mcpBin,
  env = process.env,
  url = "http://127.0.0.1:18080",
  timeoutMs = 20000,
  spawnImpl = spawn,
} = {}) {
  const checks = [];
  let child;
  const pending = new Map();
  let nextId = 1;
  let closed = false;

  const finishPending = (error) => {
    for (const { reject, timer } of pending.values()) {
      clearTimeout(timer);
      reject(error);
    }
    pending.clear();
  };

  try {
    child = spawnImpl(mcpBin, [], {
      env: { ...env, IM2A_HARNESS_URL: url },
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });
  } catch (error) {
    return {
      ok: false,
      status: "blocked",
      code: "SPAWN_BLOCKED",
      checks: [{ name: "mcp-spawn", ok: false, detail: String(error.message) }],
    };
  }

  const rl = createInterface({ input: child.stdout });
  rl.on("line", (line) => {
    let message;
    try { message = JSON.parse(line); } catch { return; }
    if (message.id !== undefined && pending.has(message.id)) {
      const waiter = pending.get(message.id);
      pending.delete(message.id);
      clearTimeout(waiter.timer);
      waiter.resolve(message);
    }
  });

  const request = (method, params) => {
    const id = nextId++;
    const payload = { jsonrpc: "2.0", id, method, params };
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error(`${method} timeout`));
      }, timeoutMs);
      pending.set(id, { resolve, reject, timer });
      child.stdin.write(JSON.stringify(payload) + "\n", (error) => {
        if (error) {
          clearTimeout(timer);
          pending.delete(id);
          reject(error);
        }
      });
    });
  };

  const notify = (method, params) => {
    child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method, params }) + "\n");
  };

  child.on("error", (error) => {
    closed = true;
    finishPending(error);
  });

  try {
    const init = await request("initialize", {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: { name: "im2a-dsh-adapter", version: "0.2.0" },
    });
    checks.push({
      name: "initialize",
      ok: Boolean(init.result?.serverInfo) && !init.error,
      detail: init.error ? JSON.stringify(init.error) : init.result?.serverInfo?.name,
    });
    if (!checks.at(-1).ok) throw new Error("initialize failed");

    notify("notifications/initialized", {});

    const tools = await request("tools/list", {});
    const toolNames = tools.result?.tools?.map((tool) => tool.name) ?? [];
    checks.push({
      name: "tools/list",
      ok: !tools.error && toolNames.includes(TOOL_NAME),
      detail: toolNames.includes(TOOL_NAME) ? TOOL_NAME : `missing ${TOOL_NAME}`,
    });
    if (!checks.at(-1).ok) throw new Error("tools/list failed");

    const call = await request("tools/call", { name: TOOL_NAME, arguments: {} });
    const text = call.result?.content?.map((item) => item.text ?? "").join("\n") ?? "";
    const isError = call.result?.isError === true || Boolean(call.error);
    let harnessCount = 0;
    if (!isError) {
      try {
        const parsed = JSON.parse(text);
        harnessCount = Array.isArray(parsed.harnesses) ? parsed.harnesses.length : 0;
      } catch {
        harnessCount = 0;
      }
    }
    checks.push({
      name: "tools/call",
      ok: !isError && harnessCount > 0,
      detail: isError ? `${JSON.stringify(call.error ?? call.result?.content?.[0]?.text ?? "").slice(0, 240)}` : `harnesses=${harnessCount}`,
    });

    child.stdin.end();
    await new Promise((resolve) => child.once("close", resolve)).catch(() => {});
    const failed = checks.find((check) => !check.ok);
    if (!failed) {
      return { ok: true, status: "passed", code: null, harnessCount, checks };
    }
    const detail = failed.detail ?? "";
    const classified = classifyMcpFailure(detail);
    return { ok: false, status: classified.status, code: classified.code, checks };
  } catch (error) {
    const message = String(error.message ?? error);
    const classified = classifyMcpFailure(message);
    checks.push({ name: "mcp-verify", ok: false, detail: message });
    return {
      ok: false,
      status: error.message?.includes("timeout") ? "failed" : (classified.status === "blocked" ? "blocked" : "failed"),
      code: error.message?.includes("timeout") ? "TIMEOUT" : classified.code,
      checks,
    };
  } finally {
    if (!closed) {
      closed = true;
      try { child.stdin.end(); } catch {}
      try { child.kill(); } catch {}
    }
    rl.close();
    finishPending(new Error("verify closed"));
  }
}
