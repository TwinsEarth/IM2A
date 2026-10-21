#!/usr/bin/env node
/**
 * 模块职责：一条命令完成「启动 + 自检 + 打印入口」——借鉴 Xiajiao 的 npm start 即跑体验
 * 契约来源：apps/im-live/server.mjs（v1+v2 协议面）、contracts/INTERFACES.md
 *
 * 为什么需要它：本项目交付物很厚（11 篇 v1 文档 + 2 篇 v2/v3 + 8 个 proto + 多套演示），
 * 评审/新同学最容易卡在"从哪开始"。本脚本把"能不能跑起来"变成一个 10 秒内可回答的问题：
 *
 *   node scripts/quickstart.mjs            # 起服务 → 跑 9 项自检 → 打印入口 → 保持运行
 *   node scripts/quickstart.mjs --check    # 只跑自检，不起常驻服务（CI 用）
 *   node scripts/quickstart.mjs --port 8899
 *
 * 退出码：自检全过 = 0；任一失败 = 1（可直接进 CI）。
 */
import { startServer } from '../apps/im-live/server.mjs';

const argv = process.argv.slice(2);
const args = new Map();
for (let i = 0; i < argv.length; i += 1) {
  const token = argv[i];
  if (!token.startsWith('--')) continue;
  const key = token.slice(2);
  const next = argv[i + 1];
  if (next !== undefined && !next.startsWith('--')) {
    args.set(key, next);
    i += 1;
  } else {
    args.set(key, true);
  }
}

const CHECK_ONLY = args.has('check');
const PORT = Number(args.get('port') ?? (CHECK_ONLY ? 0 : 8797));
const HOST = '127.0.0.1';

const results = [];
const check = async (name, fn) => {
  try {
    const detail = await fn();
    results.push({ name, ok: true, detail: detail ?? '' });
    return true;
  } catch (error) {
    results.push({ name, ok: false, detail: String(error?.message ?? error) });
    return false;
  }
};

const json = (url) => fetch(url, { cache: 'no-store' }).then(async (res) => ({ status: res.status, body: await res.json().catch(() => ({})) }));

const app = await startServer({ port: PORT, db: ':memory:' });
const base = app.url;

console.log('\n  AgentIM · 一条命令自检');
console.log('  ─────────────────────────────────────────────────────────────');

await check('IM 客户端可访问', async () => {
  const res = await fetch(`${base}/`);
  if (res.status !== 200) throw new Error(`HTTP ${res.status}`);
  const html = await res.text();
  return html.includes('审批') ? '审批态/巡视态页面已渲染' : '已返回 HTML';
});

await check('状态接口：会话 + 待审批 + 治理自检', async () => {
  const { status, body } = await json(`${base}/api/state?user=user-lin&thread=th-lin-agent`);
  if (status !== 200) throw new Error(`HTTP ${status}`);
  if (!body.tickets || body.tickets.length === 0) throw new Error('没有待审批（种子场景应有两张）');
  if (body.checks?.passed !== body.checks?.total) throw new Error(`治理自检未全绿：${body.checks?.passed}/${body.checks?.total}`);
  return `${body.tickets.length} 张待审批 · 治理自检 ${body.checks.passed}/${body.checks.total}`;
});

await check('v2：Agent 网络（身份 + 在线状态）', async () => {
  const { status, body } = await json(`${base}/api/v2/agents`);
  if (status !== 200) throw new Error(`HTTP ${status}（v2 未加载？）`);
  const online = body.agents.filter((agent) => agent.state === 'ONLINE').length;
  if (online === 0) throw new Error('没有在线 Agent');
  if (!body.agents.every((agent) => /^[0-9a-f]{16}$/.test(agent.identity.fingerprint))) throw new Error('身份指纹缺失');
  return `${body.agents.length} 个 Agent / ${online} 在线 · 审计 ${body.audit.ok ? '完整' : '异常'}`;
});

await check('v2：频道未读隔离（协商不打扰人）', async () => {
  const before = await json(`${base}/api/v2/channels`);
  const unreadBefore = before.body.unread?.['user-lin']?.total ?? 0;
  await fetch(`${base}/api/v2/negotiation/turn`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ fromAgentId: 'agent-zhou-butler', intent: 'COUNTER', payload: { summary: 'quickstart 自检' } })
  });
  const after = await json(`${base}/api/v2/channels`);
  const unreadAfter = after.body.unread?.['user-lin']?.total ?? 0;
  if (unreadAfter !== unreadBefore) throw new Error(`协商产生了人类未读：${unreadBefore} → ${unreadAfter}`);
  return `协商后人类未读仍为 ${unreadAfter}`;
});

await check('v2：读侧可见域（陌生人只拿到存在性）', async () => {
  const { status, body } = await json(`${base}/api/v2/visibility`);
  if (status !== 200) throw new Error(`HTTP ${status}`);
  const redacted = body.matrix.find((row) => row.result.decision.decision === 'REDACT');
  if (!redacted) throw new Error('没有 REDACT 样例');
  const fields = Object.keys(redacted.result.projection.projected).sort();
  if (fields.join(',') !== 'exists,id') throw new Error(`陌生人档字段过多：${fields.join(',')}`);
  return `${body.matrix.length} 组探测 · 陌生人档仅 ${fields.join('/')}`;
});

