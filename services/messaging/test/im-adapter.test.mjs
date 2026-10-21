// 测试文件：§31 IM 渠道适配层（统一 MessageEvent / 能力缺口显式 / dispatch 错误边界 / 多平台并存）/ 运行：node services/messaging/test/im-adapter.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  ADAPTER_CAPABILITY,
  ADAPTER_CODE,
  IM_PLATFORM,
  ImAdapterError,
  ImAdapterRegistry,
  createSelfHostedKernelAdapter,
  createTencentImPaasAdapter,
  normalizeEvent,
} from '../src/im-adapter.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';
const EPOCH_SECONDS = Date.parse(BASE_ISO) / 1000;

function paasRaw(overrides = {}) {
  return {
    MsgID: 'msg-42',
    From_Account: 'user-a',
    GroupID: 'group-1',
    MsgTime: EPOCH_SECONDS,
    MsgBody: [{ MsgType: 'TIMTextElem', MsgContent: { Text: '你好，世界' } }],
    CloudCustomData: {
      senderKind: 'human',
      kind: 'TEXT',
      replyTo: 'msg-41',
      metadata: { trace: 't-1' },
      trustZone: 2,
    },
    ...overrides,
  };
}

function kernelRaw(overrides = {}) {
  return {
    id: 'msg-42',
    channelId: 'group-1',
    senderId: 'user-a',
    senderKind: 'human',
    kind: 'TEXT',
    text: '你好，世界',
    card: null,
    at: BASE_ISO,
    replyTo: 'msg-41',
    metadata: { trace: 't-1' },
    trustZone: 2,
    ...overrides,
  };
}

const stripIdentity = ({ platform, adapterId, ...rest }) => rest;

test('归一化：同一语义的 PaaS 与自托管原始事件 → 业务字段完全一致（除 platform/adapterId）', () => {
  const paasEvent = normalizeEvent({
    platform: IM_PLATFORM.TENCENT_IM_PAAS,
    adapterId: 'tencent-im-paas',
    raw: paasRaw(),
  });
  const kernelEvent = normalizeEvent({
    platform: IM_PLATFORM.SELF_HOSTED_KERNEL,
    adapterId: 'self-hosted-kernel',
    raw: kernelRaw(),
  });

  assert.deepEqual(stripIdentity(paasEvent), stripIdentity(kernelEvent), '业务层无需感知平台');
  assert.equal(paasEvent.platform, IM_PLATFORM.TENCENT_IM_PAAS);
  assert.equal(kernelEvent.platform, IM_PLATFORM.SELF_HOSTED_KERNEL);
  assert.equal(paasEvent.adapterId, 'tencent-im-paas');
  assert.equal(kernelEvent.adapterId, 'self-hosted-kernel');
  assert.equal(paasEvent.at, BASE_ISO);
  assert.deepEqual(Object.keys(paasEvent), [
    'eventId',
    'platform',
    'adapterId',
    'channelId',
    'senderId',
    'senderKind',
    'kind',
    'text',
    'card',
    'at',
    'replyTo',
    'metadata',
    'trustZone',
  ]);
  assert.ok(Object.isFrozen(paasEvent));
  assert.ok(Object.isFrozen(paasEvent.metadata));
  assert.equal(paasEvent.card, null);
});

test('归一化细节：CloudCustomData JSON 字符串 / CARD / Agent 发送方 / trustZone 默认 4', () => {
  const cardEvent = normalizeEvent({
    platform: IM_PLATFORM.TENCENT_IM_PAAS,
    adapterId: 'tencent-im-paas',
    raw: paasRaw({
      CloudCustomData: JSON.stringify({
        senderKind: 'agent',
        kind: 'CARD',
        card: { title: '审批卡片', actions: ['approve', 'reject'] },
        trustZone: 1,
      }),
    }),
  });
  assert.equal(cardEvent.kind, 'CARD');
  assert.equal(cardEvent.senderKind, 'agent');
  assert.deepEqual({ ...cardEvent.card }, { title: '审批卡片', actions: ['approve', 'reject'] });
  assert.ok(Object.isFrozen(cardEvent.card));
  assert.equal(cardEvent.trustZone, 1);

  const plain = normalizeEvent({
    platform: IM_PLATFORM.TENCENT_IM_PAAS,
    adapterId: 'tencent-im-paas',
    raw: paasRaw({ CloudCustomData: null }),
  });
  assert.equal(plain.trustZone, 4, '缺省信任档必须是 STRANGER(4)');
  assert.equal(plain.senderKind, 'human');
});

