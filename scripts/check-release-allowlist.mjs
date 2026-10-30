import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const fail = [];
const allow = JSON.parse(readFileSync(join(root, 'release-allowlist.json'), 'utf8'));
const packageJson = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const matrix = JSON.parse(readFileSync(join(root, 'version-matrix.json'), 'utf8'));
const manifest = JSON.parse(readFileSync(join(root, 'release-manifest.json'), 'utf8'));

if (allow.version !== '2.4.3') fail.push('allow-list version must be 2.4.3');
if (packageJson.version !== '2.4.3') fail.push('package.json version must be 2.4.3');
if (matrix.platform?.version !== '2.4.3') fail.push('version-matrix platform must be 2.4.3');
if (matrix.platform?.tag !== 'v2.4.3') fail.push('version-matrix tag must be v2.4.3');
if (manifest.platformVersion !== '2.4.3') fail.push('release-manifest platformVersion must be 2.4.3');
if (manifest.releaseTag !== 'v2.4.3') fail.push('release-manifest tag must be v2.4.3');

const tracked = execFileSync('git', ['ls-files'], { cwd: root, encoding: 'utf8' })
  .split('\n')
  .filter(Boolean);
for (const file of tracked) {
  const allowed = allow.allowedTrackedFiles.includes(file)
    || allow.allowedTrackedPrefixes.some((prefix) => file.startsWith(prefix));
  if (!allowed) fail.push(`tracked file outside allow-list: ${file}`);
  if (allow.forbiddenPatterns.some((pattern) => file.includes(pattern))) {
    fail.push(`forbidden tracked path: ${file}`);
  }
  if (!file.startsWith('scripts/') && !file.startsWith('tests/')) {
    const text = readFileSync(join(root, file), 'utf8');
    if (/npm_[A-Za-z0-9]{20,}|github_pat_|BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|Bearer\s+[A-Za-z0-9_-]{20,}/.test(text)) {
      fail.push(`possible credential in tracked file: ${file}`);
    }
  }
}

const artifactDir = join(root, '.tmp', 'release-artifacts');
if (existsSync(artifactDir)) {
  for (const name of allow.allowedArtifacts) {
    if (!existsSync(join(artifactDir, name))) fail.push(`missing release artifact: ${name}`);
  }
}

const result = {
  schemaVersion: '1.0',
  checkedAt: new Date().toISOString(),
  version: allow.version,
  trackedFiles: tracked.length,
  artifacts: allow.allowedArtifacts,
  result: fail.length === 0 ? 'passed' : 'blocked',
  failures: fail,
};
writeEvidence(result);
console.log(JSON.stringify(result, null, 2));
if (fail.length > 0) process.exitCode = 1;

function writeEvidence(value) {
  mkdirSync(join(root, '.tmp'), { recursive: true });
  writeFileSync(join(root, '.tmp', 'release-allowlist.json'), JSON.stringify(value, null, 2) + '\n');
}
