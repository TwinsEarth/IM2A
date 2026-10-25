package api

import (
	"context"
	"net/http"
)

// AdminMux v1.7.6：运营后台总入口——管理仪表盘真实数据聚合（只读）。
// 路由在网关侧用独立管理员 Token 保护（IM2A_ADMIN_TOKEN，缺省回退 GATEWAY_TOKEN）。
func (d *Deps) AdminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/stats", d.adminStats)
	mux.HandleFunc("GET /v1/admin/timeseries", d.adminTimeseries)
	mux.HandleFunc("GET /v1/admin/payments", d.adminPayments)
	mux.HandleFunc("GET /v1/admin/agents", d.adminAgents)
	d.adminSupportRegister(mux)
	d.auditViewRegister(mux)
	d.moderationAdminRegister(mux)
	d.broadcastAdminRegister(mux)
	return mux
}

func (d *Deps) adminStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats := map[string]any{}

	// 用户 / IM 账号 / Agent 注册
	stats["users_total"] = d.countQ(ctx, `SELECT count(*) FROM users`)
	stats["im_accounts"] = d.countQ(ctx, `SELECT count(*) FROM account_identities`)
	stats["agent_capabilities"] = d.countQ(ctx, `SELECT count(*) FROM agent_capabilities`)

	// 付费（陌生人付费注意力）
	stats["paid_total_cents"] = d.sumQ(ctx, `SELECT COALESCE(sum(amount_cents),0) FROM paid_message_ledger WHERE status=1`)
	stats["paid_charged"] = d.countQ(ctx, `SELECT count(*) FROM paid_message_ledger WHERE status=1`)
	stats["paid_refunded"] = d.countQ(ctx, `SELECT count(*) FROM paid_message_ledger WHERE status=2`)

	// 审批 / 协商
	stats["approval_total"] = d.countQ(ctx, `SELECT count(*) FROM approval_tickets`)
	stats["approval_by_state"] = d.groupCount(ctx, `SELECT state, count(*) FROM approval_tickets GROUP BY state`)
	stats["negotiation_total"] = d.countQ(ctx, `SELECT count(*) FROM negotiation_sessions`)
	stats["negotiation_by_state"] = d.groupCount(ctx, `SELECT state, count(*) FROM negotiation_sessions GROUP BY state`)

	// 模型调用 / 出端 / 内容审核（来自审计事件流）
	stats["model_calls"] = d.countQ(ctx, `SELECT count(*) FROM audit_events WHERE action='model_call'`)
	stats["edge_egress"] = d.countQ(ctx, `SELECT count(*) FROM audit_events WHERE action='edge_egress'`)
	stats["content_audited"] = d.countQ(ctx, `SELECT count(*) FROM audit_events WHERE action='CONTENT_ALLOWED' OR action='CONTENT_BLOCKED'`)
	stats["audit_total"] = d.countQ(ctx, `SELECT count(*) FROM audit_events`)

	// IM 人类消息通道
	stats["im_messages"] = d.countQ(ctx, `SELECT count(*) FROM im_messages`)
	stats["im_webhook_events"] = d.countQ(ctx, `SELECT count(*) FROM im_webhook_events`)
	stats["im_relations"] = d.countQ(ctx, `SELECT count(*) FROM im_relations`)
	stats["push_pending"] = d.countQ(ctx, `SELECT count(*) FROM im_push_tasks WHERE status='pending'`)

	// 客服工单
	stats["tickets"] = d.countQ(ctx, `SELECT count(*) FROM support_tickets`)

	writeJSON(w, http.StatusOK, map[string]any{
		"service": "IM2A Admin", "status": "ok", "stats": stats,
	})
}

