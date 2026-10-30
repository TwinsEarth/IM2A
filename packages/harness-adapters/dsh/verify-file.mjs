/**
 * File-descriptor MCP verification (sandbox fallback).
 *
 * Windows DSH sandbox denies Node child_process pipes with EPERM. The MCP
 * server is line-oriented JSON-RPC, so the same requests can be supplied via a
 * file descriptor and responses captured to a file. This is a fallback
 * transport, not a replacement for the interactive phase-gated verifier.
 */
import { spawnSync } from "node:child_process";
import { closeSync, openSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { classifyMcpFailure, TOOL_NAME } from "./verify.mjs";

export function verifyMcpFile({ mcpBin, env = process.env, url = "http://127.0.0.1:18080", timeoutMs = 30000 } = {}) {
  const stamp = `${process.pid}-${Date.now()}`;
  const input = path.join(os.tmpdir(), `im2a-mcp-in-${stamp}.jsonl`);
  const output = path.join(os.tmpdir(), `im2a-mcp-out-${stamp}.log`);
  const requests = [
    { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "im2a-dsh-adapter", version: "0.2.0" } } },
    { jsonrpc: "2.0", method: "notifications/initialized", params: {} },
    { jsonrpc: "2.0", id: 2, method: "tools/list", params: {} },
    { jsonrpc: "2.0", id: 3, method: "tools/call", params: { name: TOOL_NAME, arguments: {} } },
  ];
  writeFileSync(input, requests.map((request) => JSON.stringify(request)).join("\n") + "\n", "utf8");
  const inFd = openSync(input, "r");
  const outFd = openSync(output, "w");
  let processResult;
  try {
    processResult = spawnSync(mcpBin, [], {
      env: { ...env, IM2A_HARNESS_URL: url },
      stdio: [inFd, outFd, outFd],
      windowsHide: true,
      timeout: timeoutMs,
    });
  } finally {
    try { closeSync(inFd); } catch {}
    try { closeSync(outFd); } catch {}
  }

  let text = "";
  try { text = readFileSync(output, "utf8"); } catch {}
  try { unlinkSync(input); } catch {}
  try { unlinkSync(output); } catch {}

  const checks = [];
  const byId = new Map();
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    try {
      const message = JSON.parse(line);
      if (message.id !== undefined) byId.set(message.id, message);
    } catch {}
  }

  const init = byId.get(1);
  checks.push({
    name: "initialize",
    ok: Boolean(init?.result?.serverInfo) && !init?.error,
    detail: init?.error ? JSON.stringify(init.error) : init?.result?.serverInfo?.name ?? "no response",
  });

  const list = byId.get(2);
  const toolNames = list?.result?.tools?.map((tool) => tool.name) ?? [];
  checks.push({
    name: "tools/list",
    ok: !list?.error && toolNames.includes(TOOL_NAME),
    detail: toolNames.includes(TOOL_NAME) ? TOOL_NAME : "missing tool",
  });

  const call = byId.get(3);
  const callText = call?.result?.content?.map((item) => item.text ?? "").join("\n") ?? "";
  const isError = call?.result?.isError === true || Boolean(call?.error);
  let harnessCount = 0;
  if (!isError && callText) {
    try {
      const parsed = JSON.parse(callText);
      harnessCount = Array.isArray(parsed.harnesses) ? parsed.harnesses.length : 0;
    } catch {}
  }
  checks.push({
    name: "tools/call",
    ok: !isError && harnessCount > 0,
    detail: isError ? JSON.stringify(call?.error ?? call?.result?.content?.[0]?.text ?? "").slice(0, 240) : `harnesses=${harnessCount}`,
  });

  const failed = checks.find((check) => !check.ok);
  if (!failed) {
    return { ok: true, status: "passed", code: null, harnessCount, transport: "file-fd", checks };
  }
  const classified = classifyMcpFailure(failed.detail ?? "");
  return { ok: false, status: classified.status, code: classified.code, transport: "file-fd", checks, exit: processResult?.status };
}
