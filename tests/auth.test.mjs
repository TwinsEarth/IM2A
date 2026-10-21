/**
 * 模块职责：登录鉴权测试 —— 口令校验 / 会话签名与过期 / 未认证拒绝 / 防冒充 / 越权审批拒绝
 * 契约来源：apps/im-live/auth.mjs 头注释
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { buildUsers, createAuth, readCookie } from '../apps/im-live/auth.mjs';
import { startServer } from '../apps/im-live/server.mjs';

const USERS = buildUsers();

function makeAuth(overrides = {}) {
  return createAuth({ enabled: true, users: USERS, secret: 'test-secret', ...overrides });
}

const cookieOf = (response) => (response.headers.get('set-cookie') ?? '').split(';')[0];

test('口令：正确口令通过，错误口令/未知用户拒绝；口令原文不在账号对象里', () => {
  const auth = makeAuth();
  assert.equal(auth.verifyPassword('user-lin', 'demo-lin-2026'), true);
  assert.equal(auth.verifyPassword('user-lin', 'wrong'), false);
  assert.equal(auth.verifyPassword('nobody', 'demo-lin-2026'), false);
  const user = auth.users.get('user-lin');
  assert.equal('demoPassword' in user && user.demoPassword, true);
  assert.equal('password' in user, false);
  assert.equal(Buffer.isBuffer(user.passwordHash), true);
});

test('会话：签发/解析通过；篡改签名与过期都被具名拒绝', () => {
  let nowMs = 1_700_000_000_000;
  const auth = makeAuth({ now: () => nowMs });
  const session = auth.issue('user-lin');
  const parsed = auth.parse(`im_session=${encodeURIComponent(session.token)}`);
  assert.equal(parsed.ok, true);
  assert.equal(parsed.user.userId, 'user-lin');
  assert.equal(parsed.user.name, '林总');

  const tampered = session.token.replace(/.$/, (c) => (c === 'A' ? 'B' : 'A'));
  assert.equal(auth.parse(`im_session=${encodeURIComponent(tampered)}`).reason, 'BAD_SIGNATURE');
  assert.equal(auth.parse('other=1').reason, 'NO_SESSION');
  nowMs += 13 * 3600e3;
  assert.equal(auth.parse(`im_session=${encodeURIComponent(session.token)}`).reason, 'EXPIRED');
});

test('Cookie 工具：从多个 cookie 中精确读取，不误匹配前缀相同的键', () => {
  assert.equal(readCookie('a=1; im_session=abc.123.sig; b=2'), 'abc.123.sig');
  assert.equal(readCookie('im_session_extra=no; x=y'), null);
});

test('服务集成：未认证 401；登录后按会话身份取数；body 里的 userId 不能冒充', async () => {
  const auth = makeAuth();
  const app = await startServer({ port: 0, db: ':memory:', auth });
  try {
    const anon = await fetch(`${app.url}/api/state`);
    assert.equal(anon.status, 401);
    assert.equal((await anon.json()).error, 'UNAUTHENTICATED');

    const page = await fetch(`${app.url}/`);
    assert.equal(page.status, 200, '登录页/静态页本身公开');

    const sessionInfo = await (await fetch(`${app.url}/api/session`)).json();
    assert.equal(sessionInfo.authEnabled, true);
    assert.equal(sessionInfo.users.length, 3);

    const wrong = await fetch(`${app.url}/api/login`, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ userId: 'user-lin', password: 'nope' })
    });
    assert.equal(wrong.status, 401);
    assert.equal((await wrong.json()).error, 'WRONG_CREDENTIALS');

    const login = await fetch(`${app.url}/api/login`, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ userId: 'user-lin', password: 'demo-lin-2026' })
    });
    assert.equal(login.status, 200);
    const cookie = cookieOf(login);
    assert.match(cookie, /^im_session=/);

    const state = await fetch(`${app.url}/api/state?user=user-zhou`, { headers: { cookie } });
    assert.equal(state.status, 200);
    const body = await state.json();
    assert.equal(body.me.userId, 'user-lin', 'query 里的 userId 被忽略，身份来自会话');
    assert.equal(body.auth.enabled, true);
    assert.equal(body.auth.user.userId, 'user-lin');

    const spoof = await fetch(`${app.url}/api/message`, {
      method: 'POST', headers: { 'content-type': 'application/json', cookie },
      body: JSON.stringify({ userId: 'user-zhou', threadId: 'th-lin-mom', text: '冒充周先生' })
    });
    assert.equal(spoof.status, 200);
    const sent = await spoof.json();
    assert.equal(sent.message.from, 'user-lin', '消息必须以会话身份发出');
  } finally {
    await app.close();
  }
});

test('服务集成：别人的审批单不能被裁决（403）；登出后会话失效', async () => {
  const auth = makeAuth();
  const app = await startServer({ port: 0, db: ':memory:', auth });
  const login = async (userId, password) => {
    const res = await fetch(`${app.url}/api/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ userId, password }) });
    return cookieOf(res);
  };
  try {
    const lin = await login('user-lin', 'demo-lin-2026');
    const zhou = await login('user-zhou', 'demo-zhou-2026');
    const state = await (await fetch(`${app.url}/api/state`, { headers: { cookie: lin } })).json();
    const ticket = state.tickets[0];
    assert.ok(ticket, '林总应有待审批单');

    const forbidden = await fetch(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST', headers: { 'content-type': 'application/json', cookie: zhou },
      body: JSON.stringify({ action: 'APPROVE' })
    });
    assert.equal(forbidden.status, 403);
    assert.equal((await forbidden.json()).error, 'FORBIDDEN');

    const approved = await fetch(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST', headers: { 'content-type': 'application/json', cookie: lin },
      body: JSON.stringify({ action: 'APPROVE' })
    });
    assert.equal(approved.status, 200);

    const logout = await fetch(`${app.url}/api/logout`, { method: 'POST', headers: { cookie: lin } });
    assert.equal(logout.status, 200);
    assert.match(logout.headers.get('set-cookie') ?? '', /Max-Age=0/, '登出通过清除 Cookie 生效（无状态会话）');
    const after = await fetch(`${app.url}/api/state`);
    assert.equal(after.status, 401, '没有会话 Cookie 即 401');
  } finally {
    await app.close();
  }
});
