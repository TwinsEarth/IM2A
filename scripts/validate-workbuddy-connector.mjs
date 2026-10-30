import { readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dir = join(root, 'packages', 'im2a-workbuddy-connector');
const errors = [];
const read = (name) => JSON.parse(readFileSync(join(dir, name), 'utf8'));

const meta = read('connector-meta.json');
const mcp = read('mcp.json');
const schema = read('token-schema.json');

if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(meta.source)) errors.push('source must be kebab-case');
if (meta.type !== 'mcp') errors.push('type must be mcp');
if (meta.auth_mode !== 'token') errors.push('auth_mode must be token');
if (!Array.isArray(meta.examples_zh) || meta.examples_zh.length < 2) errors.push('missing zh examples');
if (!Array.isArray(meta.examples_en) || meta.examples_en.length < 2) errors.push('missing en examples');
if (!existsSync(join(dir, 'icon.svg'))) errors.push('icon.svg missing');
if (!existsSync(join(dir, 'skills', 'im2a-connector', 'SKILL.md'))) errors.push('SKILL.md missing');

const servers = Object.keys(mcp.mcpServers ?? {});
if (servers.length !== 1) errors.push('exactly one mcpServers entry required');
const server = mcp.mcpServers?.[servers[0]] ?? {};
if (server.type !== 'stdio') errors.push('server must use stdio for local im2a-mcp');
if (server.command !== '${IM2A_MCP_BIN}') errors.push('command must use ${IM2A_MCP_BIN}');
for (const key of ['IM2A_HARNESS_URL', 'IM2A_HARNESS_TOKEN']) {
  if (!String(server.env?.[key] ?? '').includes(`\${${key}}`)) errors.push(`env ${key} must use placeholder`);
}
const fields = schema.fields ?? [];
const keys = new Set(fields.map((field) => field.key));
for (const key of ['IM2A_MCP_BIN', 'IM2A_HARNESS_URL', 'IM2A_HARNESS_TOKEN']) {
  if (!keys.has(key)) errors.push(`token-schema missing ${key}`);
}
const tokenField = fields.find((field) => field.key === 'IM2A_HARNESS_TOKEN');
if (tokenField?.type !== 'password') errors.push('token field must be password');
if (fields.some((field) => typeof field.required !== 'boolean')) errors.push('fields need required boolean');

const serialized = JSON.stringify({ meta, mcp, schema });
if (/npm_[A-Za-z0-9]{20,}|github_pat_|Bearer\s+[A-Za-z0-9_-]{20,}/i.test(serialized)) errors.push('possible real credential embedded');

const result = {
  ok: errors.length === 0,
  source: meta.source,
  version: meta.version,
  server: servers[0],
  fields: [...keys],
  errors,
};
console.log(JSON.stringify(result, null, 2));
if (errors.length > 0) process.exitCode = 1;
