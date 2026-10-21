// 测试文件：§18 多 Agent 结构化讨论引擎（DISCUSS/AUDIT/REVIEW、轮次与发言上限、异议保留）/ 运行：node services/orchestrator/test/discussion-engine.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  DISCUSSION_CODE,
  DISCUSSION_MODE,
  DISCUSSION_STATE,
  STANCE,
  DiscussionEngine,
  DiscussionError,
} from '../src/discussion-engine.mjs';
import { makeClock } from './_helpers.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function makeEngine(options = {}) {
  const clock = makeClock(BASE_ISO);
  return { engine: new DiscussionEngine({ clock, ...options }), clock };
}

function openDiscussion(engine, overrides = {}) {
  return engine.open({
    mode: DISCUSSION_MODE.DISCUSS,
    topic: '是否给陌生人开放付费消息',
    ownerUserId: 'user-owner',
    participants: [
      { agentId: 'agent-a', role: 'moderator' },
      { agentId: 'agent-b', role: 'expert' },
    ],
    agenda: ['风险', '收益'],
    ...overrides,
  });
}

function speak(engine, discussionId, agentId, overrides = {}) {
  return engine.speak({
    discussionId,
    agentId,
    stance: STANCE.SUPPORT,
    content: `${agentId} 的意见`,
    ...overrides,
  });
}

test('open：正常路径（state=OPEN、round=1、字段完整且冻结）', () => {
  const { engine } = makeEngine();
  const discussion = openDiscussion(engine);
  assert.match(discussion.id, /^dis-/);
  assert.equal(discussion.mode, DISCUSSION_MODE.DISCUSS);
  assert.equal(discussion.topic, '是否给陌生人开放付费消息');
  assert.equal(discussion.ownerUserId, 'user-owner');
  assert.equal(discussion.state, DISCUSSION_STATE.OPEN);
  assert.equal(discussion.round, 1);
  assert.equal(discussion.speeches.length, 0);
  assert.deepEqual(
    discussion.participants.map((participant) => [participant.agentId, participant.role]),
    [['agent-a', 'moderator'], ['agent-b', 'expert']]
  );
  assert.deepEqual([...discussion.agenda], ['风险', '收益']);
  assert.equal(discussion.openedAt, BASE_ISO);
  assert.equal(discussion.concludedAt, null);
  assert.ok(Object.isFrozen(discussion));
  assert.ok(Object.isFrozen(discussion.participants));
  assert.ok(Object.isFrozen(discussion.participants[0]));
  assert.equal(engine.get(discussion.id), discussion);
});

test('open 边界：MODE_INVALID / PARTICIPANTS_INVALID（不足 2 人、重复 agentId、空 agentId）', () => {
  const { engine } = makeEngine();
  assert.throws(
    () => openDiscussion(engine, { mode: 'DEBATE' }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.MODE_INVALID
  );
  const invalidParticipants = [
    [{ agentId: 'agent-a' }],
    [],
    [{ agentId: 'agent-a' }, { agentId: 'agent-a' }],
    [{ agentId: 'agent-a' }, { agentId: '' }],
    [{ agentId: 'agent-a' }, { role: 'expert' }],
  ];
  for (const participants of invalidParticipants) {
    assert.throws(
      () => openDiscussion(engine, { participants }),
      (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.PARTICIPANTS_INVALID,
      `应拒绝 participants=${JSON.stringify(participants)}`
    );
  }
});

test('speak：三方各发言一轮，state=IN_ROUND、轮次归属正确、返回新冻结对象', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine, {
    participants: [{ agentId: 'agent-a' }, { agentId: 'agent-b' }, { agentId: 'agent-c' }],
  });
  const afterA = speak(engine, opened.id, 'agent-a', { stance: STANCE.SUPPORT });
  const afterB = speak(engine, opened.id, 'agent-b', { stance: STANCE.OPPOSE });
  const afterC = speak(engine, opened.id, 'agent-c', {
    stance: STANCE.QUESTION,
    content: '成本怎么算？',
    evidence: [{ kind: 'data', ref: 'doc-1' }],
  });

  assert.equal(opened.state, DISCUSSION_STATE.OPEN, '旧对象保持原状态');
  assert.equal(afterC.state, DISCUSSION_STATE.IN_ROUND);
  assert.equal(afterC.round, 1);
  assert.deepEqual(afterC.speeches.map((speech) => speech.agentId), ['agent-a', 'agent-b', 'agent-c']);
  assert.deepEqual(afterC.speeches.map((speech) => speech.stance), ['SUPPORT', 'OPPOSE', 'QUESTION']);
  assert.equal(afterC.speeches[2].content, '成本怎么算？');
  assert.deepEqual({ ...afterC.speeches[2].evidence[0] }, { kind: 'data', ref: 'doc-1' });
  assert.ok(Object.isFrozen(afterC.speeches[2]));
  assert.ok(Object.isFrozen(afterC.speeches[2].evidence));
  assert.notEqual(afterA, afterB);
  assert.equal(engine.get(opened.id), afterC);
});

