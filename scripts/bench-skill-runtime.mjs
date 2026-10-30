import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { writeFileSync } from 'node:fs';
import { SqliteSkillStore } from '../packages/skill-runtime/src/sqlite-store.mjs';

const dir = mkdtempSync(join(tmpdir(), 'im2a-perf-'));
const dbPath = join(dir, 'perf.db');
const store = new SqliteSkillStore({ path: dbPath });
const now = () => Number(process.hrtime.bigint() / 1000n) / 1000;
const samples = { registerMs: [], listMs: [], viewMs: [], promoteMs: [] };
let baseSha = null;

for (let i = 0; i < 1000; i += 1) {
  const start = now();
  const { packageSha } = store.registerPackage({
    id: `skill-${String(i).padStart(4, '0')}`,
    version: '1.0.0',
    description: `skill ${i}`,
    files: { 'SKILL.md': `# skill ${i}`, 'references/a.md': 'a' },
  });
  if (i === 0) baseSha = packageSha;
  samples.registerMs.push(now() - start);
}

for (let i = 0; i < 1000; i += 1) {
  const start = now(); store.list(); samples.listMs.push(now() - start);
}
for (let i = 0; i < 1000; i += 1) {
  const start = now(); store.view('skill-0000'); samples.viewMs.push(now() - start);
}

for (let i = 0; i < 100; i += 1) {
  const start = now();
  const p = store.manage({ action: 'patch', id: 'skill-0000', baseSha, content: `# v${i + 2}` });
  store.stage(p.proposalId, { evidence: ['perf'], evalCases: Array.from({ length: 20 }, (_, n) => `c${n}`) });
  const promoted = store.promote({ proposalId: p.proposalId, approvalId: `a-${i}`, expectedActiveSha: baseSha, idempotencyKey: `p-${i}` });
  baseSha = promoted.packageSha;
  samples.promoteMs.push(now() - start);
}

function summary(values) {
  const sorted = [...values].sort((a, b) => a - b);
  const at = (p) => sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * p))];
  return { n: values.length, avgMs: Number((values.reduce((a, b) => a + b, 0) / values.length).toFixed(3)), p50Ms: Number(at(0.5).toFixed(3)), p95Ms: Number(at(0.95).toFixed(3)) };
}

const report = {
  schemaVersion: '1.0',
  measuredAt: new Date().toISOString(),
  runtime: 'Node 24.16 / node:sqlite',
  database: 'SQLite real file',
  operations: Object.fromEntries(Object.entries(samples).map(([k, v]) => [k, summary(v)])),
  integrity: {
    activeSkills: store.list().length,
    ledgerRows: store.db.prepare('SELECT COUNT(*) AS n FROM ledger').get().n,
    idempotencyRows: store.db.prepare('SELECT COUNT(*) AS n FROM idempotency').get().n,
  },
};
store.close();
for (const name of ['perf.db', 'perf.db-wal', 'perf.db-shm']) rmSync(join(dir, name), { force: true });
report.integrity.databaseRemoved = !existsSync(dbPath);
report.integrity.directoryRemoved = (rmSync(dir, { recursive: true, force: true }), !existsSync(dir));
writeFileSync('E:/GPT/im2a-v2.3.45/tests/PERF-v2.5.0.json', JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify(report, null, 2));
if (!report.integrity.databaseRemoved || !report.integrity.directoryRemoved) process.exitCode = 1;
