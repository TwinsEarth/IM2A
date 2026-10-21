/**
 * 模块职责：v2「面向 Agent 的 IM 内核」端到端测试 —— 验证六项 v2 变更在真实服务里成立
 * 契约来源：contracts/INTERFACES.md §11–§16（身份 / 网关 / 可见域 / 频道 / 会话 / 签名审计）
 *
 * 这不是补测试，而是 v2 的论证：下列断言每一条都对应一个"如果没有这层就会出事"的场景。
 *   · 协商频道对人类未读恒为 0 —— 协商不打扰人（否则 IM 会被 Agent 的中间态淹没）
 *   · 同名请求不同 Agent 拿到不同字段 —— 读侧可见域（否则 Agent 能把通讯录整库读走）
 *   · 换签名者后哈希链仍"完整"但签名失败 —— 只做哈希链防不住有库权限的攻击者
 *   · 网关是唯一入口 —— 未认证/未授权流量在入口就被拒绝
 *
 * 运行：node tests/im-live-v2.test.mjs（或 node scripts/test.mjs --filter=im-live-v2）
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { startServer } from '../apps/im-live/server.mjs';
import { USERS, AGENTS } from '../apps/demo-server/flow.mjs';

const json = (url, init) =>
  fetch(url, { headers: { 'content-type': 'application/json' }, ...(init ?? {}) }).then(async (response) => ({
    status: response.status,
    body: await response.json().catch(() => ({}))
  }));

async function withServer(fn) {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    return await fn(app);
  } finally {
    await app.close();
  }
}

test('Agent 身份：每个 Agent 有可验签的 Ed25519 身份与订阅范围，在线状态来自会话层', async () => {
  await withServer(async (app) => {
    const { status, body } = await json(`${app.url}/api/v2/agents`);
    assert.equal(status, 200);
    assert.equal(body.agents.length, 4, '4 个 Agent 都有身份');

    const online = body.agents.filter((agent) => agent.state === 'ONLINE');
    assert.equal(online.length, 2, '两个总管已作为长连接客户端在线');

    for (const agent of body.agents) {
      assert.equal(agent.identity.keyAlg, 'ed25519');
      assert.match(agent.identity.fingerprint, /^[0-9a-f]{16}$/, '身份指纹=公钥哈希前 16 位');
      assert.ok(agent.identity.cardHash, 'AgentCard 有哈希');
      assert.ok(Array.isArray(agent.identity.domains) && agent.identity.domains.length > 0, '卡片声明可见域');
      assert.ok(Number.isInteger(agent.identity.trustCeiling), '卡片声明信任上限');
      assert.ok(agent.token.expiresAt, 'token 有有效期');
    }

    // 订阅是"频道集合"，不是全局广播
    const butler = body.agents.find((agent) => agent.agentId === AGENTS.LIN_BUTLER);
    assert.ok(butler.subscriptions.includes('ch-negotiation-lin-zhou'));
    assert.ok(body.audit.ok, '签名审计链完整');
  });
});

test('未读隔离：Agent 在协商频道聊一轮，人类未读仍然是 0', async () => {
  await withServer(async (app) => {
    const before = await json(`${app.url}/api/v2/channels`);
    const negotiationBefore = before.body.channels.find((c) => c.type === 'AGENT_NEGOTIATION');
    assert.ok(negotiationBefore, '存在 AGENT_NEGOTIATION 频道');
    assert.equal(before.body.unread[USERS.LIN].total, 0, '初始人类未读为 0');

    const turn = await json(`${app.url}/api/v2/negotiation/turn`, {
      method: 'POST',
      body: JSON.stringify({
        fromAgentId: AGENTS.ZHOU_BUTLER,
        intent: 'COUNTER',
        payload: { summary: '接受周五 16:00，BP 已提前提交' }
      })
    });
    assert.equal(turn.status, 200);
    assert.equal(turn.body.ok, true);
    assert.equal(turn.body.gateway.decision, 'ALLOW', '协商轮次必须经网关放行');
    assert.equal(turn.body.stored, true, 'AGENT_TURN 要入库（可追溯）');
    assert.ok(turn.body.deliveredTo.includes(AGENTS.LIN_BUTLER), '订阅的对方总管收到');
    assert.equal(turn.body.deliveredTo.includes(AGENTS.ZHOU_BUTLER), false, '发送方不重复投递给自己');
    assert.ok(turn.body.pushed.delivered.length >= 2, '会话层推送给在线且订阅的 Agent（含发送方回执）');

    const after = await json(`${app.url}/api/v2/channels`);
    assert.equal(after.body.unread[USERS.LIN].total, 0, 'Agent 协商不得制造人类未读');
    assert.equal(after.body.unread[USERS.LIN].byChannel['ch-negotiation-lin-zhou'] ?? 0, 0);

    // 但 Agent 侧的推送记录必须存在——否则就是"没发生"，不是"不打扰"
    assert.ok(after.body.agentPushes.length >= 1, 'Agent 侧收到了推送');
    assert.equal(after.body.channels.find((c) => c.type === 'AGENT_NEGOTIATION').members.length, 2);
  });
});

test('读侧可见域：同一个 domain，不同 Agent 拿到的字段不同', async () => {
  await withServer(async (app) => {
    const { status, body } = await json(`${app.url}/api/v2/visibility`);
    assert.equal(status, 200);
    assert.ok(Array.isArray(body.matrix) && body.matrix.length >= 7, '预置矩阵至少 7 组探测');

    const byLabel = (keyword) => body.matrix.find((row) => String(row.label).includes(keyword));
    const decisions = body.matrix.map((row) => row.result.decision.decision);
    assert.ok(decisions.includes('ALLOW'), '存在放行');
    assert.ok(decisions.includes('REDACT'), '存在"放行但裁剪字段"');
    assert.ok(decisions.includes('DENY'), '存在拒绝');

    const codes = body.matrix.map((row) => row.result.decision.code);
    assert.ok(codes.includes('TRUST_CEILING_EXCEEDED'), '信任上限外必须拒绝');
    assert.ok(codes.includes('DOMAIN_NOT_GRANTED'), '未授权域必须拒绝');
    assert.ok(codes.includes('SCOPE_EXPIRED'), '过期 scope 必须拒绝');

    // 陌生人档只回存在性
    const strangerRead = byLabel('陌生人总管读「陌生人档」');
    assert.equal(strangerRead.result.decision.decision, 'REDACT');
    const projected = strangerRead.result.projection.projected;
    assert.deepEqual(Object.keys(projected).sort(), ['exists', 'id'], '陌生人档只暴露 id 与存在性');

    // 记忆域任何档都只回摘要，绝不返回原始内容
    const memoryRead = byLabel('总管读记忆');
    assert.equal(memoryRead.result.decision.decision, 'ALLOW');
    const memProjected = memoryRead.result.projection.projected;
    assert.equal(memProjected.content, undefined, '记忆原始内容绝不外发');
    assert.ok(memProjected.summary, '只回摘要');
    assert.ok(!('detail' in memProjected), '记忆域不返回 detail');
  });
});

test('签名审计：改内容→链断；换签名者→链完好但签名失败（只做哈希链防不住的那类攻击）', async () => {
  await withServer(async (app) => {
    const audit = await json(`${app.url}/api/v2/audit`);
    assert.equal(audit.status, 200);
    assert.equal(audit.body.verify.ok, true, '真实审计链验签通过');
    assert.ok(audit.body.entries.length >= 2, '已有审计条目');
    assert.ok(audit.body.entries.every((entry) => entry.hasSignature), '每条都有 Ed25519 签名');

    const contentTamper = await json(`${app.url}/api/v2/audit/tamper`, {
      method: 'POST',
      body: JSON.stringify({ kind: 'content' })
    });
    assert.equal(contentTamper.status, 200);
    const a = contentTamper.body.demo;
    assert.equal(a.baseline.chainOk, true);
    assert.equal(a.after.chainOk, false, '改内容后哈希链必须断');
    assert.equal(a.after.chainBrokenAt !== null, true, '断点位置必须可定位');

    const resignTamper = await json(`${app.url}/api/v2/audit/tamper`, {
      method: 'POST',
      body: JSON.stringify({ kind: 'resign' })
    });
    const b = resignTamper.body.demo;
    assert.equal(b.after.chainOk, true, '换签名者时哈希链看不出问题（这正是纯链的盲区）');
    assert.equal(b.after.signaturesOk, false, '签名层必须失败——这是 v2 加签名的唯一理由');
  });
});

test('网关是唯一入口：未知 Agent 无法发起协商；下线后不再收到推送', async () => {
  await withServer(async (app) => {
    const unknown = await json(`${app.url}/api/v2/negotiation/turn`, {
      method: 'POST',
      body: JSON.stringify({ fromAgentId: 'agent-does-not-exist', intent: 'PROPOSE', payload: {} })
    });
    assert.ok(unknown.status >= 400, '未注册 Agent 被入口拒绝');

    const disconnected = await json(`${app.url}/api/v2/agents/${AGENTS.ZHOU_BUTLER}/disconnect`, { method: 'POST' });
    assert.equal(disconnected.status, 200);
    const after = await json(`${app.url}/api/v2/agents`);
    const zhou = after.body.agents.find((agent) => agent.agentId === AGENTS.ZHOU_BUTLER);
    assert.equal(zhou.state, 'OFFLINE', '断开后状态必须如实变化');

    const pushed = await json(`${app.url}/api/v2/negotiation/turn`, {
      method: 'POST',
      body: JSON.stringify({ fromAgentId: AGENTS.LIN_BUTLER, intent: 'PROPOSE', payload: { summary: '重新约时间' } })
    });
    assert.equal(pushed.body.pushed.delivered.includes(AGENTS.ZHOU_BUTLER), false, '离线的 Agent 不再收到推送');

    const reconnected = await json(`${app.url}/api/v2/agents/${AGENTS.ZHOU_BUTLER}/connect`, {
      method: 'POST',
      body: JSON.stringify({ subscriptions: ['ch-negotiation-lin-zhou'] })
    });
    assert.equal(reconnected.status, 200);
    assert.equal(reconnected.body.session.state, 'ONLINE');
  });
});

test('v3：审计讨论保留异议，人格与记忆分层且 SOUL 可携带', async () => {
  await withServer(async (app) => {
    // ── 多 Agent 结构化审计（imcodes 的 audit 模式）──
    const discussions = await json(`${app.url}/api/v2/discussions`);
    assert.equal(discussions.status, 200);
    const audit = discussions.body.items[0];
    assert.equal(audit.mode, 'AUDIT');
    assert.equal(audit.state, 'CONCLUDED');
    assert.ok(audit.rounds >= 2, '至少两轮');
    assert.ok(audit.speeches >= 4, '多方发言');
    assert.ok(audit.findings.length >= 1, 'AUDIT 必须有 findings');
    assert.equal(audit.dissent.length, 1, '异议必须原样保留');
    assert.match(audit.dissent[0].text, /保修/, '异议内容不得被"综合"掉');

    // ── 人格与记忆生命周期（nekoclaw / MindRoom）──
    const persona = await json(`${app.url}/api/v2/persona`);
    assert.equal(persona.status, 200);
    assert.ok(persona.body.soul.values.includes('家人优先于工作'), 'SOUL 是稳定人格基座');
    assert.ok(persona.body.soul.boundaries.length >= 1, 'SOUL 有边界');
    assert.ok(Object.keys(persona.body.stages).length >= 3, '记忆按 Agent 分层统计');
    assert.equal(persona.body.portableSummary.version, 'agentim-persona/1');
    assert.equal(persona.body.portableSummary.signed, true, '跨平台可携带包应签名');

    // ── 纪要只把 hash 与证据面写进签名链 ──
    const auditChain = await json(`${app.url}/api/v2/audit`);
    const synth = auditChain.body.entries.find((entry) => entry.action === 'SYNTHESIZE');
    assert.ok(synth, '讨论结论必须有签名审计条目');
    assert.equal(synth.hasSignature, true);
    assert.ok(synth.detail?.minutesHash, '链上只锚纪要 hash');
    assert.equal(synth.detail?.dissentCount, 1, '异议计数进证据面');
    assert.ok(!JSON.stringify(synth.detail ?? {}).includes('保修维度应在下单价前纳入'), '长内容不进哈希链');
  });
});

test('v4：宿主型 IM 四机制（可信上下文 / 旁观静默 / 模型路由 / 轮次内投递）', async () => {
  await withServer(async (app) => {
    // ① 服务端可信上下文：签名的、注入的、不可伪造的
    const context = await json(`${app.url}/api/v2/context`);
    assert.equal(context.status, 200);
    assert.equal(context.body.verify.ok, true, '注入的上下文必须可验签');
    assert.equal(context.body.context.signed, true);
    assert.equal(context.body.forgery.ok, false, 'Agent 篡改上下文必须被抓住');
    assert.equal(context.body.forgery.code, 'CONTEXT_FORGED');
    assert.equal(context.body.crossAgent.ok, false, '换个 Agent 复用上下文必须被拒');
    assert.equal(context.body.crossAgent.code, 'CONTEXT_SCOPE_MISMATCH');
    assert.ok(context.body.context.conversation?.members?.length >= 2, '上下文自带会话与成员，Agent 不用自己查');

    // ② 旁观与主动沉默：家庭群记录但不说；1:1 不受影响
    const presence = await json(`${app.url}/api/v2/presence`);
    assert.equal(presence.status, 200);
    const family = presence.body.decisions.find((d) => d.label.includes('家庭群（未被 @）'));
    assert.equal(family.result.speak, false, '家庭群未被 @ 时不该说话');
    assert.equal(family.result.recordSilently, true, '但必须记录下来');
    const direct = presence.body.decisions.find((d) => d.label.includes('1:1'));
    assert.equal(direct.result.speak, true, '1:1 对话不能被沉默策略掐死');

    // ③ 模型路由：家庭/法律不出端侧，显式覆盖可审
    const routing = await json(`${app.url}/api/v2/model-route`);
    assert.equal(routing.status, 200);
    const byLabel = (keyword) => routing.body.decisions.find((d) => d.label.includes(keyword));
    assert.equal(byLabel('家庭事务').tier, 'LOCAL', '家庭事务走本地模型');
    assert.equal(byLabel('法律咨询').tier, 'LOCAL');
    assert.equal(byLabel('工作邮件').tier, 'CLOUD');
    const override = routing.body.decisions.find((d) => d.label.includes('显式'));
    assert.equal(override.tier, 'LOCAL');
    assert.equal(override.code, 'EXPLICIT_OVERRIDE', '显式覆盖必须能被审计解释');

    // ④ 轮次内投递：不等下一次用户提示
    const turn = await json(`${app.url}/api/v2/turn-demo`, { method: 'POST' });
    assert.equal(turn.status, 200);
    assert.equal(turn.body.injected, 'MID_TURN', '轮次进行中的消息必须是轮次内投递');
    assert.equal(turn.body.consumed, 1, '被 runtime 在当前轮次内消费');
    assert.match(String(turn.body.sample), /周五 16:00/);
    assert.equal(turn.body.queued, 'QUEUED', '轮次外的消息入队而不是丢失');
    assert.equal(turn.body.ended.state, 'CLOSED');
  });
});

test('/api/state 暴露 v2 摘要，/v2 页面可访问', async () => {
  await withServer(async (app) => {
    const state = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    assert.equal(state.body.v2.available, true);
    assert.equal(state.body.v2.agentsOnline, 2);
    assert.ok(state.body.v2.channels >= 5);
    assert.equal(state.body.v2.auditOk, true);

    const page = await fetch(`${app.url}/v2`);
    assert.equal(page.status, 200);
    const html = await page.text();
    assert.ok(html.includes('AGENT_NEGOTIATION'), '页面说明协商频道');
    assert.ok(html.includes('可见域'), '页面展示读侧权限');
  });
});
