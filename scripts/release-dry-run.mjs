import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const artifacts = join(root, '.tmp', 'release-artifacts');
const failures = [];

function run(label, args) {
  const result = spawnSync(process.execPath, args, { cwd: root, encoding: 'utf8' });
  process.stdout.write(`\n== ${label} ==\n${result.stdout ?? ''}`);
  process.stderr.write(result.stderr ?? '');
  if (result.status !== 0) failures.push(`${label} exited ${result.status}`);
}

function gitClean() {
  const result = spawnSync('git', ['status', '--porcelain'], { cwd: root, encoding: 'utf8' });
  if (result.status !== 0) return failures.push('git status failed');
  if ((result.stdout ?? '').trim() !== '') failures.push('working tree is not clean');
}

gitClean();
run('validate', ['scripts/validate.mjs']);
run('p0 tests', ['tests/p0.test.mjs']);
run('compat', ['scripts/compat-check.mjs']);
run('healthz', ['scripts/healthz.mjs']);

rmSync(artifacts, { recursive: true, force: true });
mkdirSync(artifacts, { recursive: true });
const inputs = [
  ['.tmp/artifacts/twinsearth-im2a-dsh-plugin-0.1.1.tgz', 'twinsearth-im2a-dsh-plugin-0.1.1.tgz'],
  ['E:/GPT/release/w2m-im2a-2.0.3-rc3/browser-extension-0.2.3.zip', 'browser-extension-0.2.3.zip'],
  ['E:/GPT/release/w2m-im2a-2.0.3-rc3/w2m-tofuture-0.1.5.tgz', 'w2m-tofuture-0.1.5.tgz'],
];
const hashes = [];
for (const [source, name] of inputs) {
  try {
    const destination = join(artifacts, name);
    copyFileSync(source, destination);
    const hash = createHash('sha256').update(readFileSync(destination)).digest('hex');
    hashes.push(`${hash}  ${name}`);
  } catch (error) {
    failures.push(`artifact missing: ${source}`);
  }
}
writeFileSync(join(artifacts, 'SHA256SUMS'), hashes.join('\n') + '\n');

const evidence = {
  schemaVersion: '1.0',
  checkedAt: new Date().toISOString(),
  mode: 'dry-run',
  tag: null,
  published: false,
  artifacts: readdirSync(artifacts),
  result: failures.length === 0 ? 'passed' : 'blocked',
  failures,
};
writeFileSync(join(root, '.tmp', 'release-dry-run.json'), JSON.stringify(evidence, null, 2) + '\n');
console.log(JSON.stringify(evidence, null, 2));
if (failures.length > 0) process.exitCode = 1;
