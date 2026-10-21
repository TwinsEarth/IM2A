#!/usr/bin/env node
/**
 * 模块职责：零依赖、单进程测试运行器（契约来源 contracts/INTERFACES.md §12）
 *
 * 为什么不用 `node --test`：
 *   本机 Windows 沙箱禁止子进程管道捕获（spawn EPERM），而 `node --test`
 *   会为每个测试文件 spawn 一个子进程。故改为在**单进程内顺序 import**，
 *   node:test 会在进程退出时统一汇总，并在任一断言失败时置 exitCode=1。
 *
 * 用法：
 *   node scripts/test.mjs                      # 跑全部 *.test.mjs
 *   node scripts/test.mjs --filter=payment     # 只跑路径含 payment 的
 *   node scripts/test.mjs --list               # 只列出将运行的文件
 */
import { readdir } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const ROOT = path.resolve(import.meta.dirname, '..');
const SKIP_DIRS = new Set(['node_modules', '.git', 'public', 'contracts']);
const argv = process.argv.slice(2);
const filters = argv
  .filter((a) => a.startsWith('--filter='))
  .map((a) => a.slice('--filter='.length).toLowerCase())
  .filter(Boolean);
const listOnly = argv.includes('--list');

async function walk(dir, out) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!SKIP_DIRS.has(entry.name) && !entry.name.startsWith('.')) await walk(full, out);
    } else if (entry.name.endsWith('.test.mjs')) {
      out.push(full);
    }
  }
  return out;
}

const all = (await walk(ROOT, [])).sort();
const selected = filters.length
  ? all.filter((file) => filters.some((f) => file.toLowerCase().includes(f)))
  : all;

if (selected.length === 0) {
  console.error(
    `[test] 没有匹配的测试文件 (filter=${filters.join(',') || 'none'})；仓库内共 ${all.length} 个 *.test.mjs`
  );
  process.exit(1);
}

if (listOnly) {
  for (const file of selected) console.log(path.relative(ROOT, file).replaceAll('\\', '/'));
  process.exit(0);
}

console.log(`[test] 运行 ${selected.length} 个测试文件 · 单进程无 spawn · 根目录 ${ROOT}`);
for (const file of selected) {
  const rel = path.relative(ROOT, file).replaceAll('\\', '/');
  console.log(`\n── ${rel} ──`);
  try {
    await import(pathToFileURL(file).href);
  } catch (error) {
    console.error(`[test] 导入失败 ${rel}:`, error);
    process.exitCode = 1;
  }
}
console.log('\n[test] 全部文件已导入，等待 node:test 汇总…');
