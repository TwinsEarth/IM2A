import { spawn } from 'node:child_process';
import { writeFileSync } from 'node:fs';

const child = spawn('E:\\GPT\\im2a-v2.3.45\\.tmp\\im2a-mcp-v250.exe', [], {
  stdio: ['pipe', 'pipe', 'pipe'],
  env: {
    ...process.env,
    IM2A_HARNESS_URL: 'http://127.0.0.1:18080',
    IM2A_HARNESS_TOKEN: 'test-token',
    IM2A_HARNESS_ID: 'test-harness',
    IM2A_AGENT_NAME: 'go-test-agent',
    IM2A_MCP_HTTP_URL: 'http://127.0.0.1:18084/mcp',
  },
});

let buffer = '';
let nextId = 1;
const pending = new Map();
child.stdout.on('data', (chunk) => {
  buffer += chunk.toString();
  let index;
  while ((index = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, index).trim();
    buffer = buffer.slice(index + 1);
    if (!line) continue;
    const message = JSON.parse(line);
    if (message.id && pending.has(message.id)) {
      pending.get(message.id)(message);
      pending.delete(message.id);
    }
  }
});
child.stderr.on('data', () => {});

const rpc = (method, params = {}) => new Promise((resolve, reject) => {
  const id = nextId++;
  const timer = setTimeout(() => reject(new Error(`${method} timeout`)), 15000);
  pending.set(id, (message) => { clearTimeout(timer); resolve(message); });
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
});

try {
  const init = await rpc('initialize', { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'go-mcp-test', version: '1.0.0' } });
  const list = await rpc('tools/list');
  const names = list.result.tools.map((tool) => tool.name);
  const skills = await rpc('tools/call', { name: 'skills_list', arguments: { query: 'demo' } });
  const view = await rpc('tools/call', { name: 'skill_view', arguments: { id: 'demo-skill', path: 'references/a.md' } });
  const memory = await rpc('tools/call', { name: 'memory_search', arguments: { query: 'evolution' } });
  const evidence = {
    ok: names.length === 13 && !skills.result.isError && !view.result.isError && !memory.result.isError,
    serverInfo: init.result.serverInfo,
    toolCount: names.length,
    tools: names,
    skills: JSON.parse(skills.result.content[0].text),
    skillView: JSON.parse(view.result.content[0].text),
    memory: JSON.parse(memory.result.content[0].text),
    checkedAt: new Date().toISOString(),
  };
  writeFileSync('E:/GPT/im2a-v2.3.45/tests/GO-MCP-2.0-EVIDENCE.json', JSON.stringify(evidence, null, 2) + '\n');
  console.log(JSON.stringify({ ok: evidence.ok, count: names.length, memory: evidence.memory.count, view: evidence.skillView.path }, null, 2));
  if (!evidence.ok) process.exitCode = 1;
} finally {
  child.kill();
}