test('TURN_LIMIT：同一人同一轮重复发言被拒；已关闭轮次不可补写（重复发言优先 TURN_LIMIT）', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine, {
    participants: [{ agentId: 'agent-a' }, { agentId: 'agent-b' }, { agentId: 'agent-c' }, { agentId: 'agent-d' }],
  });
  speak(engine, opened.id, 'agent-a');
  speak(engine, opened.id, 'agent-b');
  speak(engine, opened.id, 'agent-c'); // agent-d 本轮未发言
  assert.throws(
    () => speak(engine, opened.id, 'agent-a'),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.TURN_LIMIT
  );

  assert.equal(engine.nextRound({ discussionId: opened.id }).round, 2);
  // 已合并的第 1 轮里，C 再次发言 → TURN_LIMIT
  assert.throws(
    () => speak(engine, opened.id, 'agent-c', { round: 1 }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.TURN_LIMIT
  );
  // 第 1 轮没发过言的 D 也不能补写已关闭轮次 → ILLEGAL_STATE
  assert.throws(
    () => speak(engine, opened.id, 'agent-d', { round: 1 }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.ILLEGAL_STATE
  );
  // 第 2 轮每人仍可发言一次
  speak(engine, opened.id, 'agent-a', { stance: STANCE.EVIDENCE });
  assert.throws(
    () => speak(engine, opened.id, 'agent-a'),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.TURN_LIMIT
  );
  // 未开启的第 3 轮 → ILLEGAL_STATE
  assert.throws(
    () => speak(engine, opened.id, 'agent-d', { round: 3 }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.ILLEGAL_STATE
  );
});

test('ROUND_LIMIT：显式指定超过 maxRounds 的轮次 → ROUND_LIMIT', () => {
  const { engine } = makeEngine({ maxRounds: 2 });
  const opened = openDiscussion(engine);
  speak(engine, opened.id, 'agent-a');
  assert.throws(
    () => speak(engine, opened.id, 'agent-b', { round: 3 }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.ROUND_LIMIT
  );
});

test('超过 maxRounds → ESCALATED（不静默丢弃），minutes 保留全部已发生发言', () => {
  const { engine } = makeEngine({ maxRounds: 2 });
  const opened = openDiscussion(engine, {
    participants: [{ agentId: 'agent-a' }, { agentId: 'agent-b' }, { agentId: 'agent-c' }],
  });
  speak(engine, opened.id, 'agent-a');
  speak(engine, opened.id, 'agent-b');
  assert.equal(engine.nextRound({ discussionId: opened.id }).round, 2);
  speak(engine, opened.id, 'agent-a', { stance: STANCE.OPPOSE });
  speak(engine, opened.id, 'agent-b', { stance: STANCE.QUESTION });
  speak(engine, opened.id, 'agent-c', { stance: STANCE.EVIDENCE });

  assert.equal(engine.nextRound({ discussionId: opened.id }).round, 2, '已到上限时不再开新轮');
  const escalated = engine.get(opened.id);
  assert.equal(escalated.state, DISCUSSION_STATE.ESCALATED);
  assert.equal(escalated.escalationReason, 'ROUND_LIMIT');
  assert.equal(escalated.escalatedAt, BASE_ISO);

  const minutes = engine.minutes(opened.id);
  assert.equal(minutes.state, DISCUSSION_STATE.ESCALATED);
  assert.equal(minutes.rounds.length, 2);
  assert.deepEqual(minutes.rounds.map((round) => round.round), [1, 2]);
  const allSpeeches = minutes.rounds.flatMap((round) => round.speeches);
  assert.equal(allSpeeches.length, 5, '升级前全部发言必须保留');
  assert.deepEqual(allSpeeches.map((speech) => speech.agentId), ['agent-a', 'agent-b', 'agent-a', 'agent-b', 'agent-c']);

  // 终态不可再流转
  assert.throws(() => speak(engine, opened.id, 'agent-a'), { code: 'ILLEGAL_STATE' });
  assert.throws(() => engine.nextRound({ discussionId: opened.id }), { code: 'ILLEGAL_STATE' });
  assert.throws(
    () => engine.synthesize({ discussionId: opened.id, byAgentId: 'agent-a', conclusion: '结论' }),
    { code: 'ILLEGAL_STATE' }
  );
  assert.throws(() => engine.escalate({ discussionId: opened.id }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => engine.abort({ discussionId: opened.id }), { code: 'ILLEGAL_STATE' });
});

test('AUDIT：没有 findings 不能形成结论（FINDINGS_REQUIRED），补齐 findings 后 CONCLUDED', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine, { mode: DISCUSSION_MODE.AUDIT });
  speak(engine, opened.id, 'agent-a', { stance: STANCE.EVIDENCE });
  speak(engine, opened.id, 'agent-b', { stance: STANCE.OPPOSE });

  assert.throws(
    () => engine.synthesize({ discussionId: opened.id, byAgentId: 'agent-a', dissent: [] }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.FINDINGS_REQUIRED
  );
  // 失败后状态不变、仍可继续
  assert.equal(engine.get(opened.id).state, DISCUSSION_STATE.IN_ROUND);

  const findings = [
    { id: 'F1', severity: 'high', detail: '缺少冷启动风控' },
    { id: 'F2', severity: 'medium', detail: '退款路径未覆盖' },
  ];
  const concluded = engine.synthesize({
    discussionId: opened.id,
    byAgentId: 'agent-a',
    conclusion: '暂不通过，修复后复审',
    findings,
    dissent: [{ agentId: 'agent-b', reason: '上线窗口太紧' }],
  });
  assert.equal(concluded.state, DISCUSSION_STATE.CONCLUDED);
  assert.equal(concluded.concludedAt, BASE_ISO);
  assert.equal(concluded.synthesizedBy, 'agent-a');
  const minutes = engine.minutes(opened.id);
  assert.equal(minutes.conclusion, '暂不通过，修复后复审');
  assert.deepEqual(minutes.findings.map((finding) => finding.id), ['F1', 'F2']);
  assert.deepEqual(minutes.dissent.map((item) => item.agentId), ['agent-b']);
});

test('REVIEW：同样必须至少一条 findings；DISCUSS 必须 conclusion', () => {
  const { engine } = makeEngine();
  const review = openDiscussion(engine, { mode: DISCUSSION_MODE.REVIEW });
  assert.throws(
    () => engine.synthesize({ discussionId: review.id, byAgentId: 'agent-a', findings: [] }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.FINDINGS_REQUIRED
  );

  const discuss = openDiscussion(engine);
  assert.throws(
    () => engine.synthesize({ discussionId: discuss.id, byAgentId: 'agent-a', conclusion: '' }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.CONCLUSION_REQUIRED
  );
  assert.throws(
    () => engine.synthesize({ discussionId: discuss.id, byAgentId: 'agent-a' }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.CONCLUSION_REQUIRED
  );
  const concluded = engine.synthesize({
    discussionId: discuss.id,
    byAgentId: 'agent-a',
    conclusion: '分阶段放开',
    dissent: [],
  });
  assert.equal(concluded.state, DISCUSSION_STATE.CONCLUDED);
});

test('异议保留：dissent 原样进入 minutes（顺序/长度/内容不变、不去重、不被综合）', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine, { mode: DISCUSSION_MODE.AUDIT });
  speak(engine, opened.id, 'agent-a', { stance: STANCE.SUPPORT });
  speak(engine, opened.id, 'agent-b', { stance: STANCE.OPPOSE, content: '不同意' });

  const dissent = [
    { agentId: 'agent-b', stance: STANCE.OPPOSE, reason: '隐私风险大于收益' },
    { agentId: 'agent-b', stance: STANCE.OPPOSE, reason: '隐私风险大于收益' }, // 重复项也要原样保留
    { agentId: 'agent-c', stance: STANCE.QUESTION, reason: '成本未评估' },
  ];
  const dissentSnapshot = JSON.parse(JSON.stringify(dissent));
  const concluded = engine.synthesize({
    discussionId: opened.id,
    byAgentId: 'agent-a',
    conclusion: '有条件通过',
    findings: [{ id: 'F1', severity: 'low', detail: '补充隐私说明' }],
    dissent,
  });
  assert.equal(concluded.dissent.length, 3);
  const minutes = engine.minutes(opened.id);
  assert.equal(minutes.dissent.length, dissent.length, '异议条数必须一致');
  assert.deepEqual(
    minutes.dissent.map((item) => ({ ...item })),
    dissentSnapshot,
    '异议必须原样保留（顺序与内容都不许被综合）'
  );
  assert.ok(Object.isFrozen(minutes.dissent));
  assert.ok(Object.isFrozen(minutes.dissent[0]));
  // 修改调用方传入的 dissent 不影响纪要
  dissent.push({ agentId: 'agent-d', reason: '追加' });
  dissent[0].reason = 'changed';
  assert.equal(engine.minutes(opened.id).dissent.length, 3);
  assert.equal(engine.minutes(opened.id).dissent[0].reason, '隐私风险大于收益');
});

test('NOT_PARTICIPANT：非参与者不能发言、不能 synthesize', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine);
  assert.throws(
    () => speak(engine, opened.id, 'agent-x'),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.NOT_PARTICIPANT
  );
  assert.throws(
    () => engine.synthesize({ discussionId: opened.id, byAgentId: 'agent-x', conclusion: '结论' }),
    (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.NOT_PARTICIPANT
  );
});

test('NOT_FOUND：所有操作对未知 discussionId 抛 NOT_FOUND', () => {
  const { engine } = makeEngine();
  const unknown = 'dis-unknown';
  const operations = [
    () => engine.get(unknown),
    () => speak(engine, unknown, 'agent-a'),
    () => engine.nextRound({ discussionId: unknown }),
    () => engine.synthesize({ discussionId: unknown, byAgentId: 'agent-a', conclusion: '结论' }),
    () => engine.escalate({ discussionId: unknown }),
    () => engine.abort({ discussionId: unknown }),
    () => engine.minutes(unknown),
  ];
  for (const operation of operations) {
    assert.throws(operation, (error) => error instanceof DiscussionError && error.code === DISCUSSION_CODE.NOT_FOUND);
  }
});

test('escalate / abort：人工终止并记录原因；终态后任何操作都 ILLEGAL_STATE', () => {
  const { engine } = makeEngine();
  const escalated = openDiscussion(engine);
  const escalatedResult = engine.escalate({ discussionId: escalated.id, reason: '需要人工裁决' });
  assert.equal(escalatedResult.state, DISCUSSION_STATE.ESCALATED);
  assert.equal(escalatedResult.escalationReason, '需要人工裁决');
  assert.throws(() => engine.abort({ discussionId: escalated.id }), { code: 'ILLEGAL_STATE' });

  const aborted = openDiscussion(engine);
  speak(engine, aborted.id, 'agent-a');
  const abortedResult = engine.abort({ discussionId: aborted.id, reason: '发起方撤回' });
  assert.equal(abortedResult.state, DISCUSSION_STATE.ABORTED);
  assert.equal(abortedResult.abortReason, '发起方撤回');
  assert.throws(() => speak(engine, aborted.id, 'agent-b'), { code: 'ILLEGAL_STATE' });
  assert.throws(() => engine.synthesize({ discussionId: aborted.id, byAgentId: 'agent-a', conclusion: '结论' }), {
    code: 'ILLEGAL_STATE',
  });
});

test('minutes：结构化纪要（轮次升序、speeches 完整、participants 与时间戳）', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine, {
    participants: [{ agentId: 'agent-a' }, { agentId: 'agent-b' }, { agentId: 'agent-c' }],
    agenda: ['风险'],
  });
  speak(engine, opened.id, 'agent-a', { content: 'r1-a' });
  speak(engine, opened.id, 'agent-b', { content: 'r1-b' });
  engine.nextRound({ discussionId: opened.id });
  speak(engine, opened.id, 'agent-c', { content: 'r2-c' });
  const concluded = engine.synthesize({
    discussionId: opened.id,
    byAgentId: 'agent-a',
    conclusion: '按第二轮意见收敛',
    findings: [],
    dissent: [{ agentId: 'agent-b', reason: '保留意见' }],
  });
  assert.equal(concluded.state, DISCUSSION_STATE.CONCLUDED);

  const minutes = engine.minutes(opened.id);
  assert.deepEqual(Object.keys(minutes).sort(), [
    'concludedAt',
    'conclusion',
    'dissent',
    'findings',
    'mode',
    'openedAt',
    'participants',
    'rounds',
    'state',
    'topic',
  ]);
  assert.equal(minutes.topic, '是否给陌生人开放付费消息');
  assert.equal(minutes.mode, DISCUSSION_MODE.DISCUSS);
  assert.equal(minutes.state, DISCUSSION_STATE.CONCLUDED);
  assert.equal(minutes.openedAt, BASE_ISO);
  assert.equal(minutes.concludedAt, BASE_ISO);
  assert.deepEqual(minutes.participants.map((participant) => participant.agentId), ['agent-a', 'agent-b', 'agent-c']);
  assert.deepEqual(minutes.rounds.map((round) => round.round), [1, 2]);
  assert.deepEqual(minutes.rounds[0].speeches.map((speech) => speech.content), ['r1-a', 'r1-b']);
  assert.deepEqual(minutes.rounds[1].speeches.map((speech) => speech.content), ['r2-c']);
  assert.equal(minutes.conclusion, '按第二轮意见收敛');
  assert.deepEqual(minutes.findings, []);
  assert.deepEqual(minutes.dissent.map((item) => item.reason), ['保留意见']);
  assert.ok(Object.isFrozen(minutes));
  assert.ok(Object.isFrozen(minutes.rounds));
  assert.ok(Object.isFrozen(minutes.rounds[0]));
});