test('能力缺口显式：PaaS 缺 UNREAD_SHAPING/CUSTOM_MESSAGE → CAPABILITY_MISSING（不许假装支持）；内核全部可声明', () => {
  const registry = new ImAdapterRegistry();
  registry.register(createTencentImPaasAdapter({ send: () => 'paas-ack' }));
  registry.register(createSelfHostedKernelAdapter({ send: () => 'kernel-ack' }));

  assert.equal(
    registry.hasCapability({ adapterId: 'tencent-im-paas', capability: ADAPTER_CAPABILITY.UNREAD_SHAPING }),
    false
  );
  assert.equal(
    registry.hasCapability({ adapterId: 'self-hosted-kernel', capability: ADAPTER_CAPABILITY.UNREAD_SHAPING }),
    true
  );
  for (const capability of ['UNREAD_SHAPING', 'CUSTOM_MESSAGE']) {
    assert.throws(
      () => registry.requireCapability({ adapterId: 'tencent-im-paas', capability }),
      (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.CAPABILITY_MISSING,
      `PaaS 未声明 ${capability} 时必须显式报错，不能静默降级`
    );
    assert.doesNotThrow(() => registry.requireCapability({ adapterId: 'self-hosted-kernel', capability }));
  }
  assert.deepEqual([...registry.capabilitiesOf('tencent-im-paas')], ['AUDIT_CHAIN', 'CARD_CALLBACK']);
  assert.deepEqual(
    [...registry.capabilitiesOf('self-hosted-kernel')].sort(),
    [...Object.values(ADAPTER_CAPABILITY)].sort()
  );
});

test('dispatch：verify → normalize → send 回执，返回 platformMessageId；send 返回值三种形态', () => {
  const registry = new ImAdapterRegistry();
  const sent = [];
  registry.register(
    createTencentImPaasAdapter({
      send: (event) => {
        sent.push(event);
        return { platformMessageId: 'paas-ack-1' };
      },
    })
  );
  const result = registry.dispatch({ adapterId: 'tencent-im-paas', raw: paasRaw() });
  assert.equal(result.platformMessageId, 'paas-ack-1');
  assert.equal(result.event.eventId, 'msg-42');
  assert.equal(sent.length, 1);
  assert.equal(sent[0], result.event, 'send 收到的是归一化后的冻结事件');
  assert.ok(Object.isFrozen(result));

  const stringRegistry = new ImAdapterRegistry();
  stringRegistry.register(createSelfHostedKernelAdapter({ send: () => 'kernel-ack-9' }));
  assert.equal(stringRegistry.dispatch({ adapterId: 'self-hosted-kernel', raw: kernelRaw() }).platformMessageId, 'kernel-ack-9');

  const fallbackRegistry = new ImAdapterRegistry();
  fallbackRegistry.register(createSelfHostedKernelAdapter({ send: () => undefined }));
  assert.equal(
    fallbackRegistry.dispatch({ adapterId: 'self-hosted-kernel', raw: kernelRaw() }).platformMessageId,
    'msg-42',
    'send 无返回值时回退 eventId'
  );
});

test('dispatch 错误边界：send 抛错 → PLATFORM_ERROR（不吞）；verifyEvent false/抛错/非法 raw → EVENT_INVALID', () => {
  const throwingSend = new ImAdapterRegistry();
  throwingSend.register(createTencentImPaasAdapter({ send: () => { throw new Error('platform timeout'); } }));
  assert.throws(
    () => throwingSend.dispatch({ adapterId: 'tencent-im-paas', raw: paasRaw() }),
    (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.PLATFORM_ERROR && /platform timeout/.test(error.message)
  );

  const registry = new ImAdapterRegistry();
  registry.register({ adapterId: 'paas-ok', platform: IM_PLATFORM.TENCENT_IM_PAAS, capabilities: [], send: () => 'ack' });
  registry.register({
    adapterId: 'verify-false',
    platform: IM_PLATFORM.TENCENT_IM_PAAS,
    capabilities: [],
    send: () => 'ack',
    verifyEvent: () => false,
  });
  registry.register({
    adapterId: 'verify-throw',
    platform: IM_PLATFORM.TENCENT_IM_PAAS,
    capabilities: [],
    send: () => 'ack',
    verifyEvent: () => {
      throw new Error('boom');
    },
  });
  for (const adapterId of ['paas-ok', 'verify-false', 'verify-throw']) {
    assert.throws(
      () => registry.dispatch({ adapterId, raw: { garbage: true } }),
      (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.EVENT_INVALID,
      `${adapterId} 的非法 raw 必须 EVENT_INVALID`
    );
  }
  // 合法 raw 通过 paas-ok
  assert.equal(registry.dispatch({ adapterId: 'paas-ok', raw: paasRaw() }).event.channelId, 'group-1');
});

test('未知 adapter → ADAPTER_UNKNOWN；重复注册 → ADAPTER_EXISTS', () => {
  const registry = new ImAdapterRegistry();
  assert.throws(
    () => registry.dispatch({ adapterId: 'nope', raw: paasRaw() }),
    (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.ADAPTER_UNKNOWN
  );
  for (const call of [
    () => registry.requireCapability({ adapterId: 'nope', capability: ADAPTER_CAPABILITY.AUDIT_CHAIN }),
    () => registry.capabilitiesOf('nope'),
    () => registry.hasCapability({ adapterId: 'nope', capability: ADAPTER_CAPABILITY.AUDIT_CHAIN }),
  ]) {
    assert.throws(call, (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.ADAPTER_UNKNOWN);
  }

  registry.register(createTencentImPaasAdapter({ send: () => 'ack' }));
  assert.throws(
    () => registry.register(createTencentImPaasAdapter({ send: () => 'ack' })),
    (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.ADAPTER_EXISTS
  );
});

test('多平台并存：adaptersOf 按 platform 查、adapterId 互不干扰、能力独立', () => {
  const registry = new ImAdapterRegistry();
  const paasSent = [];
  const kernelSent = [];
  registry.register(
    createTencentImPaasAdapter({
      send: (event) => {
        paasSent.push(event);
        return 'paas-ack';
      },
    })
  );
  registry.register(
    createTencentImPaasAdapter({
      adapterId: 'tencent-im-paas-2',
      send: () => 'paas-2-ack',
    })
  );
  registry.register(
    createSelfHostedKernelAdapter({
      send: (event) => {
        kernelSent.push(event);
        return 'kernel-ack';
      },
    })
  );

  assert.equal(registry.adaptersOf(IM_PLATFORM.TENCENT_IM_PAAS).length, 2);
  assert.deepEqual(
    registry.adaptersOf(IM_PLATFORM.TENCENT_IM_PAAS).map((adapter) => adapter.adapterId).sort(),
    ['tencent-im-paas', 'tencent-im-paas-2']
  );
  assert.deepEqual(
    registry.adaptersOf(IM_PLATFORM.SELF_HOSTED_KERNEL).map((adapter) => adapter.adapterId),
    ['self-hosted-kernel']
  );
  assert.deepEqual([...registry.adaptersOf('unknown-platform')], []);

  const paasResult = registry.dispatch({ adapterId: 'tencent-im-paas', raw: paasRaw() });
  const kernelResult = registry.dispatch({ adapterId: 'self-hosted-kernel', raw: kernelRaw() });
  assert.equal(paasResult.platformMessageId, 'paas-ack');
  assert.equal(kernelResult.platformMessageId, 'kernel-ack');
  assert.equal(paasResult.event.platform, IM_PLATFORM.TENCENT_IM_PAAS);
  assert.equal(kernelResult.event.platform, IM_PLATFORM.SELF_HOSTED_KERNEL);
  assert.equal(paasSent.length, 1);
  assert.equal(kernelSent.length, 1);

  // 能力按 adapterId 独立：同平台第二个实例也不假装有 UNREAD_SHAPING
  assert.equal(registry.hasCapability({ adapterId: 'tencent-im-paas-2', capability: 'UNREAD_SHAPING' }), false);
  assert.equal(registry.hasCapability({ adapterId: 'self-hosted-kernel', capability: 'UNREAD_SHAPING' }), true);
});

test('自定义适配器（未知平台）走规范 raw fallback；默认 kind/trustZone 正确', () => {
  const registry = new ImAdapterRegistry();
  registry.register({ adapterId: 'custom-kernel', platform: 'my-platform', capabilities: [], send: () => 'ack' });
  const result = registry.dispatch({
    adapterId: 'custom-kernel',
    raw: { id: 'm-1', channelId: 'ch-1', senderId: 'user-a', at: BASE_ISO },
  });
  assert.equal(result.event.platform, 'my-platform');
  assert.equal(result.event.eventId, 'm-1');
  assert.equal(result.event.kind, 'TEXT');
  assert.equal(result.event.trustZone, 4);
  assert.equal(result.event.card, null);
});

test('normalizeEvent：非法输入一律 EVENT_INVALID（不抛非业务异常）', () => {
  const invalidFactories = [
    () => normalizeEvent(),
    () => normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: null }),
    () => normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: [] }),
    () => normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: paasRaw({ From_Account: '' }) }),
    () => normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: paasRaw({ MsgID: undefined }) }),
    () => normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: paasRaw({ GroupID: undefined }) }),
    () => normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: paasRaw({ MsgTime: 'bad' }) }),
    () =>
      normalizeEvent({
        platform: IM_PLATFORM.TENCENT_IM_PAAS,
        adapterId: 'x',
        raw: paasRaw({ CloudCustomData: '{bad json' }),
      }),
    () =>
      normalizeEvent({
        platform: IM_PLATFORM.TENCENT_IM_PAAS,
        adapterId: 'x',
        raw: paasRaw({ CloudCustomData: { senderKind: 'robot' } }),
      }),
    () =>
      normalizeEvent({
        platform: IM_PLATFORM.TENCENT_IM_PAAS,
        adapterId: 'x',
        raw: paasRaw({ CloudCustomData: { kind: 'VIDEO' } }),
      }),
    () =>
      normalizeEvent({
        platform: IM_PLATFORM.TENCENT_IM_PAAS,
        adapterId: 'x',
        raw: paasRaw({ CloudCustomData: { trustZone: 9 } }),
      }),
    () => normalizeEvent({ platform: IM_PLATFORM.SELF_HOSTED_KERNEL, adapterId: 'y', raw: { id: 'm', channelId: 'c' } }),
  ];
  for (const factory of invalidFactories) {
    assert.throws(factory, (error) => error instanceof ImAdapterError && error.code === ADAPTER_CODE.EVENT_INVALID);
  }
});