// v1.7.7：调用量 / 消息 / 付费 / 审批 按天时间序列（默认近 7 天，最多 30）。
func (d *Deps) adminTimeseries(w http.ResponseWriter, r *http.Request) {
	days := 7
	if n := atoiDefault(r.URL.Query().Get("days"), 7); n >= 1 && n <= 30 {
		days = n
	}
	ctx := r.Context()
	type dayMetrics struct {
		Date          string `json:"date"`
		ModelCalls    int    `json:"model_calls"`
		IMMessages    int    `json:"im_messages"`
		WebhookEvents int    `json:"webhook_events"`
		Paid          int    `json:"paid_charged"`
		Approvals     int    `json:"approvals"`
	}
	series := map[string]*dayMetrics{}
	fill := func(q, key string) {
		rows, err := d.DB.QueryContext(ctx, q, days)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var dt string
			var n int
			if err := rows.Scan(&dt, &n); err != nil {
				continue
			}
			m, ok := series[dt]
			if !ok {
				m = &dayMetrics{Date: dt}
				series[dt] = m
			}
			switch key {
			case "model":
				m.ModelCalls = n
			case "im":
				m.IMMessages = n
			case "webhook":
				m.WebhookEvents = n
			case "paid":
				m.Paid = n
			case "approvals":
				m.Approvals = n
			}
		}
	}
	fill(`SELECT to_char(occurred_at,'YYYY-MM-DD') d, count(*) FROM audit_events WHERE action='model_call' AND occurred_at >= now()-make_interval(days=>$1::int) GROUP BY d`, "model")
	fill(`SELECT to_char(created_at,'YYYY-MM-DD') d, count(*) FROM im_messages WHERE created_at >= now()-make_interval(days=>$1::int) GROUP BY d`, "im")
	fill(`SELECT to_char(received_at,'YYYY-MM-DD') d, count(*) FROM im_webhook_events WHERE received_at >= now()-make_interval(days=>$1::int) GROUP BY d`, "webhook")
	fill(`SELECT to_char(charged_at,'YYYY-MM-DD') d, count(*) FROM paid_message_ledger WHERE status=1 AND charged_at >= now()-make_interval(days=>$1::int) GROUP BY d`, "paid")
	fill(`SELECT to_char(created_at,'YYYY-MM-DD') d, count(*) FROM approval_tickets WHERE created_at >= now()-make_interval(days=>$1::int) GROUP BY d`, "approvals")

	// 补齐空日期，保证连续
	out := []dayMetrics{}
	for i := days - 1; i >= 0; i-- {
		var ds string
		_ = d.DB.QueryRowContext(ctx, `SELECT to_char(now()-make_interval(days=>$1::int),'YYYY-MM-DD')`, i).Scan(&ds)
		if m, ok := series[ds]; ok {
			out = append(out, *m)
		} else {
			out = append(out, dayMetrics{Date: ds})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "days": days, "series": out})
}

