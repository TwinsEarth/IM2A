#!/usr/bin/env node
/**
 * 模块职责：v0.1.1 数据 schema 迁移工具 —— 旧数据（缺 schemaVersion）安全升到当前 schema
 * 契约：contracts/INTERFACES.md §51「迁移工具（v0.1.1 补）」
 *
 * 五个安全动作（缺一不可）：
 *   1) 先备份（<file>.pre-<schema>.bak）并返回 sha256
 *   2) dry-run 只报告、不写盘
 *   3) 原子改写（tmp + rename），不留半截文件
 *   4) 改写后**用 createWorld 实际加载一次并核对数据条数**，核对失败 → 回滚备份
 *   5) 幂等：已是当前 schema → CURRENT；未来 schema → UNSUPPORTED（不猜测降级）
 *
 * **服务启动时永不自动迁移**：迁移是显式人工操作（见 docs/31）。
 *
 * 用法：node scripts/migrate-state.mjs [--file <state.json>] [--dry-run]
 */
import { readFileSync, writeFileSync, copyFileSync, renameSync, existsSync, openSync, closeSync } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';

import { SCHEMA_VERSION, createWorld } from '../apps/im2agent/domain.mjs';

export class MigrationError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'MigrationError';
    this.code = code;
  }
}

const sha256 = (buffer) => createHash('sha256').update(buffer).digest('hex');

/** 只统计"可核对"的条数，不解释业务字段（迁移不改业务数据） */
function countsOf(data) {
  return {
    agents: Array.isArray(data.agents) ? data.agents.length : 0,
    tickets: Array.isArray(data.tickets) ? data.tickets.length : 0,
    messages: Array.isArray(data.messages) ? data.messages.length : 0,
    ledger: Array.isArray(data.ledger) ? data.ledger.length : 0,
    audit: Array.isArray(data.audit) ? data.audit.length : 0
  };
}

export function migrateStateFile({ file, dryRun = false } = {}) {
  if (typeof file !== 'string' || file === '') throw new MigrationError('--file 必填', 'INVALID_ARGUMENT');
  if (!existsSync(file)) throw new MigrationError(`状态文件不存在：${file}`, 'NOT_FOUND');

  const raw = readFileSync(file, 'utf8');
  let data;
  try {
    data = JSON.parse(raw);
  } catch {
    throw new MigrationError(`状态文件不是合法 JSON：${file}`, 'INVALID_JSON');
  }
  if (data === null || typeof data !== 'object' || Array.isArray(data)) {
    throw new MigrationError('状态文件顶层必须是对象', 'INVALID_JSON');
  }

  const current = data.schemaVersion;
  if (current === SCHEMA_VERSION) {
    return { status: 'CURRENT', file, schemaVersion: current, counts: countsOf(data) };
  }
  if (current !== undefined) {
    // 0 或未来版本都不猜测：显式拒绝，由人判断
    throw new MigrationError(`不支持从 schema=${String(current)} 迁移到 ${SCHEMA_VERSION}（仅支持缺失 schemaVersion 的 v0.0.x 数据）`, 'UNSUPPORTED');
  }

  const before = countsOf(data);
  const backup = `${file}.pre-${SCHEMA_VERSION}.bak`;
  if (dryRun) {
    return { status: 'DRY_RUN', file, schemaVersion: SCHEMA_VERSION, counts: before, backup, wouldWrite: true };
  }

  // 1) 备份（不覆盖已有备份：同名存在则加时间戳后缀）
  let backupFile = backup;
  if (existsSync(backupFile)) backupFile = `${backup}.${Date.now()}`;
  copyFileSync(file, backupFile);
  const backupSha256 = sha256(readFileSync(backupFile));

  // 2) 原子改写：schemaVersion 放最前，业务字段原样保留
  const next = { schemaVersion: SCHEMA_VERSION, ...data };
  const tmp = `${file}.tmp-${process.pid}`;
  writeFileSync(tmp, JSON.stringify(next, null, 2), 'utf8');
  renameSync(tmp, file);

  // 3) 加载核对：用真实领域构造器验证"新代码读得懂"
  try {
    createWorld({ restored: next });
  } catch (error) {
    copyFileSync(backupFile, file); // 回滚，绝不留下半迁移文件
    throw new MigrationError(`迁移后加载失败，已回滚：${String(error?.message ?? error)}`, 'VERIFY_FAILED');
  }

  // 4) 条数核对：迁移不得丢数据
  const after = countsOf(next);
  for (const key of Object.keys(before)) {
    if (before[key] !== after[key]) {
      copyFileSync(backupFile, file);
      throw new MigrationError(`迁移后 ${key} 条数不一致（${before[key]} → ${after[key]}），已回滚`, 'VERIFY_FAILED');
    }
  }

  return { status: 'MIGRATED', file, schemaVersion: SCHEMA_VERSION, backup: backupFile, backupSha256, counts: after };
}

// ── CLI ──────────────────────────────────────────────────────────────────────
const isMain = process.argv[1] && path.resolve(process.argv[1]) === path.resolve(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
if (isMain) {
  const argv = process.argv.slice(2);
  const arg = (name, fallback = null) => {
    const index = argv.indexOf(`--${name}`);
    return index >= 0 && argv[index + 1] !== undefined ? argv[index + 1] : fallback;
  };
  const file = arg('file', path.join('apps', 'im2agent', '.data', 'im2agent.json'));
  const dryRun = argv.includes('--dry-run');
  try {
    const result = migrateStateFile({ file, dryRun });
    console.log(JSON.stringify(result, null, 2));
    if (result.status === 'DRY_RUN') console.log('\n（dry-run：未写盘。去掉 --dry-run 才会真正迁移）');
    console.log(`\n结论：${result.status}`);
  } catch (error) {
    console.error(`迁移失败：[${error?.code ?? 'ERROR'}] ${String(error?.message ?? error)}`);
    process.exitCode = 1;
  }
}