test('list：按 mode / state 过滤，按打开顺序返回冻结数组', () => {
  const { engine } = makeEngine();
  const discuss = openDiscussion(engine);
  const audit = openDiscussion(engine, { mode: DISCUSSION_MODE.AUDIT, topic: '审计' });
  openDiscussion(engine, { mode: DISCUSSION_MODE.REVIEW, topic: '评审' });
  speak(engine, discuss.id, 'agent-a');
  engine.synthesize({ discussionId: audit.id, byAgentId: 'agent-a', findings: [{ id: 'F1', detail: 'ok' }] });

  assert.equal(engine.list().length, 3);
  assert.equal(engine.list({ mode: DISCUSSION_MODE.AUDIT }).length, 1);
  assert.equal(engine.list({ mode: DISCUSSION_MODE.REVIEW }).length, 1);
  assert.equal(engine.list({ state: DISCUSSION_STATE.CONCLUDED }).length, 1);
  assert.equal(engine.list({ state: DISCUSSION_STATE.IN_ROUND }).length, 1);
  assert.equal(engine.list({ mode: DISCUSSION_MODE.AUDIT, state: DISCUSSION_STATE.OPEN }).length, 0);
  const copy = engine.list();
  assert.ok(Object.isFrozen(copy), 'list 返回冻结数组');
  assert.throws(() => copy.push('junk'), TypeError, '冻结数组不可修改');
  assert.equal(engine.list().length, 3);
});

