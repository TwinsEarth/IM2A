// 测试文件：§44 数据备份与恢复演练（sha256 校验 / 篡改检测 / 不自动应用 / 轮转 / 坏文件可见）/ 运行：node services/persistence/test/backup.test.mjs
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { BACKUP_CODE, BackupError, BackupService } from '../src/backup.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';
const tempDirs = [];

function makeTempDir() {
  const dir = mkdtempSync(join(tmpdir(), 'im2agent-backup-'));
  tempDirs.push(dir);
  return dir;
}

after(() => {
  for (const dir of tempDirs) rmSync(dir, { recursive: true, force: true });
});

function makeClock(startIso = BASE_ISO) {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

function makeStore(snapshot) {
  const calls = { read: 0, write: 0 };
  return {
    calls,
    store: {
      read: () => {
        calls.read += 1;
        return typeof snapshot === 'function' ? snapshot() : snapshot;
      },
      write: () => {
        calls.write += 1;
      },
    },
  };
}

const SNAPSHOT = Object.freeze({
  users: [{ id: 'user-a', name: '阿林' }],
  messages: [{ id: 'msg-1', text: '你好' }],
});

/** 与实现同口径的手动重算：sha256(canonical({ data, createdAt, label })) */
function manualSha256(parsed) {
  return createHash('sha256')
    .update(JSON.stringify({ data: parsed.data, createdAt: parsed.meta.createdAt, label: parsed.meta.label }), 'utf8')
    .digest('hex');
}

function newService({ dir = makeTempDir(), snapshot = SNAPSHOT, keep = 5, clock = makeClock() } = {}) {
  const { store, calls } = makeStore(snapshot);
  return { service: new BackupService({ store, dir, keep, clock }), dir, calls, clock };
}

test('create → 落盘文件带 meta+data、sha256 可手动重算、bytes 正确；list 可见', () => {
  const { service, dir } = newService();
  const created = service.create({ label: 'nightly' });
  assert.equal(basename(created.file), `im2agent-${BASE_ISO.replace(/:/g, '-')}.bak.json`);
  assert.ok(existsSync(created.file));
  assert.equal(created.createdAt, BASE_ISO);
  assert.equal(created.label, 'nightly');
  assert.match(created.sha256, /^[0-9a-f]{64}$/);

  const raw = readFileSync(created.file, 'utf8');
  const parsed = JSON.parse(raw);
  assert.deepEqual(parsed.data, SNAPSHOT);
  assert.equal(parsed.meta.createdAt, BASE_ISO);
  assert.equal(parsed.meta.label, 'nightly');
  assert.equal(parsed.meta.sha256, created.sha256, '文件内 meta.sha256 必须等于返回值');
  assert.equal(created.sha256, manualSha256(parsed), 'sha256 必须与文件内容同口径重算一致');
  assert.equal(created.bytes, Buffer.byteLength(raw, 'utf8'));

  const listed = service.list();
  assert.equal(listed.length, 1);
  assert.equal(listed[0].file, created.file);
  assert.equal(listed[0].sha256, created.sha256);
  assert.equal(listed[0].bytes, created.bytes);
  assert.equal(listed[0].createdAt, BASE_ISO);
  assert.equal(listed[0].label, 'nightly');
  assert.equal(listed[0].skipped, false);
  assert.deepEqual({ ...service.verify({ file: created.file }) }, { ok: true, code: null });
  // 也接受裸文件名
  assert.deepEqual({ ...service.verify({ file: basename(created.file) }) }, { ok: true, code: null });
  assert.ok(existsSync(join(dir, basename(created.file))));
});

test('restore 返回数据与快照 deepEqual，且不调用 store.write（不自动应用）', () => {
  const { service, calls } = newService();
  const created = service.create({ label: 'restore-drill' });
  const before = { ...calls };
  const restored = service.restore({ file: created.file });
  assert.deepEqual(restored.data, SNAPSHOT);
  assert.equal(restored.meta.sha256, created.sha256);
  assert.equal(restored.meta.label, 'restore-drill');
  assert.equal(restored.meta.createdAt, BASE_ISO);
  assert.ok(Object.isFrozen(restored));
  assert.ok(Object.isFrozen(restored.meta));
  assert.equal(calls.write, 0, '恢复演练绝不写回 store');
  assert.equal(calls.read, before.read, '恢复只读文件、不再读 store');
});

test('篡改文件内容/label → verify HASH_MISMATCH，restore 抛 HASH_MISMATCH（拒绝恢复）', () => {
  const { service } = newService();
  const created = service.create();

  // 篡改 data
  const tamperedData = JSON.parse(readFileSync(created.file, 'utf8'));
  tamperedData.data.messages.push({ id: 'msg-evil', text: '被注入' });
  writeFileSync(created.file, JSON.stringify(tamperedData), 'utf8');
  assert.deepEqual({ ...service.verify({ file: created.file }) }, { ok: false, code: BACKUP_CODE.HASH_MISMATCH });
  assert.throws(
    () => service.restore({ file: created.file }),
    (error) => error instanceof BackupError && error.code === BACKUP_CODE.HASH_MISMATCH
  );

  // 篡改 label（hash 覆盖 meta.createdAt/label，防止伪造元数据）
  const other = service.create({ label: 'original' });
  const tamperedMeta = JSON.parse(readFileSync(other.file, 'utf8'));
  tamperedMeta.meta.label = 'forged';
  writeFileSync(other.file, JSON.stringify(tamperedMeta), 'utf8');
  assert.equal(service.verify({ file: other.file }).code, BACKUP_CODE.HASH_MISMATCH);

  // 篡改 createdAt
  const third = service.create({ label: 'third' });
  const tamperedTime = JSON.parse(readFileSync(third.file, 'utf8'));
  tamperedTime.meta.createdAt = '2030-01-01T00:00:00.000Z';
  writeFileSync(third.file, JSON.stringify(tamperedTime), 'utf8');
  assert.equal(service.verify({ file: third.file }).code, BACKUP_CODE.HASH_MISMATCH);
});

test('备份不存在 / 文件名被删除或改走 → NOT_FOUND（verify 不抛、restore 抛）', () => {
  const { service, dir } = newService();
  const created = service.create();
  assert.deepEqual({ ...service.verify({ file: created.file }) }, { ok: true, code: null });

  unlinkSync(created.file);
  assert.deepEqual({ ...service.verify({ file: created.file }) }, { ok: false, code: BACKUP_CODE.NOT_FOUND });
  assert.throws(
    () => service.restore({ file: created.file }),
    (error) => error instanceof BackupError && error.code === BACKUP_CODE.NOT_FOUND
  );

  // 改名后旧路径同样 NOT_FOUND
  const another = service.create();
  renameSync(another.file, join(dir, 'renamed.bak.json'));
  assert.equal(service.verify({ file: another.file }).code, BACKUP_CODE.NOT_FOUND);

  // 目录外路径不允许被用来读任意文件
  assert.equal(service.verify({ file: join(tmpdir(), 'outside.bak.json') }).code, BACKUP_CODE.NOT_FOUND);
  assert.equal(service.verify({ file: 123 }).code, BACKUP_CODE.INVALID);
  assert.throws(() => service.restore({ file: 123 }), (error) => error.code === BACKUP_CODE.INVALID);
});

test('rotate：keep=2 时造 4 份 → 剩最新 2 份（最新一定在），removed 列表正确', () => {
  const { service, clock } = newService({ keep: 2 });
  const created = [];
  for (let index = 0; index < 4; index += 1) {
    created.push(service.create({ label: `backup-${index}` }));
    clock.advance(1000);
  }
  const { removed } = service.rotate();
  assert.equal(removed.length, 2, '4 份保留 2 份应删除 2 份');
  assert.deepEqual([...removed].sort(), [created[0].file, created[1].file].sort(), 'removed 应是最旧两份');
  const remaining = service.list().filter((entry) => entry.skipped === false);
  assert.equal(remaining.length, 2);
  assert.deepEqual(remaining.map((entry) => entry.label).sort(), ['backup-2', 'backup-3']);
  assert.ok(!existsSync(created[0].file));
  assert.ok(!existsSync(created[1].file));
  assert.ok(existsSync(created[3].file), '最新一份绝不删除');
  assert.ok(existsSync(created[2].file));

  // 未超限时 rotate 幂等：不误删
  assert.deepEqual({ ...service.rotate() }, { removed: [] });
  assert.equal(service.list().filter((entry) => entry.skipped === false).length, 2);
});

test('轮转不删最新：keep=1 且只有 1 份时 rotate 不删除', () => {
  const { service } = newService({ keep: 1 });
  const only = service.create({ label: 'only' });
  assert.deepEqual({ ...service.rotate() }, { removed: [] });
  assert.ok(existsSync(only.file));
});

test('坏备份可见：非法 JSON / meta 缺 sha256 → list 标注 skipped、verify INVALID', () => {
  const { service, dir } = newService();
  const good = service.create({ label: 'good' });
  const badJson = join(dir, 'im2agent-bad.bak.json');
  writeFileSync(badJson, '这不是 JSON', 'utf8');
  const missingSha = join(dir, 'im2agent-missing-sha.bak.json');
  writeFileSync(missingSha, JSON.stringify({ meta: { createdAt: BASE_ISO }, data: { a: 1 } }), 'utf8');
  writeFileSync(join(dir, 'unrelated.txt'), 'ignored', 'utf8');

  const listed = service.list();
  assert.equal(listed.length, 3, '1 份合法 + 2 份坏文件（无关文件不计）');
  const skipped = listed.filter((entry) => entry.skipped === true);
  assert.equal(skipped.length, 2);
  assert.deepEqual(skipped.map((entry) => entry.reason).sort(), [BACKUP_CODE.INVALID, BACKUP_CODE.INVALID]);
  assert.ok(skipped.some((entry) => entry.file === badJson));
  assert.ok(skipped.some((entry) => entry.file === missingSha));
  assert.equal(listed[0].file, good.file, '合法备份排在前面');

  assert.deepEqual({ ...service.verify({ file: badJson }) }, { ok: false, code: BACKUP_CODE.INVALID });
  assert.deepEqual({ ...service.verify({ file: missingSha }) }, { ok: false, code: BACKUP_CODE.INVALID });
  assert.throws(
    () => service.restore({ file: badJson }),
    (error) => error instanceof BackupError && error.code === BACKUP_CODE.INVALID
  );
});

test('空目录 list → []；同一时间戳多次备份生成不同文件名（文件名安全化 + 去重）', () => {
  const empty = newService();
  assert.deepEqual([...empty.service.list()], []);

  const { service, dir } = newService(); // clock 不推进 → createdAt 相同
  const first = service.create();
  const second = service.create();
  assert.notEqual(first.file, second.file, '同时间戳也必须产生独立文件');
  assert.equal(basename(first.file), `im2agent-${BASE_ISO.replace(/:/g, '-')}.bak.json`);
  assert.equal(basename(second.file), `im2agent-${BASE_ISO.replace(/:/g, '-')}-2.bak.json`);
  assert.equal(service.list().length, 2);
  assert.ok([first.sha256, second.sha256].every((hash) => /^[0-9a-f]{64}$/.test(hash)));
  // 文件名不含 Windows 非法字符 ':'
  assert.ok(readFileSync(first.file, 'utf8').includes('"sha256"'));
  assert.ok(!basename(first.file).includes(':'));
  assert.ok(existsSync(join(dir, basename(first.file))));
});

test('create 异常路径：store.read 抛错 → IO_FAILED；快照不可序列化 → INVALID；label 非法 → TypeError', () => {
  const dir = makeTempDir();
  const throwing = new BackupService({
    store: {
      read: () => {
        throw new Error('disk gone');
      },
    },
    dir,
  });
  assert.throws(
    () => throwing.create(),
    (error) => error instanceof BackupError && error.code === BACKUP_CODE.IO_FAILED
  );

  const circular = { id: 'cycle' };
  circular.self = circular;
  const circularService = new BackupService({ store: { read: () => circular }, dir });
  assert.throws(
    () => circularService.create(),
    (error) => error instanceof BackupError && error.code === BACKUP_CODE.INVALID
  );
  assert.equal(circularService.list().length, 0, '失败的 create 不得留下半截备份');

  const { service } = newService();
  assert.throws(() => service.create({ label: 123 }), TypeError);
});

test('构造参数校验：store/dir/keep/clock 非法 → TypeError/RangeError', () => {
  assert.throws(() => new BackupService({ dir: makeTempDir() }), TypeError);
  assert.throws(() => new BackupService({ store: {}, dir: makeTempDir() }), TypeError);
  assert.throws(() => new BackupService({ store: { read: () => ({}) }, dir: '' }), TypeError);
  assert.throws(() => new BackupService({ store: { read: () => ({}) }, dir: makeTempDir(), keep: 0 }), RangeError);
  assert.throws(() => new BackupService({ store: { read: () => ({}) }, dir: makeTempDir(), keep: 1.5 }), RangeError);
  assert.throws(
    () => new BackupService({ store: { read: () => ({}) }, dir: makeTempDir(), clock: 'now' }),
    TypeError
  );
});

test('默认参数：label 缺省为 null，keep 默认 5（少于 5 份 rotate 不删）', () => {
  const { service } = newService();
  const created = service.create();
  assert.equal(created.label, null);
  const parsed = JSON.parse(readFileSync(created.file, 'utf8'));
  assert.equal(parsed.meta.label, null);
  assert.deepEqual({ ...service.rotate() }, { removed: [] });
  assert.ok(existsSync(created.file));
});
