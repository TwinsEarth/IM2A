/**
 * 模块职责：用 GitHub Git Data API 把一个本地仓库快照发布为一次 commit（不依赖 git push / github.com 可达性）
 * 用法：GH_TOKEN=<pat> node scripts/publish-github.mjs [--dry-run]
 *
 * 设计：
 *   · 逐文件算 blob（base64），再建 tree、commit、更新 ref；幂等思路：重复运行 = 新 commit（内容相同也会产生新 sha）
 *   · 排除 .git / node_modules / .data / dist / .env* / *.pem / *.key / *.log
 *   · 只打印文件数与 commit sha，不打印 token
 *   · 需要 token 具备目标仓库 Contents: Read and write
 */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const OWNER = process.env.GH_OWNER ?? 'TwinsEarth';
const REPO = process.env.GH_REPO ?? 'IM2A';
const BRANCH = process.env.GH_BRANCH ?? 'main';
const TOKEN = process.env.GH_TOKEN;
const DRY_RUN = process.argv.includes('--dry-run');
const ROOT = process.cwd();

const API = 'https://api.github.com';
const H = {
  Authorization: `Bearer ${TOKEN ?? ''}`,
  'User-Agent': 'im2a-publisher',
  Accept: 'application/vnd.github+json',
  'Content-Type': 'application/json'
};

const EXCLUDE_DIRS = new Set(['.git', 'node_modules', '.data', 'dist', 'build', 'coverage', '.tmp', 'tmp']);
const EXCLUDE_FILE = /(^|\/)(\.env(\..*)?|.*\.pem|.*\.key|.*\.log|.*\.pid)$/;

function walk(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (EXCLUDE_DIRS.has(entry.name)) continue;
    const full = join(dir, entry.name);
    const rel = relative(ROOT, full).replaceAll('\\', '/');
    if (entry.isDirectory()) walk(full, out);
    else if (entry.isFile() && !EXCLUDE_FILE.test(rel)) out.push({ rel, full });
  }
  return out;
}

async function api(method, pathname, body = undefined) {
  const response = await fetch(API + pathname, {
    method,
    headers: H,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(60000)
  });
  const text = await response.text();
  let json = null;
  try { json = text === '' ? null : JSON.parse(text); } catch { json = { raw: text }; }
  return { status: response.status, body: json, text };
}

if (TOKEN === undefined || TOKEN === '') {
  console.error('  GH_TOKEN 未设置');
  process.exit(1);
}

const files = walk(ROOT);
console.log(`  files: ${files.length}`);

if (DRY_RUN) {
  for (const file of files) console.log('   ', file.rel);
  process.exit(0);
}

const repo = await api('GET', `/repos/${OWNER}/${REPO}`);
if (repo.status !== 200) {
  console.error(`  repo read failed: HTTP ${repo.status} ${JSON.stringify(repo.body).slice(0, 300)}`);
  process.exit(1);
}
console.log(`  repo: ${repo.body.full_name} public=${repo.body.private === false}`);
const perms = repo.body.permissions ?? {};
console.log(`  token permissions: admin=${perms.admin} push=${perms.push} maintain=${perms.maintain} triage=${perms.triage} pull=${perms.pull}`);
if (perms.push !== true) {
  console.error('  token 对该仓库没有 push(Contents: write) 权限 —— 请在 PAT 的 Repository access 中加入 IM2A，并勾选 Contents: Read and write');
  process.exit(2);
}

const ref = await api('GET', `/repos/${OWNER}/${REPO}/git/ref/heads/${BRANCH}`);
let parents = [];
let baseTree = null;
if (ref.status === 200) {
  parents = [ref.body.object.sha];
  const commit = await api('GET', `/repos/${OWNER}/${REPO}/git/commits/${ref.body.object.sha}`);
  baseTree = commit.body?.tree?.sha ?? null;
  console.log(`  base commit: ${ref.body.object.sha.slice(0, 8)}`);
} else {
  console.log(`  no ${BRANCH} ref yet (HTTP ${ref.status}); will create from empty tree`);
}

const treeEntries = [];
const skipped = [];
for (const file of files) {
  // fine-grained PAT 若未授予 Workflows: write，GitHub 会拒绝创建 .github/workflows 文件（403）
  if (!process.env.GH_INCLUDE_WORKFLOWS === true && file.rel.startsWith('.github/workflows/')) { skipped.push(file.rel); continue; }
  const content = readFileSync(file.full);
  const blob = await api('POST', `/repos/${OWNER}/${REPO}/git/blobs`, { content: content.toString('base64'), encoding: 'base64' });
  if (blob.status !== 201) {
    console.error(`  blob failed for ${file.rel}: HTTP ${blob.status} ${JSON.stringify(blob.body).slice(0, 200)}`);
    process.exit(1);
  }
  treeEntries.push({ path: file.rel, mode: '100644', type: 'blob', sha: blob.body.sha });
}
console.log(`  blobs created: ${treeEntries.length}`);

const tree = await api('POST', `/repos/${OWNER}/${REPO}/git/trees`, baseTree === null ? { tree: treeEntries } : { base_tree: baseTree, tree: treeEntries });
if (tree.status !== 201) {
  console.error(`  tree failed: HTTP ${tree.status} ${JSON.stringify(tree.body).slice(0, 300)}`);
  process.exit(1);
}

console.log(`  skipped (need Workflows: write): ${skipped.length === 0 ? 'none' : skipped.join(', ')}`);

const pkgVersion = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8')).version;
const message = `feat: IM2A v${pkgVersion} — TaskLedger 任务账本（只追加 + 状态机 + 幂等键 + 租约）

v0.2.0 交付：apps/im2a/task-ledger.mjs + 13 项契约测试（create/lease/heartbeat/complete/fail/cancel/
sweepExpired/fromEvents）；旧 attempt 落账被 TOKEN_STALE 拒绝；重复 create/complete 幂等不追加事件。
文档同步：ARCHITECTURE/PLAN/DELIVERY/README。测试：node scripts/test.mjs → 211 passed / 0 failed。
与 w2m/DSH 完全独立：不引用其代码/服务/依赖，仅借鉴任务账本设计思想。`;
const commit = await api('POST', `/repos/${OWNER}/${REPO}/git/commits`, { message, tree: tree.body.sha, parents });
if (commit.status !== 201) {
  console.error(`  commit failed: HTTP ${commit.status} ${JSON.stringify(commit.body).slice(0, 300)}`);
  process.exit(1);
}
const sha = commit.body.sha;
console.log(`  commit: ${sha}`);

if (parents.length === 0) {
  const created = await api('POST', `/repos/${OWNER}/${REPO}/git/refs`, { ref: `refs/heads/${BRANCH}`, sha });
  console.log(`  ref create: HTTP ${created.status}`);
} else {
  const updated = await api('PATCH', `/repos/${OWNER}/${REPO}/git/refs/heads/${BRANCH}`, { sha, force: true });
  console.log(`  ref update: HTTP ${updated.status}`);
}

const verify = await api('GET', `/repos/${OWNER}/${REPO}/git/trees/${BRANCH}?recursive=1`);
const count = (verify.body?.tree ?? []).filter((entry) => entry.type === 'blob').length;
console.log(`  published files: ${count}`);
console.log(`  url: https://github.com/${OWNER}/${REPO}`);
