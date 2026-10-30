import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { PassThrough, Writable } from "node:stream";
import test from "node:test";
import { classifyMcpFailure, verifyMcp } from "./verify.mjs";

function fakeSpawn(onRequest) {
  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  let buffer = "";
  child.stdin = new Writable({
    write(chunk, _encoding, callback) {
      buffer += chunk.toString();
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const line of lines) {
        if (!line.trim()) continue;
        onRequest(JSON.parse(line), child);
      }
      callback();
    },
    final(callback) {
      setImmediate(() => child.emit("close", 0, null));
      callback();
    },
  });
  child.kill = () => child.emit("close", 0, null);
  return child;
}

function reply(child, message) {
  child.stdout.write(JSON.stringify(message) + "\n");
}

test("classifyMcpFailure separates auth, route, gateway and network", () => {
  assert.equal(classifyMcpFailure("HTTP 401: unauthorized").code, "AUTH_INVALID");
  assert.equal(classifyMcpFailure("HTTP 404: not found").code, "ROUTE_NOT_FOUND");
  assert.equal(classifyMcpFailure("HTTP 503: unavailable").code, "GATEWAY_5XX");
  assert.equal(classifyMcpFailure("dial tcp 127.0.0.1:18080: connectex refused").status, "blocked");
  assert.equal(classifyMcpFailure("request timeout").code, "TIMEOUT");
});

test("verifyMcp passes only when id=3 is a real successful harness list", async () => {
  const spawnImpl = () => fakeSpawn((message, child) => {
    if (message.id === 1) {
      reply(child, { jsonrpc: "2.0", id: 1, result: { serverInfo: { name: "im2a-harness-bridge", version: "0.1.0" }, protocolVersion: "2024-11-05" } });
    }
    if (message.id === 2) {
      reply(child, { jsonrpc: "2.0", id: 2, result: { tools: [{ name: "im2a_harness_list" }] } });
    }
    if (message.id === 3) {
      reply(child, { jsonrpc: "2.0", id: 3, result: { content: [{ type: "text", text: JSON.stringify({ harnesses: [{ id: "deepseek-harness" }] }) }] } });
    }
  });
  const result = await verifyMcp({ mcpBin: "fake", env: {}, spawnImpl });
  assert.equal(result.ok, true);
  assert.equal(result.status, "passed");
  assert.equal(result.harnessCount, 1);
  assert.equal(result.checks.every((check) => check.ok), true);
});

test("verifyMcp fails when tools/call returns isError, even if tools/list contains the name", async () => {
  const spawnImpl = () => fakeSpawn((message, child) => {
    if (message.id === 1) {
      reply(child, { jsonrpc: "2.0", id: 1, result: { serverInfo: { name: "im2a-harness-bridge" } } });
    }
    if (message.id === 2) {
      reply(child, { jsonrpc: "2.0", id: 2, result: { tools: [{ name: "im2a_harness_list" }] } });
    }
    if (message.id === 3) {
      reply(child, { jsonrpc: "2.0", id: 3, result: { isError: true, content: [{ type: "text", text: "gateway HTTP 401: unauthorized" }] } });
    }
  });
  const result = await verifyMcp({ mcpBin: "fake", env: {}, spawnImpl });
  assert.equal(result.ok, false);
  assert.equal(result.status, "failed");
  assert.equal(result.code, "AUTH_INVALID");
});

test("verifyMcp reports gateway-unreachable as blocked, not passed", async () => {
  const spawnImpl = () => fakeSpawn((message, child) => {
    if (message.id === 1) {
      reply(child, { jsonrpc: "2.0", id: 1, result: { serverInfo: { name: "im2a-harness-bridge" } } });
    }
    if (message.id === 2) {
      reply(child, { jsonrpc: "2.0", id: 2, result: { tools: [{ name: "im2a_harness_list" }] } });
    }
    if (message.id === 3) {
      reply(child, { jsonrpc: "2.0", id: 3, result: { isError: true, content: [{ type: "text", text: "dial tcp 127.0.0.1:18080: connectex: refused" }] } });
    }
  });
  const result = await verifyMcp({ mcpBin: "fake", env: {}, spawnImpl });
  assert.equal(result.ok, false);
  assert.equal(result.status, "blocked");
  assert.equal(result.code, "GATEWAY_UNREACHABLE");
});
