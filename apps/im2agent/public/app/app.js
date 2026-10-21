// 模块职责：Web 客户端视图层 —— 只依赖 logic.mjs 与 fetch；三态导航（审批/巡视/账本）
// 契约来源：contracts/INTERFACES.md §47
// 说明：客户端不把 admin token 当用户态使用；没有令牌时只读公开状态。
import { formatCents, summarizeTickets, canDecide, moderationQueue, funnelSummary } from './logic.mjs';

const $ = (selector) => document.querySelector(selector);
const view = $('#view');
const hint = $('#hint');

const initialTab = (location.hash || '').replace('#', '');
const state = {
  tab: ['approve', 'oversight', 'ledger'].includes(initialTab) ? initialTab : 'approve',
  token: sessionStorage.getItem('im2a.client.token') ?? '',
  role: null,
  admin: null,
  publicState: null,
  metrics: null,
  wallet: null,
  error: null,
  events: null
};

const esc = (value) =>
  String(value ?? '').replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));

async function api(path, options = {}) {
  const headers = { 'content-type': 'application/json', ...(options.headers ?? {}) };
  if (state.token) headers['x-admin-token'] = state.token;
  const response = await fetch(path, { ...options, headers });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw Object.assign(new Error(body.message ?? body.error ?? `HTTP ${response.status}`), { status: response.status, code: body.error });
  return body;
}

async function load() {
  state.error = null;
  try {
    state.publicState = await api('/api/state');
    if (state.token) {
      try {
        state.admin = await api('/api/admin/state');
        state.role = state.admin.role;
      } catch (error) {
        // 令牌可能是账户会话而非运维令牌：降级为公开模式，不报错刷屏
        state.admin = null;
        state.role = null;
      }
      if (state.role) {
        state.metrics = await api('/api/ops/metrics').catch(() => null);
      }
    }
  } catch (error) {
    state.error = error.message;
  }
  render();
  updateHint();
}

function updateHint() {
  if (!state.token) return (hint.textContent = '未连接：填入令牌后点"连接"');
  if (!state.role) return (hint.textContent = '已连接（只读公开状态）：该令牌不是运维令牌');
  hint.textContent = `已连接：${state.role === 'operator' ? '操作者（可审批）' : '观察者（只读）'}`;
}

function card(title, subtitle, body) {
  return `<section class="card"><h3>${esc(title)}</h3><p class="sub">${esc(subtitle)}</p>${body}</section>`;
}

function renderApprove() {
  if (state.error) return card('加载失败', state.error, `<div class="note bad">${esc(state.error)}</div>`);
  if (!state.role) return card('审批', '需要运维令牌', '<div class="empty">填入 ADMIN_TOKEN 后连接，才能看到待审批卡片。</div>');
  const tickets = state.admin?.tickets ?? [];
  const summary = summarizeTickets(state.admin ?? {});
  const list = tickets.length === 0 ? '<div class="empty">没有待办事项</div>' : tickets.map((ticket) => {
    const payload = ticket.payload ?? {};
    const title = payload.topic ?? payload.item ?? ticket.title ?? ticket.ticketType ?? '待审批';
    const canWrite = canDecide(state.role);
    const actions = ticket.state === 'PENDING' && canWrite
      ? `<div class="row" style="margin-top:8px"><button class="ok" data-approve="${esc(ticket.id)}">通过</button><button class="bad" data-reject="${esc(ticket.id)}">驳回</button></div>`
      : '';
    return `<div class="note"><b>${esc(title)}</b> <span class="tag ${ticket.state === 'PENDING' ? 'warn' : 'ok'}">${esc(ticket.state)}</span><br>
      ${payload.amountCents != null ? `金额 ${esc(formatCents(payload.amountCents))} · ` : ''}来源 ${esc(ticket.sourceAgentId ?? '')}${actions}</div>`;
  }).join('');
  return card('审批', `待审 ${summary.pending}（高优 ${summary.pendingHigh}）· 已完成 ${summary.done}`, list) + card(
    '审核队列', '被机审 FLAG 的付费消息等人审', renderQueue()
  );
}

