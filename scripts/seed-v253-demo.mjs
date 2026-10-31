import { mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { SqliteSkillStore } from '../packages/skill-runtime/src/sqlite-store.mjs';
import { MemoryHub } from '../packages/memory-hub/src/index.mjs';

const args = process.argv.slice(2);
const dbIndex = args.indexOf('--db-dir');
const dbDir = dbIndex >= 0 ? args[dbIndex + 1] : join(process.cwd(), '.tmp', 'v253-demo');
if (!dbDir) throw new Error('--db-dir requires a path');

mkdirSync(dbDir, { recursive: true });
for (const name of ['skills.db', 'skills.db-wal', 'skills.db-shm', 'memory.db', 'memory.db-wal', 'memory.db-shm']) {
  rmSync(join(dbDir, name), { force: true });
}

const skills = new SqliteSkillStore({ path: join(dbDir, 'skills.db') });
skills.registerPackage({
  id: 'release-gate',
  version: '1.0.0',
  description: 'Release gate rules for the IM2A demo mystery',
  tags: ['demo', 'release', 'gate'],
  files: {
    'SKILL.md': `# Release Gate

1. tag must equal commit.
2. assets must be exactly 6.
3. healthz 401 is excluded from release judgment.
4. Output only a decision card: verdict, version, three evidence lines, skill, memory.
5. Never read the repository or network for this demo.
`,
    'references/healthz.md': 'healthz has been explicitly excluded since v2.5.0. A 401/404 is not a release failure.\n',
    'references/tag-commit.md': 'A tag that points at a different commit is a release blocker.\n',
  },
});
skills.close();

const memory = new MemoryHub({ path: join(dbDir, 'memory.db'), tenant: 'demo' });
memory.put({
  kind: 'incident',
  title: 'rc1 missing asset',
  body: 'v2.5.2-rc1 had assets=5. The missing asset blocked the release; assets must be exactly 6.',
  metadata: { candidate: 'A', source: 'demo-pack' },
});
memory.put({
  kind: 'incident',
  title: 'rc2 tag mismatch',
  body: 'v2.5.2-rc2 tag d4e5f6 pointed at commit 999999 and caused a rollback.',
  metadata: { candidate: 'B', source: 'demo-pack' },
});
memory.put({
  kind: 'rule',
  title: 'healthz excluded',
  body: 'healthz 401 is excluded from release judgment since v2.5.0; it is not a blocker.',
  metadata: { source: 'demo-pack' },
});
memory.close();

console.log(JSON.stringify({ ok: true, dbDir, skill: 'release-gate@1.0.0', memories: 3 }, null, 2));
