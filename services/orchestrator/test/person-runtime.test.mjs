// 测试：Person-scoped Runtime（契约 §33）。
// 覆盖：三层分离（WORKING 不迁移）、secret 拒绝、任务状态机白名单、snapshot/restore 的 version+hash 校验、metrics。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  PersonRuntime,
  PersonRuntimeError,
  RUNTIME_CODE,
  RUNTIME_TASK_STATE,
  STATE_LAYER
} from '../src/person-runtime.mjs';
import { canonicalize, sha256Hex } from '../../identity/src/agent-id.mjs';

const USER = 'user-lin';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.currentMs = () => currentMs;
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const makeRuntime = (options = {}) => {
  const clock = options.clock ?? makeClock();
  const runtime = new PersonRuntime({ userId: USER, clock, ...(options.version ? { version: options.version } : {}) });
  return { clock, runtime };
};

test('STATE_LAYER / RUNTIME_TASK_STATE / RUNTIME_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...STATE_LAYER }, { PERSONAL: 'PERSONAL', TASK: 'TASK', WORKING: 'WORKING' });
  assert.deepEqual({ ...RUNTIME_TASK_STATE }, {
    CREATED: 'CREATED',
    RUNNING: 'RUNNING',
    WAITING: 'WAITING',
    COMPLETED: 'COMPLETED',
    FAILED: 'FAILED',
    CANCELED: 'CANCELED'
  });
  assert.deepEqual({ ...RUNTIME_CODE }, {
    NOT_FOUND: 'NOT_FOUND',
    ILLEGAL_STATE: 'ILLEGAL_STATE',
    LAYER_INVALID: 'LAYER_INVALID',
    SECRET_IN_LAYER: 'SECRET_IN_LAYER',
    BUNDLE_INVALID: 'BUNDLE_INVALID',
    VERSION_MISMATCH: 'VERSION_MISMATCH'
  });
  assert.ok(Object.isFrozen(STATE_LAYER));
  assert.ok(Object.isFrozen(RUNTIME_TASK_STATE));
  assert.ok(Object.isFrozen(RUNTIME_CODE));
});

test('PERSONAL 层：putPersonal/personal 冻结、可覆盖、未知 key 返回 null', () => {
  const { clock, runtime } = makeRuntime();
  const entry = runtime.putPersonal({
    key: 'preference.coffee',
    value: { brand: 'Manner', sugar: 'less' },
    evidence: ['chat-1', 'chat-2']
  });
  assert.ok(Object.isFrozen(entry));
  assert.equal(entry.layer, STATE_LAYER.PERSONAL);
  assert.equal(entry.key, 'preference.coffee');
  assert.deepEqual(entry.value, { brand: 'Manner', sugar: 'less' });
  assert.deepEqual([...entry.evidence], ['chat-1', 'chat-2']);
  assert.equal(entry.updatedAt, clock.iso());
  assert.equal(runtime.personal({ key: 'preference.coffee' }), entry);
  assert.equal(runtime.personal({ key: 'missing' }), null);

  clock.advance(1000);
  const updated = runtime.putPersonal({ key: 'preference.coffee', value: { brand: 'Manner' } });
  assert.deepEqual(updated.value, { brand: 'Manner' });
  assert.equal(updated.updatedAt, '2026-10-21T00:00:01.000Z');
});

test('不变量②：任何 secret:* key 写入 => SECRET_IN_LAYER（凭证归 Vault，不进 Agent 上下文）', () => {
  const { runtime } = makeRuntime();
  assert.throws(() => runtime.putPersonal({ key: 'secret:token', value: 'abc' }), {
    code: 'SECRET_IN_LAYER'
  });
  assert.throws(() => runtime.putPersonal({ key: 'profile', value: { 'secret:apiKey': 'sk-1' } }), {
    code: 'SECRET_IN_LAYER'
  });
  const task = runtime.putTask({ taskId: 't1', title: '订机票' });
  assert.equal(task.state, 'CREATED');
  assert.throws(
    () => runtime.putTask({ taskId: 't2', title: 'x', checkpoint: { 'secret:card': '6222' } }),
    { code: 'SECRET_IN_LAYER' }
  );
  assert.throws(() => runtime.setWorking({ turnId: 'turn-1', context: { 'secret:token': 'x' } }), {
    code: 'SECRET_IN_LAYER'
  });
});

