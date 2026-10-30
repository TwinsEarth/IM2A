import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { validateSchema } from './schema-validator.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const read = async (rel) => JSON.parse(await readFile(path.join(root, rel), 'utf8'));
const sha = (text) => createHash('sha256').update(text).digest('hex');

const schema = await read('docs/contracts/agent-plugin-2.0.schema.json');
const oneSchema = await read('packages/protocol/schemas/agent-plugin.schema.json');
const plugin = await read('packages/agent-plugins/source/plugin-2.0.json');
const failures = [];

const checked = validateSchema(schema, plugin);
if (!checked.ok) failures.push(...checked.errors.map((error) => `v2 schema: ${error}`));
if (plugin.profileVersion !== '2.0') failures.push('profileVersion must be 2.0');

for (const skill of plugin.skills ?? []) {
  const body = await readFile(path.join(root, skill.ref), 'utf8');
  if (sha(body) !== skill.sha256) failures.push(`skill hash mismatch: ${skill.id}`);
  if (!/^---\n/.test(body)) failures.push(`skill missing frontmatter: ${skill.id}`);
}
if (plugin.toolCatalogRef) {
  try { await read(plugin.toolCatalogRef); } catch { failures.push('toolCatalogRef does not exist'); }
}

const projection = {
  name: plugin.name,
  version: plugin.version,
  description: plugin.description,
  license: plugin.license,
  skills: plugin.skills.map((skill) => skill.ref),
  mcp: plugin.mcp,
  targets: plugin.targets,
};
const projected = validateSchema(oneSchema, projection);
if (!projected.ok) failures.push(...projected.errors.map((error) => `1.0 projection: ${error}`));

const result = {
  ok: failures.length === 0,
  profileVersion: plugin.profileVersion,
  name: plugin.name,
  version: plugin.version,
  skills: plugin.skills.length,
  targets: plugin.targets.length,
  projection: projected.ok,
  failures,
};
console.log(JSON.stringify(result, null, 2));
if (failures.length) process.exitCode = 1;