function renderQueue() {
  const queue = moderationQueue({ moderation: state.admin?.moderation ?? [] });
  if (queue.length === 0) return '<div class="empty">队列为空</div>';
  return queue.map((item) => `<div class="note"><span class="mono">${esc(item.caseId ?? '')}</span><br>
    消息 ${esc(item.messageId ?? '')}<br>
    ${canDecide(state.role) ? `<div class="row" style="margin-top:6px"><button class="ok" data-review="${esc(item.caseId ?? '')}" data-decision="APPROVE">放行</button><button class="bad" data-review="${esc(item.caseId ?? '')}" data-decision="REJECT">拒绝</button></div>` : '<span class="sub">只读</span>'}</div>`).join('');
}

function renderOversight() {
  const agents = state.publicState?.agents ?? [];
  const funnel = funnelSummary(state.metrics ?? {});
  const funnelRows = funnel.map((item) => `<tr><td>${esc(item.stage)}</td><td class="mono">${esc(item.count)}</td><td class="mono">${(item.rate * 100).toFixed(1)}%</td></tr>`).join('');
  return card('Agent 团队', '谁在替我做事', agents.length === 0 ? '<div class="empty">暂无数据</div>' : `<table><thead><tr><th>Agent</th><th>领域</th><th>信任下限</th></tr></thead><tbody>${
    agents.map((agent) => `<tr><td class="mono">${esc(agent.agentId ?? '')}</td><td>${esc(agent.domain ?? '')}</td><td class="mono">${esc(agent.trustZoneMin ?? '')}</td></tr>`).join('')
  }</tbody></table>`) + card('漏斗', '付费消息六段转化（需运维令牌）', funnel.length === 0 ? '<div class="empty">未连接运维态或无数据</div>' : `<table><thead><tr><th>阶段</th><th>数量</th><th>转化</th></tr></thead><tbody>${funnelRows}</tbody></table>`);
}

function renderLedger() {
  const messages = state.publicState?.messages ?? [];
  const rows = messages.map((message) => `<tr><td>${esc(message.topic ?? '')}</td><td><span class="tag">${esc(message.state ?? '')}</span></td><td class="mono">${esc(formatCents(message.amountCents))}</td></tr>`).join('');
  return card('账本 / 付费消息', '金额由服务端账本决定，客户端只展示', messages.length === 0 ? '<div class="empty">暂无付费消息</div>' : `<table><thead><tr><th>事项</th><th>状态</th><th>金额</th></tr></thead><tbody>${rows}</tbody></table>`) +
    card('退款说明', '未回复可自助退；已回复需走争议人工', '<div class="note">退款窗口由服务端判定（§37/§39）；界面不代用户做判断。</div>');
}

function render() {
  const renderers = { approve: renderApprove, oversight: renderOversight, ledger: renderLedger };
  const renderer = renderers[state.tab] ?? renderApprove;
  view.innerHTML = renderer();
  document.querySelectorAll('.tabs button').forEach((button) => {
    button.classList.toggle('active', button.dataset.tab === state.tab);
  });
}

async function decide(ticketId, action) {
  try {
    await api('/api/approval/decide', { method: 'POST', body: JSON.stringify({ ticket_id: ticketId, action }) });
    await load();
  } catch (error) {
    state.error = error.message;
    render();
  }
}

async function review(caseId, decision) {
  try {
    await api('/api/moderation/review', { method: 'POST', body: JSON.stringify({ caseId, decision }) });
    await load();
  } catch (error) {
    state.error = error.message;
    render();
  }
}

document.addEventListener('click', (event) => {
  const approve = event.target.closest('[data-approve]');
  if (approve) return decide(approve.dataset.approve, 'APPROVE');
  const reject = event.target.closest('[data-reject]');
  if (reject) return decide(reject.dataset.reject, 'REJECT');
  const reviewButton = event.target.closest('[data-review]');
  if (reviewButton) return review(reviewButton.dataset.review, reviewButton.dataset.decision);
  const tab = event.target.closest('.tabs button');
  if (tab) {
    state.tab = tab.dataset.tab;
    location.hash = state.tab; // hash 路由：刷新/分享后可回到同一屏
    render();
  }
});

$('#connect').addEventListener('click', async () => {
  state.token = $('#token').value.trim();
  sessionStorage.setItem('im2a.client.token', state.token);
  await load();
  // SSE：可用时自动刷新（不可用则退化为手动刷新，不做假承诺）
  if (state.events === null && window.EventSource) {
    try {
      state.events = new EventSource('/api/events');
      state.events.onmessage = () => load();
    } catch {
      state.events = null;
    }
  }
});

$('#token').value = state.token;
load();