test('归一化后的 metadata/card 是拷贝：修改原始 raw 不影响事件', () => {
  const raw = paasRaw();
  const event = normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw });
  raw.CloudCustomData.metadata.trace = 'changed';
  assert.equal(event.metadata.trace, 't-1');
  const cardRaw = paasRaw({ CloudCustomData: { kind: 'CARD', card: { title: '原卡片' } } });
  const cardEvent = normalizeEvent({ platform: IM_PLATFORM.TENCENT_IM_PAAS, adapterId: 'x', raw: cardRaw });
  cardRaw.CloudCustomData.card.title = '改过';
  assert.equal(cardEvent.card.title, '原卡片');
});

test('注册表/工厂参数校验：adapterId/platform/send/verifyEvent/capability 非法 → TypeError', () => {
  const registry = new ImAdapterRegistry();
  assert.throws(() => registry.register({ platform: 'p', send: () => 'x' }), TypeError);
  assert.throws(() => registry.register({ adapterId: '', platform: 'p', send: () => 'x' }), TypeError);
  assert.throws(() => registry.register({ adapterId: 'a', platform: '', send: () => 'x' }), TypeError);
  assert.throws(() => registry.register({ adapterId: 'a', platform: 'p' }), TypeError);
  assert.throws(() => registry.register({ adapterId: 'a', platform: 'p', send: () => 'x', verifyEvent: 'nope' }), TypeError);
  assert.throws(
    () => registry.register({ adapterId: 'a', platform: 'p', send: () => 'x', capabilities: ['NOT_A_CAPABILITY'] }),
    TypeError
  );
  registry.register({ adapterId: 'a', platform: 'p', send: () => 'x' });
  assert.throws(() => registry.hasCapability({ adapterId: 'a', capability: 'NOT_A_CAPABILITY' }), TypeError);
  assert.throws(() => registry.requireCapability({ adapterId: 'a', capability: 'NOT_A_CAPABILITY' }), TypeError);
  assert.throws(() => registry.adaptersOf(''), TypeError);
  assert.throws(() => createTencentImPaasAdapter({}), TypeError);
  assert.throws(() => createSelfHostedKernelAdapter({ send: 'not-a-function' }), TypeError);
});