test('WORKING 层：setWorking 可读，clearWorking 后不可读（返回 null）', () => {
  const { runtime } = makeRuntime();
  const working = runtime.setWorking({ turnId: 'turn-1', context: { step: 'plan', plan: ['a', 'b'] } });
  assert.ok(Object.isFrozen(working));
  assert.equal(working.layer, STATE_LAYER.WORKING);
  assert.equal(runtime.working().turnId, 'turn-1');
  assert.deepEqual(runtime.working().context, { step: 'plan', plan: ['a', 'b'] });

  assert.equal(runtime.clearWorking(), true);
  assert.equal(runtime.working(), null);
  assert.equal(runtime.clearWorking(), false); // 幂等
});

test('TASK 层：putTask/task/tasks（排序、状态过滤、未知任务 NOT_FOUND、非法过滤 ILLEGAL_STATE）', () => {
  const { clock, runtime } = makeRuntime();
  const first = runtime.putTask({ taskId: 't1', title: '订机票', checkpoint: { step: 1 } });
  assert.equal(first.layer, STATE_LAYER.TASK);
  assert.equal(first.state, 'CREATED');
  assert.deepEqual(first.checkpoint, { step: 1 });
  assert.equal(first.waitingFor, null);
  assert.equal(first.createdAt, '2026-10-21T00:00:00.000Z');
  clock.advance(1000);
  runtime.putTask({ taskId: 't2', title: '约体检', state: 'WAITING' });
  clock.advance(1000);
  runtime.putTask({ taskId: 't3', title: '取消的', state: 'CANCELED' });

  assert.deepEqual(runtime.tasks().map((task) => task.taskId), ['t1', 't2', 't3']);
  assert.deepEqual(runtime.tasks({ state: 'WAITING' }).map((task) => task.taskId), ['t2']);
  assert.ok(Object.isFrozen(runtime.tasks()));
  assert.equal(runtime.task('t1').taskId, 't1');
  assert.throws(() => runtime.task('missing'), { code: 'NOT_FOUND' });
  assert.throws(() => runtime.tasks({ state: 'DONE' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => runtime.putTask({ taskId: 't4', title: 'x', state: 'DONE' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => runtime.putTask({ taskId: 't4' }), TypeError);
});

test('transition 白名单：CREATED→RUNNING→WAITING→RUNNING→COMPLETED/FAILED；任意非终态→CANCELED；终态不可回退', () => {
  const { runtime } = makeRuntime();
  runtime.putTask({ taskId: 't1', title: '正常链' });
  assert.equal(runtime.transition({ taskId: 't1', to: 'RUNNING' }).state, 'RUNNING');
  assert.equal(runtime.transition({ taskId: 't1', to: 'WAITING' }).state, 'WAITING');
  assert.equal(runtime.transition({ taskId: 't1', to: 'RUNNING' }).state, 'RUNNING');
  const completed = runtime.transition({ taskId: 't1', to: 'COMPLETED', reason: 'done' });
  assert.equal(completed.state, 'COMPLETED');
  assert.ok(completed.finishedAt !== null);
  assert.equal(completed.lastReason, 'done');
  assert.throws(() => runtime.transition({ taskId: 't1', to: 'RUNNING' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => runtime.transition({ taskId: 't1', to: 'CANCELED' }), { code: 'ILLEGAL_STATE' });

  runtime.putTask({ taskId: 't2', title: '跳过 RUNNING' });
  assert.throws(() => runtime.transition({ taskId: 't2', to: 'COMPLETED' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => runtime.transition({ taskId: 't2', to: 'WAITING' }), { code: 'ILLEGAL_STATE' });

  runtime.putTask({ taskId: 't3', title: '从 CREATED 取消' });
  assert.equal(runtime.transition({ taskId: 't3', to: 'CANCELED' }).state, 'CANCELED');

  runtime.putTask({ taskId: 't4', title: '失败链' });
  runtime.transition({ taskId: 't4', to: 'RUNNING' });
  assert.equal(runtime.transition({ taskId: 't4', to: 'FAILED' }).state, 'FAILED');
  assert.throws(() => runtime.transition({ taskId: 't4', to: 'CANCELED' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => runtime.transition({ taskId: 't9', to: 'RUNNING' }), { code: 'NOT_FOUND' });
  assert.throws(() => runtime.transition({ taskId: 't2', to: 'DONE' }), { code: 'ILLEGAL_STATE' });
});

test('wait/resume：RUNNING→WAITING 记录等待条件，resume 回 RUNNING 并清空条件', () => {
  const { runtime } = makeRuntime();
  runtime.putTask({ taskId: 't1', title: '等审批' });
  assert.throws(() => runtime.wait({ taskId: 't1', condition: { kind: 'approval', ref: 'tkt-1' } }), {
    code: 'ILLEGAL_STATE'
  });
  runtime.transition({ taskId: 't1', to: 'RUNNING' });
  const waiting = runtime.wait({
    taskId: 't1',
    condition: { kind: 'approval', ref: 'tkt-1', until: '2026-10-22T00:00:00.000Z' }
  });
  assert.equal(waiting.state, 'WAITING');
  assert.deepEqual({ ...waiting.waitingFor }, {
    kind: 'approval',
    ref: 'tkt-1',
    until: '2026-10-22T00:00:00.000Z'
  });
  assert.ok(Object.isFrozen(waiting.waitingFor));

  // WAITING 下可更新条件，不改变状态
  const updated = runtime.wait({ taskId: 't1', condition: { kind: 'external_event', ref: 'evt-2' } });
  assert.equal(updated.state, 'WAITING');
  assert.equal(updated.waitingFor.kind, 'external_event');

  const resumed = runtime.resume({ taskId: 't1', reason: '审批通过' });
  assert.equal(resumed.state, 'RUNNING');
  assert.equal(resumed.waitingFor, null);
  assert.equal(resumed.lastReason, '审批通过');
  assert.throws(() => runtime.resume({ taskId: 't1' }), { code: 'ILLEGAL_STATE' });
  assert.throws(
    () => runtime.wait({ taskId: 't1', condition: { kind: 'unknown', ref: 'x' } }),
    TypeError
  );
});

test('snapshot：只含 PERSONAL + TASK（绝不含 WORKING），带 hash；personal 稳定排序', () => {
  const { clock, runtime } = makeRuntime();
  runtime.putPersonal({ key: 'b.key', value: 2 });
  runtime.putPersonal({ key: 'a.key', value: 1 });
  runtime.putTask({ taskId: 't1', title: '任务一', checkpoint: { progress: 0.5 } });
  runtime.setWorking({ turnId: 'turn-secret', context: { internal: 'WORKING-ONLY-DATA' } });
  clock.advance(5000);

  const bundle = runtime.snapshot();
  assert.ok(Object.isFrozen(bundle));
  assert.equal(bundle.version, 'im2a-runtime/1');
  assert.equal(bundle.userId, USER);
  assert.equal(bundle.exportedAt, '2026-10-21T00:00:05.000Z');
  assert.match(bundle.hash, /^[0-9a-f]{64}$/);
  assert.equal('working' in bundle, false);
  assert.deepEqual(bundle.personal.map((entry) => entry.key), ['a.key', 'b.key']);
  assert.equal(bundle.tasks[0].taskId, 't1');
  assert.ok(Object.isFrozen(bundle.personal));
  assert.ok(Object.isFrozen(bundle.tasks));
  // WORKING 内容绝不出现在包里
  const serialized = JSON.stringify(bundle);
  assert.equal(serialized.includes('WORKING-ONLY-DATA'), false);
  assert.equal(serialized.includes('turn-secret'), false);
  // hash 可复算
  const payload = {
    version: bundle.version,
    userId: bundle.userId,
    personal: bundle.personal,
    tasks: bundle.tasks,
    exportedAt: bundle.exportedAt
  };
  assert.equal(sha256Hex(canonicalize(payload)), bundle.hash);
});

test('restore：换实例恢复 PERSONAL + TASK，等待条件与检查点原样回来；WORKING 不迁移', () => {
  const { clock, runtime } = makeRuntime();
  runtime.putPersonal({ key: 'profile.city', value: '杭州' });
  runtime.putTask({ taskId: 't1', title: '预约体检', checkpoint: { step: 'choose-hospital' } });
  runtime.transition({ taskId: 't1', to: 'RUNNING' });
  runtime.wait({ taskId: 't1', condition: { kind: 'schedule', ref: 'cal-1', until: '2026-10-25T09:00:00.000Z' } });
  runtime.setWorking({ turnId: 'turn-1', context: { scratch: 'DO-NOT-MIGRATE' } });
  clock.advance(10_000);
  const bundle = runtime.snapshot();

  const { runtime: restored } = makeRuntime({ clock });
  restored.setWorking({ turnId: 'turn-old', context: { scratch: 'OLD' } });
  const result = restored.restore(bundle);
  assert.ok(Object.isFrozen(result));
  assert.equal(result.personal.length, 1);
  assert.equal(result.tasks.length, 1);
  assert.equal(restored.personal({ key: 'profile.city' }).value, '杭州');
  const task = restored.task('t1');
  assert.equal(task.state, 'WAITING');
  assert.deepEqual({ ...task.checkpoint }, { step: 'choose-hospital' });
  assert.equal(task.waitingFor.kind, 'schedule');
  assert.equal(task.waitingFor.ref, 'cal-1');
  assert.equal(restored.working(), null); // 执行期状态不迁移
});

test('restore 拒绝：version 不符 → VERSION_MISMATCH；hash/结构损坏 → BUNDLE_INVALID（不"尽力恢复"）', () => {
  const { runtime } = makeRuntime();
  runtime.putPersonal({ key: 'k', value: 'v' });
  runtime.putTask({ taskId: 't1', title: 'x' });
  const bundle = runtime.snapshot();

  const { runtime: otherVersion } = makeRuntime({ version: 'im2a-runtime/2' });
  assert.throws(() => otherVersion.restore(bundle), { code: 'VERSION_MISMATCH' });

  const { runtime: target } = makeRuntime();
  const tampered = { ...bundle, tasks: [{ ...bundle.tasks[0], state: 'COMPLETED' }] };
  assert.throws(() => target.restore(tampered), { code: 'BUNDLE_INVALID' });
  assert.throws(() => target.restore({ ...bundle, hash: 'deadbeef' }), { code: 'BUNDLE_INVALID' });
  assert.throws(() => target.restore({ ...bundle, personal: 'nope' }), { code: 'BUNDLE_INVALID' });
  assert.throws(() => target.restore(null), { code: 'BUNDLE_INVALID' });

  const { runtime: anotherUser } = makeRuntime();
  const otherUser = new PersonRuntime({ userId: 'user-other', clock: makeClock() });
  assert.throws(() => otherUser.restore(bundle), { code: 'BUNDLE_INVALID' });

  // 结构合法但分层被伪造（重算 hash）=> LAYER_INVALID：WORKING 不得借迁移包进入上下文
  const forgedPersonal = [{ ...bundle.personal[0], layer: STATE_LAYER.WORKING }];
  const forgedPayload = {
    version: bundle.version,
    userId: bundle.userId,
    personal: forgedPersonal,
    tasks: bundle.tasks,
    exportedAt: bundle.exportedAt
  };
  const forged = {
    ...forgedPayload,
    hash: sha256Hex(canonicalize(forgedPayload))
  };
  assert.throws(() => target.restore(forged), { code: 'LAYER_INVALID' });

  // 凭证 key 伪造（重算 hash）=> SECRET_IN_LAYER
  const secretPersonal = [{ ...bundle.personal[0], key: 'secret:token' }];
  const secretPayload = {
    version: bundle.version,
    userId: bundle.userId,
    personal: secretPersonal,
    tasks: bundle.tasks,
    exportedAt: bundle.exportedAt
  };
  assert.throws(
    () => target.restore({ ...secretPayload, hash: sha256Hex(canonicalize(secretPayload)) }),
    { code: 'SECRET_IN_LAYER' }
  );
  assert.equal(anotherUser.metrics().personalKeys, 0); // 失败不产生半恢复状态
});

test('metrics：Context 飞轮最小指标 { personalKeys, tasksTotal, tasksCompleted, tasksWaiting }', () => {
  const { runtime } = makeRuntime();
  runtime.putPersonal({ key: 'a', value: 1 });
  runtime.putPersonal({ key: 'b', value: 2 });
  runtime.putTask({ taskId: 't1', title: '完成', state: 'RUNNING' });
  runtime.transition({ taskId: 't1', to: 'COMPLETED' });
  runtime.putTask({ taskId: 't2', title: '等待', state: 'WAITING' });
  runtime.putTask({ taskId: 't3', title: '新建' });
  const metrics = runtime.metrics();
  assert.deepEqual({ ...metrics }, { personalKeys: 2, tasksTotal: 3, tasksCompleted: 1, tasksWaiting: 1 });
  assert.ok(Object.isFrozen(metrics));
});

test('PersonRuntimeError 带 code 且是 Error 子类', () => {
  const { runtime } = makeRuntime();
  try {
    runtime.putPersonal({ key: 'secret:token', value: 'x' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof PersonRuntimeError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'SECRET_IN_LAYER');
  }
});
