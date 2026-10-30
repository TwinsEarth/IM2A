import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const readJson = (name) => JSON.parse(readFileSync(join(root, name), 'utf8'));
const failures = [];

const scope = readFileSync(join(root, 'SIX-PARTY-RELEASE-SCOPE.md'), 'utf8');
if (!/Status: \*\*release candidate scope lock/.test(scope)) failures.push('six-party scope is not locked');

const browser = readJson('P0-BROWSER-E2E.json');
if (browser.result !== 'passed' || browser.stream?.ok !== true) failures.push('browser E2E evidence is not PASS');

const live = readJson('P0-LIVE-DSH-TOOL-CALL.json');
if (live.ok !== true || live.isError !== false || live.harnessCount !== 33) failures.push('DSH live tool call evidence is not PASS');
if (live.dshVersion !== '0.2.1-rc.5' || live.pluginVersion !== '0.1.1') failures.push('DSH evidence is not from rc.5 + plugin 0.1.1');

const rollback = readJson('P0-LIVE-ROLLBACK-DRILL.json');
if (rollback.result !== 'passed' || rollback.afterRemoveRefs !== 0 || rollback.afterReinstallRefs !== 4) failures.push('DSH live rollback drill is not PASS');

const status = readJson('P0-STATUS.json');
if (status.status !== 'verified') failures.push('DSH status is not verified');
const health = String(status.checks?.liveHttpHealthz ?? '');
if (health.startsWith('blocked')) failures.push(`DSH HTTP healthz is blocked: ${health}`);

mkdirSync(join(root, '.tmp'), { recursive: true });
const evidence = {
  schemaVersion: '1.0',
  checkedAt: new Date().toISOString(),
  scope: 'six-party',
  browser: browser.result,
  dshLiveToolCall: live.result,
  dshRollback: rollback.result,
  dshHealthz: health || 'missing',
  result: failures.length === 0 ? 'passed' : 'blocked',
  failures,
};
writeFileSync(join(root, '.tmp', 'compat-evidence.json'), JSON.stringify(evidence, null, 2) + '\n');
console.log(JSON.stringify(evidence, null, 2));
if (failures.length > 0) process.exitCode = 1;
