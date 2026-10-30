import { writeFileSync } from 'node:fs';

const URL = 'http://127.0.0.1:18084/mcp';
let id = 0;
async function rpc(method, params = {}) {
  const response = await fetch(URL, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++id, method, params }),
  });
  const body = await response.json();
  if (body.error) throw new Error(JSON.stringify(body.error));
  return body.result;
}
const init = await rpc('initialize', { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'v250-test', version: '1.0.0' } });
const list = await rpc('tools/list');
const names = list.tools.map((tool) => tool.name);
const expected = ['im2a_harness_list','im2a_send','im2a_recv','im2a_presence','im2a_whoami','im2a_history','skills_list','skill_view','skill_manage','curator','memory_search','memory_get','memory_propose'];
const missing = expected.filter((name) => !names.includes(name));
const extra = names.filter((name) => !expected.includes(name));
const skills = await rpc('tools/call', { name: 'skills_list', arguments: { query: 'demo', limit: 10 } });
const view = await rpc('tools/call', { name: 'skill_view', arguments: { id: 'demo-skill', path: 'references/a.md' } });
const proposal = await rpc('tools/call', { name: 'skill_manage', arguments: { action: 'create', id: 'new-skill', content: '# New skill\n\nproposal only' } });
const curator = await rpc('tools/call', { name: 'curator', arguments: { action: 'status' } });
const memory = await rpc('tools/call', { name: 'memory_search', arguments: { query: 'evolution', limit: 5 } });
const memoryItems = JSON.parse(memory.content[0].text).items;
const memoryGet = await rpc('tools/call', { name: 'memory_get', arguments: { id: memoryItems[0].id } });
const memoryProposal = await rpc('tools/call', { name: 'memory_propose', arguments: { title: 'new memory', body: 'proposal only memory' } });
const evidence = {
  ok: missing.length === 0 && extra.length === 0 && !skills.isError && !view.isError && !proposal.isError,
  tools: names,
  count: names.length,
  missing,
  extra,
  skills: JSON.parse(skills.content[0].text),
  skillView: JSON.parse(view.content[0].text),
  proposal: JSON.parse(proposal.content[0].text),
  curator: JSON.parse(curator.content[0].text),
  memorySearch: JSON.parse(memory.content[0].text),
  memoryGet: JSON.parse(memoryGet.content[0].text),
  memoryProposal: JSON.parse(memoryProposal.content[0].text),
  checkedAt: new Date().toISOString(),
};
writeFileSync('E:/GPT/im2a-v2.3.45/tests/HTTP-MCP-2.0-EVIDENCE.json', JSON.stringify(evidence, null, 2) + '\n');
console.log(JSON.stringify({ ok: evidence.ok, count: evidence.count, missing, extra, memoryCount: evidence.memorySearch.count, skill: evidence.skillView.path }, null, 2));
if (!evidence.ok) process.exitCode = 1;