// v1.7.7：付费流水明细（真实账本）。
func (d *Deps) adminPayments(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := d.DB.QueryContext(r.Context(), `SELECT id, message_id, sender_id, receiver_id, billing_mode, amount_cents, status, created_at, COALESCE(charged_at::text,'') FROM paid_message_ledger ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		ID          int64  `json:"id"`
		MessageID   string `json:"message_id"`
		Sender      int64  `json:"sender_id"`
		Receiver    int64  `json:"receiver_id"`
		BillingMode int    `json:"billing_mode"`
		AmountCents int64  `json:"amount_cents"`
		Status      int    `json:"status"`
		CreatedAt   string `json:"created_at"`
		ChargedAt   string `json:"charged_at"`
	}
	list := []row{}
	for rows.Next() {
		var x row
		var created string
		if err := rows.Scan(&x.ID, &x.MessageID, &x.Sender, &x.Receiver, &x.BillingMode, &x.AmountCents, &x.Status, &created, &x.ChargedAt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		x.CreatedAt = created
		list = append(list, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "payments": list})
}

// v1.7.7：Agent 注册明细（真实 agent_capabilities）。
func (d *Deps) adminAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := d.DB.QueryContext(r.Context(), `SELECT agent_id, owner_id, kind, trust_level_min, budget_cents_per_day, enabled, COALESCE(updated_at::text,'') FROM agent_capabilities ORDER BY updated_at DESC NULLS LAST LIMIT 100`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		AgentID           string `json:"agent_id"`
		OwnerID           int64  `json:"owner_id"`
		Kind              string `json:"kind"`
		TrustLevelMin     int    `json:"trust_level_min"`
		BudgetCentsPerDay int64  `json:"budget_cents_per_day"`
		Enabled           bool   `json:"enabled"`
		UpdatedAt         string `json:"updated_at"`
	}
	list := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.AgentID, &x.OwnerID, &x.Kind, &x.TrustLevelMin, &x.BudgetCentsPerDay, &x.Enabled, &x.UpdatedAt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		list = append(list, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agents": list})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func (d *Deps) countQ(r context.Context, q string) int {
	var n int
	_ = d.DB.QueryRowContext(r, q).Scan(&n)
	return n
}
func (d *Deps) sumQ(r context.Context, q string) int64 {
	var n int64
	_ = d.DB.QueryRowContext(r, q).Scan(&n)
	return n
}
func (d *Deps) groupCount(r context.Context, q string) map[string]int {
	out := map[string]int{}
	rows, err := d.DB.QueryContext(r, q)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err == nil {
			out[k] = n
		}
	}
	return out
}

// AdminPage 运营后台单页仪表盘（深色，自包含；数据由 /v1/admin/stats 带管理员 Token 拉取）。
func (d *Deps) AdminPage() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminHTML))
	})
}

const adminHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>IM2A 运营后台</title>
<style>
:root{--bg:#0b0f14;--card:#131a22;--line:#222c38;--fg:#e6edf3;--mut:#8b98a5;--acc:#3b82f6;--ok:#22c55e;--warn:#f59e0b;--bad:#ef4444}
*{box-sizing:border-box;margin:0;padding:0}
body{background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;padding:24px}
h1{font-size:20px;margin-bottom:4px}.sub{color:var(--mut);margin-bottom:18px}
.bar{display:flex;gap:10px;align-items:center;margin-bottom:22px}
input{background:var(--card);border:1px solid var(--line);color:var(--fg);border-radius:8px;padding:9px 12px;width:320px}
button{background:var(--acc);border:0;color:#fff;border-radius:8px;padding:9px 16px;cursor:pointer;font-weight:600}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:14px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px}
.card .k{color:var(--mut);font-size:12px;margin-bottom:8px}.card .v{font-size:26px;font-weight:700}
.sec{margin-top:26px}.sec h2{font-size:15px;color:var(--mut);margin-bottom:10px;font-weight:600}
.chips{display:flex;flex-wrap:wrap;gap:8px}
.chip{background:var(--card);border:1px solid var(--line);border-radius:999px;padding:6px 12px;font-size:13px}
.chip b{color:var(--acc)}
.table{width:100%;border-collapse:collapse;font-size:13px;margin-top:6px}
.table th,.table td{border-bottom:1px solid var(--line);padding:8px 10px;text-align:left;white-space:nowrap}
.table th{color:var(--mut);font-weight:600}.pill{font-size:11px;padding:2px 8px;border-radius:999px;border:1px solid var(--line)}
.bars{display:flex;align-items:flex-end;gap:10px;height:160px;margin-top:10px}
.bar-col{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:flex-end;height:100%}
.bar-stack{width:60%;display:flex;flex-direction:column-reverse;border-radius:4px 4px 0 0;overflow:hidden}
.bar-lbl{font-size:10px;color:var(--mut);margin-top:6px}
.err{color:var(--bad)}.ok{color:var(--ok)}
.ticket{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 14px;margin-bottom:10px}
.ticket .th{display:flex;justify-content:space-between;gap:8px;align-items:center;margin-bottom:6px}
.ticket .msg{background:#0d131a;border-radius:8px;padding:6px 10px;margin:4px 0;font-size:13px}
.ticket .who{color:var(--mut);font-size:11px;margin-right:6px}
.ticket textarea{width:100%;background:#0d131a;border:1px solid var(--line);color:var(--fg);border-radius:8px;padding:8px;margin-top:6px;font:inherit}
.tbtn{background:var(--card);border:1px solid var(--line);color:var(--fg);border-radius:7px;padding:5px 12px;font-size:12px;margin:6px 6px 0 0;cursor:pointer}
.tbtn.on{background:var(--acc);border-color:var(--acc);color:#fff}
.amsg{background:#0d131a;border:1px solid var(--line);border-radius:8px;padding:7px 10px;margin:5px 0;font-size:13px;display:flex;flex-wrap:wrap;gap:8px;align-items:center}
.afin{background:#0d131a;border:1px solid var(--line);color:var(--fg);border-radius:7px;padding:6px 8px;margin-right:6px;font:inherit;font-size:13px}
.step{border-left:2px solid var(--acc);padding:4px 0 4px 12px;margin:4px 0;font-size:13px}
.dok{color:var(--ok)}.dbad{color:var(--bad)}
</style></head><body>
<h1>IM2A 运营后台</h1>
<div class="sub">人机混合 IM · 实时运营仪表盘 · 数据来自生产 PG 实时聚合 · 10 秒自动刷新</div>
<div class="bar">
  <input id="tok" type="password" placeholder="管理员 Token（IM2A_ADMIN_TOKEN）">
  <button onclick="loadAll()">刷新</button>
  <span id="status" class="sub"></span>
</div>
<div class="grid" id="grid"></div>
<div class="sec"><h2>审批状态分布</h2><div class="chips" id="appr"></div></div>
<div class="sec"><h2>协商状态分布</h2><div class="chips" id="nego"></div></div>
<div class="sec"><h2>近 7 天趋势</h2><div class="bars" id="chart"></div>
<div style="margin-top:8px;display:flex;gap:14px;font-size:12px;color:var(--mut)">
<span style="color:#3b82f6">■ 模型调用</span><span style="color:#22c55e">■ IM消息</span><span style="color:#f59e0b">■ Webhook</span><span style="color:#a855f7">■ 付费</span><span style="color:#ef4444">■ 审批</span></div></div>
<div class="sec"><h2>付费流水（最新 50 笔）</h2><table class="table"><thead><tr><th>ID</th><th>发送</th><th>接收</th><th>计费模式</th><th>金额(分)</th><th>状态</th><th>时间</th></tr></thead><tbody id="payrows"></tbody></table></div>
<div class="sec"><h2>Agent 注册（最新 100）</h2><table class="table"><thead><tr><th>AgentID</th><th>Owner</th><th>类型</th><th>最低信任</th><th>日预算(分)</th><th>启用</th></tr></thead><tbody id="agentrows"></tbody></table></div>
<div class="sec"><h2>客服工单</h2>
<div style="margin-bottom:10px">
 <button class="tbtn on" data-f="0" onclick="setFilter(0)">全部</button>
 <button class="tbtn" data-f="1" onclick="setFilter(1)">待处理</button>
 <button class="tbtn" data-f="2" onclick="setFilter(2)">处理中</button>
 <button class="tbtn" data-f="3" onclick="setFilter(3)">已解决</button>
 <button class="tbtn" data-f="4" onclick="setFilter(4)">已关闭</button>
</div>
<div id="ticketlist"></div></div>
<div class="sec"><h2>审计与门控决策回放</h2>
<div id="auditagg" class="sub" style="margin-bottom:10px"></div>
<div style="margin-bottom:10px">
 <input class="afin" id="af_action" placeholder="action（如 ticket_decision）">
 <input class="afin" id="af_decision" placeholder="decision（如 approved / block）">
 <input class="afin" id="af_trace" placeholder="trace_id">
 <button class="tbtn" onclick="loadAudit()">查询</button>
</div>
<div id="replay"></div>
<div id="auditlist"></div></div>
<div class="sec"><h2>内容审核</h2>
<div id="modrules" style="margin-bottom:8px"></div>
<div style="margin:8px 0;display:flex;flex-wrap:wrap;gap:6px">
 <input class="afin" id="nr_code" placeholder="规则 code（唯一）" style="width:150px">
 <input class="afin" id="nr_pat" placeholder="匹配词/正则" style="width:150px">
 <select class="afin" id="nr_type"><option value="1">关键词</option><option value="2">手机号</option><option value="3">URL</option><option value="4">正则</option></select>
 <select class="afin" id="nr_act"><option value="1">拦截</option><option value="2">转人工</option></select>
 <button class="tbtn" onclick="addRule()">新增规则</button>
</div>
<div style="margin:8px 0">
 <button class="tbtn" data-m="0" onclick="loadModQueue(0)">待审</button>
 <button class="tbtn" data-m="1" onclick="loadModQueue(1)">放行</button>
 <button class="tbtn" data-m="2" onclick="loadModQueue(2)">拦截</button>
 <button class="tbtn" data-m="3" onclick="loadModQueue(3)">误报</button>
</div>
<div id="modqueue"></div></div>
<script>
var CARDS=[
 ["users_total","用户总数"],["im_accounts","IM 账号"],["agent_capabilities","Agent 能力"],
 ["paid_total_cents","付费收入(分)"],["paid_charged","已计费笔数"],["paid_refunded","已退款"],
 ["approval_total","审批单总数"],["negotiation_total","协商会话"],
 ["model_calls","模型调用"],["edge_egress","出端事件"],["content_audited","内容审核"],
 ["im_messages","IM 消息"],["im_webhook_events","Webhook 事件"],["im_relations","关系链"],
 ["push_pending","待推送"],["tickets","客服工单"],["audit_total","审计事件总数"]
];
function jget(path){
 var tok=document.getElementById("tok").value.trim();
 return fetch(path,{headers:{Authorization:"Bearer "+tok}}).then(function(r){if(!r.ok)throw new Error(path+" HTTP "+r.status);return r.json();});
}
function load(){
 var tok=document.getElementById("tok").value.trim();
 localStorage.setItem("im2a_admin_tok",tok);
 var st=document.getElementById("status");
 return jget("/v1/admin/stats").then(function(j){
  var s=j.stats||{};
  st.className="ok";st.textContent="更新于 "+new Date().toLocaleTimeString();
  document.getElementById("grid").innerHTML=CARDS.map(function(c){
   var v=s[c[0]]||0; if(c[0]==="paid_total_cents") v=(v/100).toFixed(2)+" 元";
   return '<div class="card"><div class="k">'+c[1]+'</div><div class="v">'+v+'</div></div>';
  }).join("");
  chips("appr",s.approval_by_state);chips("nego",s.negotiation_by_state);
 }).catch(function(e){st.className="err";st.textContent=String(e);});
}
function chips(id,obj){obj=obj||{};
 document.getElementById(id).innerHTML=Object.keys(obj).map(function(k){return '<span class="chip">'+k+' <b>'+obj[k]+'</b></span>';}).join("")||'<span class="chip">无</span>';
}
function seg(v,col,max){var h=Math.round((v/max)*100);if(h<=0)return "";return '<div style="height:'+h+'%;background:'+col+'"></div>';}
function loadDetails(){
 return Promise.all([jget("/v1/admin/timeseries?days=7"),jget("/v1/admin/payments?limit=50"),jget("/v1/admin/agents")]).then(function(r){
  var s=r[0].series||[];var max=1;
  s.forEach(function(d){[d.model_calls,d.im_messages,d.webhook_events,d.paid_charged,d.approvals].forEach(function(v){if(v>max)max=v;});});
  document.getElementById("chart").innerHTML=s.map(function(d){
   return '<div class="bar-col"><div class="bar-stack">'+seg(d.approvals,"#ef4444",max)+seg(d.paid_charged,"#a855f7",max)+seg(d.webhook_events,"#f59e0b",max)+seg(d.im_messages,"#22c55e",max)+seg(d.model_calls,"#3b82f6",max)+'</div><div class="bar-lbl">'+d.date.slice(5)+'</div></div>';
  }).join("");
  var mode={1:"发送即扣费",2:"回复才扣费"};var stt={1:"已计费",2:"已退款"};
  document.getElementById("payrows").innerHTML=(r[1].payments||[]).map(function(p){
   return '<tr><td>'+p.id+'</td><td>'+p.sender_id+'</td><td>'+p.receiver_id+'</td><td>'+(mode[p.billing_mode]||p.billing_mode)+'</td><td>'+p.amount_cents+'</td><td>'+(stt[p.status]||p.status)+'</td><td>'+(p.created_at||"").slice(0,16)+'</td></tr>';
  }).join("")||'<tr><td colspan=7 class="sub">暂无</td></tr>';
  document.getElementById("agentrows").innerHTML=(r[2].agents||[]).map(function(x){
   return '<tr><td>'+x.agent_id+'</td><td>'+x.owner_id+'</td><td>'+x.kind+'</td><td>'+x.trust_level_min+'</td><td>'+x.budget_cents_per_day+'</td><td><span class="pill" style="color:'+(x.enabled?'var(--ok)':'var(--bad)')+'">'+(x.enabled?'启用':'停用')+'</span></td></tr>';
  }).join("")||'<tr><td colspan=6 class="sub">暂无</td></tr>';
 });
}

var TS={1:"待处理",2:"处理中",3:"已解决",4:"已关闭"};
var tFilter=0;
function jpost(path,body){
 var tok=document.getElementById("tok").value.trim();
 return fetch(path,{method:"POST",headers:{Authorization:"Bearer "+tok,"Content-Type":"application/json"},body:JSON.stringify(body)}).then(function(r){return r.json();});
}
function esc(s){return String(s==null?"":s).replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;").replace(/"/g,"&quot;").replace(/'/g,"&#39;");}
function setFilter(f){tFilter=f;
 var btns=document.querySelectorAll("#ticketfilter,.tbtn");
 Array.prototype.forEach.call(document.querySelectorAll('[data-f]'),function(b){b.className="tbtn"+(Number(b.getAttribute("data-f"))===f?" on":"");});
 loadTickets();
}
function loadTickets(){
 return jget("/v1/admin/tickets"+(tFilter?("?status="+tFilter):"")).then(function(r){
  document.getElementById("ticketlist").innerHTML=(r.tickets||[]).map(function(t){
   var msgs='<div class="msg"><span class="who">用户</span>'+esc(t.content)+'</div>';
   (t.replies||[]).forEach(function(x){msgs+='<div class="msg"><span class="who">'+(x.author_type===2?'客服':'用户')+' '+(x.author||"")+'</span>'+esc(x.content)+'</div>';});
   return '<div class="ticket"><div class="th"><div><b>'+t.ticket_no+'</b> <span class="pill">'+(TS[t.status]||t.status)+'</span> <span class="who">'+esc(t.category)+'</span></div><div class="who">'+esc(t.assignee||"未分配")+'</div></div>'+
    '<div><b>'+esc(t.subject)+'</b><div class="who">联系方式 '+esc(t.contact||"无")+'</div></div>'+msgs+
    '<textarea id="tr_'+t.id+'" placeholder="客服回复内容…"></textarea>'+
    '<button class="tbtn" onclick="replyTicket('+t.id+',2)">回复(处理中)</button>'+
    '<button class="tbtn" onclick="replyTicket('+t.id+',3)">回复并解决</button>'+
    '<button class="tbtn" onclick="closeTicket('+t.id+')">关闭</button></div>';
  }).join("")||'<div class="sub">无工单</div>';
 }).catch(function(){});
}


var MODTYPE={1:"关键词",2:"手机号",3:"URL",4:"正则"};
var MODMANUAL=["未审","放行","拦截","误报"];
var MODAUTO=["待审","自动放行","自动拦截"];
function loadModRules(){
 jget("/v1/admin/moderation/rules").then(function(r){
  document.getElementById("modrules").innerHTML=(r.rules||[]).map(function(x){
   return '<span class="pill">'+esc(x.code)+' '+MODTYPE[x.rule_type]+' '+(x.pattern?esc(x.pattern):"(内置)")+' '+(x.action===1?"拦截":"转人工")+' <b style="color:'+(x.enabled?"var(--ok)":"var(--bad)")+'">'+(x.enabled?"启用":"停用")+'</b></span> <button class="tbtn" onclick="toggleRule('+x.id+','+(x.enabled?0:1)+')">'+(x.enabled?"停用":"启用")+'</button> ';
  }).join("");
 }).catch(function(){});
}
function addRule(){
 var body={code:document.getElementById("nr_code").value.trim(),pattern:document.getElementById("nr_pat").value.trim(),rule_type:parseInt(document.getElementById("nr_type").value),action:parseInt(document.getElementById("nr_act").value)};
 if(!body.code){alert("请填写规则 code");return;}
 jpost("/v1/admin/moderation/rules",body).then(function(){document.getElementById("nr_code").value="";document.getElementById("nr_pat").value="";loadModRules();}).catch(function(e){alert(String(e));});
}
function toggleRule(id,en){jpost("/v1/admin/moderation/rules/"+id+"/toggle",{enabled:en}).then(loadModRules);}
function loadModQueue(st){
 Array.prototype.forEach.call(document.querySelectorAll("[data-m]"),function(b){b.className="tbtn"+(Number(b.getAttribute("data-m"))===st?" on":"");});
 jget("/v1/admin/moderation/queue?status="+st+"&limit=100").then(function(r){
  document.getElementById("modqueue").innerHTML=(r.items||[]).map(function(q){
   return '<div class="ticket"><div class="th"><div><b>['+MODAUTO[q.auto_decision]+']</b> '+esc(q.content)+'</div><div class="who">'+(q.created_at||"").slice(0,19)+'</div></div>'+
    '<div class="sub">ref '+esc(q.ref)+' · 命中 '+esc(JSON.stringify(q.hit_rules))+' · 人工 '+MODMANUAL[q.manual_status]+' '+(q.reviewer?("· "+esc(q.reviewer)):"")+'</div>'+
    '<div style="margin-top:6px"><button class="tbtn" onclick="modReview('+q.id+',1)">放行</button> <button class="tbtn" onclick="modReview('+q.id+',2)">拦截</button> <button class="tbtn" onclick="modReview('+q.id+',3)">误报</button></div></div>';
  }).join("")||'<div class="sub">无</div>';
 }).catch(function(){});
}
function modReview(id,dec){
 var d=["allow","block","false_positive"][dec-1];
 jpost("/v1/admin/moderation/"+id+"/review",{decision:d,reviewer:"admin"}).then(function(){
  var on=document.querySelector("[data-m].on"); loadModQueue(on?Number(on.getAttribute("data-m")):0);
 });
}

function dclass(d){return (d==="approved"||d==="allow"||d==="ok")?"dok":((d==="rejected"||d==="block"||d==="blocked")?"dbad":"");}
function loadAuditAgg(){
 return jget("/v1/admin/audit/aggregate").then(function(r){
  var pills=(r.decisions||[]).map(function(x){return '<span class="pill">'+esc(x.k||"未决")+' '+x.v+'</span>';}).join(" ");
  document.getElementById("auditagg").innerHTML="事件总数 <b>"+r.total+"</b>　"+pills;
 }).catch(function(){});
}
function loadAudit(){
 var qs=[];
 var av=document.getElementById("af_action").value.trim();
 var dv=document.getElementById("af_decision").value.trim();
 var tv=document.getElementById("af_trace").value.trim();
 if(av)qs.push("action="+encodeURIComponent(av));
 if(dv)qs.push("decision="+encodeURIComponent(dv));
 if(tv)qs.push("trace_id="+encodeURIComponent(tv));
 return jget("/v1/admin/audit/stream?"+qs.join("&")+"&limit=200").then(function(r){
  document.getElementById("auditlist").innerHTML=(r.events||[]).map(function(e){
   return '<div class="amsg"><span class="who">'+esc(e.occurred_at).slice(0,19)+'</span><b>'+esc(e.action)+'</b>'+
    '<span class="pill '+dclass(e.decision)+'">'+esc(e.decision||"—")+'</span>'+
    '<span class="who">'+esc(e.agent_id||"")+'</span>'+
    '<span class="who">trace: '+esc(e.trace_id||"").slice(0,28)+'</span>'+
    (e.trace_id?'<button class="tbtn" onclick="replayTrace(\''+esc(e.trace_id)+'\')">回放</button>':"")+'</div>';
  }).join("")||'<div class="sub">无事件</div>';
 }).catch(function(){});
}
function replayTrace(tr){
 jget("/v1/admin/audit/trace/"+encodeURIComponent(tr)).then(function(r){
  var html='<div class="ticket"><div class="th"><div><b>决策回放</b> <span class="pill">'+esc(r.trace_id)+'</span></div><div class="who">'+r.steps+' 步</div></div>';
  html+=(r.events||[]).map(function(e,i){
   return '<div class="step"><span class="who">'+(i+1)+' · '+esc(e.occurred_at).slice(11,19)+'</span><b>'+esc(e.action)+'</b> <span class="'+dclass(e.decision)+'">'+esc(e.decision||"—")+'</span><div class="who">'+esc(JSON.stringify(e.detail))+'</div></div>';
  }).join("");
  html+='</div>';
  document.getElementById("replay").innerHTML=html;
 }).catch(function(){});
}

function replyTicket(id,status){
 var c=document.getElementById("tr_"+id).value.trim();if(!c)return;
 jpost("/v1/admin/tickets/"+id+"/reply",{author:"kefu",assignee:"kefu",content:c,status:status}).then(function(){loadTickets();});
}
function closeTicket(id){jpost("/v1/admin/tickets/"+id+"/status",{status:4,assignee:"kefu"}).then(function(){loadTickets();});}

function loadAll(){load().then(function(){return loadDetails().catch(function(){}).then(function(){return loadTickets();}).then(function(){loadAuditAgg();loadAudit();loadModRules();loadModQueue(0);});});}
window.addEventListener("load",function(){
 var t=localStorage.getItem("im2a_admin_tok");if(t){document.getElementById("tok").value=t;loadAll();}
 setInterval(function(){if(document.getElementById("tok").value.trim())loadAll();},10000);
});
</script></body></html>`