test('参数校验：stance / content / evidence / round / reason 非法 → TypeError', () => {
  const { engine } = makeEngine();
  const opened = openDiscussion(engine);
  assert.throws(() => speak(engine, opened.id, 'agent-a', { stance: 'CHEER' }), TypeError);
  assert.throws(() => speak(engine, opened.id, 'agent-a', { content: '' }), TypeError);
  assert.throws(() => speak(engine, opened.id, 'agent-a', { evidence: 'doc' }), TypeError);
  assert.throws(() => speak(engine, opened.id, 'agent-a', { round: 0 }), TypeError);
  assert.throws(() => speak(engine, opened.id, 'agent-a', { round: 1.5 }), TypeError);
  assert.throws(() => engine.open({ mode: DISCUSSION_MODE.DISCUSS, topic: '', ownerUserId: 'user-owner', participants: [{ agentId: 'a' }, { agentId: 'b' }] }), TypeError);
  assert.throws(() => engine.open({ mode: DISCUSSION_MODE.DISCUSS, topic: 't', participants: [{ agentId: 'a' }, { agentId: 'b' }] }), TypeError);
  assert.throws(() => engine.escalate({ discussionId: opened.id, reason: 123 }), TypeError);
});

test('构造参数：clock / maxRounds / maxTurnsPerParticipantPerRound 校验', () => {
  assert.throws(() => new DiscussionEngine({ clock: 'now' }), TypeError);
  assert.throws(() => new DiscussionEngine({ maxRounds: 0 }), RangeError);
  assert.throws(() => new DiscussionEngine({ maxRounds: 1.5 }), RangeError);
  assert.throws(() => new DiscussionEngine({ maxTurnsPerParticipantPerRound: 0 }), RangeError);
  assert.throws(() => new DiscussionEngine({ maxTurnsPerParticipantPerRound: -1 }), RangeError);
  // 自定义上限生效：每人每轮 2 次
  const engine = new DiscussionEngine({ clock: makeClock(BASE_ISO), maxTurnsPerParticipantPerRound: 2 });
  const opened = openDiscussion(engine);
  speak(engine, opened.id, 'agent-a');
  speak(engine, opened.id, 'agent-a');
  assert.throws(() => speak(engine, opened.id, 'agent-a'), { code: 'TURN_LIMIT' });
});