await check('v2：签名审计两层防御', async () => {
  const content = await fetch(`${base}/api/v2/audit/tamper`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ kind: 'content' })
  }).then((res) => res.json());
  if (content.demo.after.chainOk !== false) throw new Error('改内容后哈希链未断');
  const resign = await fetch(`${base}/api/v2/audit/tamper`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ kind: 'resign' })
  }).then((res) => res.json());
  if (resign.demo.after.chainOk !== true || resign.demo.after.signaturesOk !== false) {
    throw new Error('换签名者未被签名层抓住（链应完好、签名应失败）');
  }
  return '改内容→链断；换签名者→链完好但签名失败';
});

await check('v3：审计讨论保留异议，纪要 hash 上链', async () => {
  const { status, body } = await json(`${base}/api/v2/discussions`);
  if (status !== 200) throw new Error(`HTTP ${status}`);
  const audit = body.items?.[0];
  if (!audit || audit.mode !== 'AUDIT' || audit.state !== 'CONCLUDED') throw new Error('没有已完成的 AUDIT 讨论');
  if (!(audit.findings || []).length) throw new Error('AUDIT 缺少 findings');
  if ((audit.dissent || []).length === 0) throw new Error('异议被"综合"掉了');
  const chain = await json(`${base}/api/v2/audit`);
  const synth = chain.body.entries.find((entry) => entry.action === 'SYNTHESIZE');
  if (!synth?.detail?.minutesHash) throw new Error('纪要 hash 未进签名链');
  return `${audit.rounds} 轮 / ${audit.speeches} 次发言 · findings ${audit.findings.length} · 异议 ${audit.dissent.length}（原样保留）`;
});

await check('v3：人格 SOUL 与记忆分层（可携带包已签名）', async () => {
  const { status, body } = await json(`${base}/api/v2/persona`);
  if (status !== 200) throw new Error(`HTTP ${status}`);
  if (!(body.soul?.values || []).length) throw new Error('SOUL 未设置');
  if (Object.keys(body.stages || {}).length < 3) throw new Error('记忆未按 Agent 分层');
  if (body.portableSummary?.signed !== true) throw new Error('跨平台可携带包未签名');
  return `SOUL ${body.soul.values.length} 条价值观 · ${Object.keys(body.stages).length} 个 Agent 的记忆分层`;
});

await check('审批闭环：通过 → 扣费 → 会话解锁', async () => {
  const state = await json(`${base}/api/state?user=user-lin&thread=th-lin-agent`);
  const ticket = state.body.tickets.find((item) => item.payload?.kind === 'STRANGER_INTENT');
  if (!ticket) throw new Error('没有陌生人审批单');
  const decided = await fetch(`${base}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ action: 'APPROVE' })
  }).then((res) => res.json());
  if (decided.ok !== true) throw new Error(`审批失败：${decided.error ?? 'unknown'}`);
  if (decided.state !== 'COMPLETED') throw new Error(`审批未完成：${decided.state}`);
  if (decided.result?.charged !== true) throw new Error('审批通过但没有扣费');
  // 产品级证据：陌生人会话从"锁定"变为"可发送"（关系档位已升级）
  const after = await json(`${base}/api/state?user=user-lin&thread=th-lin-zhou`);
  if (after.body.activeThread?.canSend !== true) throw new Error('审批通过后陌生人会话仍未解锁');
  return '通过 → 扣费 → 查看 → 回复 → 结算 → 关系升级、会话解锁';
});

const passed = results.filter((item) => item.ok).length;
console.log('');
for (const item of results) {
  console.log(`  ${item.ok ? '✔' : '✘'} ${item.name}${item.detail ? `\n      ${item.detail}` : ''}`);
}
console.log('  ─────────────────────────────────────────────────────────────');
console.log(`  自检结果：${passed}/${results.length}`);

if (CHECK_ONLY) {
  await app.close();
  const ok = passed === results.length;
  console.log(ok ? '\n  自检通过（--check 模式不常驻）。\n' : '\n  自检未通过。\n');
  // 不调用 process.exit：先让 server 正常关闭、事件循环自然清空，避免 Windows 上的 UV_HANDLE_CLOSING 断言
  process.exitCode = ok ? 0 : 1;
} else {
  console.log(`
  入口：
    人机混合 IM      ${base}/
    v2 Agent 网络    ${base}/v2
    状态快照         ${base}/api/state
    签名审计         ${base}/api/v2/audit

  下一步：
    node scripts/test.mjs        全量测试（约 300+ 项）
    node scripts/verify.mjs      文档 / 契约 / 代码一致性

  按 Ctrl+C 停止
`);

  if (passed !== results.length) {
    // 自检失败也保持服务运行（方便排查），但把退出码留给退出时刻
    process.exitCode = 1;
  }
}
