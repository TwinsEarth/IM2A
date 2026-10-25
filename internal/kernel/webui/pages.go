// SPDX-License-Identifier: MIT
//
// Package webui 收敛网关内嵌的运营/绑定页面 HTML。这些页面原先以常量形式散落在
// services/gateway/main.go 与 services/gateway/broadcast_page.go 中；Wave A 路由
// 注册表化后统一由本包提供，页面内容逐字节保持不变。
package webui

const AdminLoginHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Admin Login</title>
<style>body{background:#0b0f14;color:#e6edf3;font-family:sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh}
.box{background:#131a22;padding:32px;border-radius:12px;width:340px}
input{width:100%;padding:10px;margin:10px 0;background:#0b0f14;border:1px solid #222c38;color:#fff;border-radius:6px}
button{width:100%;padding:10px;background:#3b82f6;border:0;color:#fff;border-radius:6px;cursor:pointer}
.err{color:#ef4444;font-size:12px;min-height:16px}</style></head><body>
<div class="box"><h2>IM2A Admin</h2><form onsubmit="event.preventDefault();const t=document.getElementById('t').value;if(t){document.cookie='admin_token='+t+';path=/;max-age=86400';location.reload()}">
<input id="t" type="password" placeholder="Admin Token"><button>登录</button></form>
<div class="err" id="e"></div></div>
<script>if(location.search.includes('e=1')){document.getElementById('e').textContent='Token 错误'}</script>
</body></html>`
const WechatBindHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>微信绑定</title>
<style>
body{font-family:-apple-system,sans-serif;background:#1a1a2e;color:#fff;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0}
.card{background:#16213e;padding:40px;border-radius:16px;text-align:center;max-width:420px}
h1{font-size:20px;margin:0 0 8px}
p{color:#aaa;font-size:13px;margin:8px 0}
#qr{margin:20px auto;background:#fff;padding:12px;border-radius:8px;width:240px;height:240px;display:flex;align-items:center;justify-content:center}
#qr img{width:100%;height:100%}
#timer{font-size:28px;color:#6cf;font-weight:700;margin:10px 0}
button{background:#0f3460;color:#fff;border:none;padding:10px 24px;border-radius:6px;cursor:pointer;font-size:14px;margin-top:10px}
button:hover{background:#164880}
a{color:#6cf;font-size:12px;word-break:break-all}
#status{margin-top:14px;font-size:13px;color:#ffd166}
</style></head><body>
<div class="card">
<h1>绑定你的微信</h1>
<p>用微信扫一扫下方二维码，5 分钟内有效</p>
<div id="guide" style="display:flex;gap:8px;justify-content:center;margin:6px 0 2px;flex-wrap:wrap">
<span style="background:#0f3460;padding:5px 10px;border-radius:14px;font-size:12px">① 打开微信</span>
<span style="background:#0f3460;padding:5px 10px;border-radius:14px;font-size:12px">② 点右上角「+」</span>
<span style="background:#0f3460;padding:5px 10px;border-radius:14px;font-size:12px">③ 扫一扫</span>
<span style="background:#164880;padding:5px 10px;border-radius:14px;font-size:12px">④ 确认登录</span>
</div>
<div id="qr"><span style="color:#999">加载中...</span></div>
<div id="timer">05:00</div>
<button onclick="start()">重新生成二维码</button>
<p id="status"></p>
<a id="link" target="_blank"></a>
</div>
<script>
const TOK="im2a_demo_token";let sec=300,timer;
async function start(){
  document.getElementById('status').textContent='启动 OpenClaw...';
  const r=await fetch('/v1/openclaw/start',{method:'POST',headers:{Authorization:'Bearer '+TOK}});
  const j=await r.json();
  const st=document.getElementById('status');
  function show(msg){st.textContent=msg;}
  if(j.queued){
    show(j.message+'：前面排第 '+(j.position||'?')+' 位，预计约 '+((j.etaSeconds||0)/60).toFixed(1)+' 分钟。'+(j.nextStep||''));
    const owner=j.owner;
    clearInterval(timer);
    timer=setInterval(async()=>{
      try{
        const q=await (await fetch('/v1/openclaw/pool/queue?owner='+encodeURIComponent(owner),{headers:{Authorization:'Bearer '+TOK}})).json();
        if(q.ready&&q.qrUrl){clearInterval(timer);draw(q);}
        else if(q.queued){show(q.message+'：前面排第 '+(q.position||'?')+' 位，预计约 '+((q.etaSeconds||0)/60).toFixed(1)+' 分钟。');}
        else {show('状态刷新中...');}
      }catch(e){show('网络抖动，正在重试...');}
    },2000);
    return;
  }
  if(!j.qrUrl){show((j.message||'失败: '+(j.error||JSON.stringify(j)))+(j.nextStep?'（'+j.nextStep+'）':''));return}
  draw(j);
  function draw(j){
    document.getElementById('qr').innerHTML='<img src="https://api.qrserver.com/v1/create-qr-code/?size=220x220&margin=0&data='+encodeURIComponent(j.qrUrl)+'">';
    document.getElementById('link').href=j.qrUrl;
    document.getElementById('link').textContent='或点此链接打开';
    sec=300;clearInterval(timer);
    timer=setInterval(()=>{
      sec--;
      if(sec<0){clearInterval(timer);show('二维码已过期，'+(j.nextStep||'点「重新生成二维码」再扫'));return}
      const m=Math.floor(sec/60),s=sec%60;
      document.getElementById('timer').textContent=String(m).padStart(2,'0')+':'+String(s).padStart(2,'0');
    },1000);
    show('等待扫码...（扫码成功会直接发欢迎语到你微信）');
  }
}
start();
</script></body></html>
`
const PlaygroundHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>IM2A API Playground</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#0f1419;color:#e6e6e6;height:100vh;display:flex;flex-direction:column}
header{padding:10px 16px;background:#1a2230;border-bottom:1px solid #2a3444;display:flex;gap:12px;align-items:center}
header b{color:#6cf;font-size:14px}
header input{background:#0f1419;border:1px solid #2a3444;color:#e6e6e6;padding:6px 10px;border-radius:4px;font-size:12px;font-family:monospace}
#tok{width:340px}
#base{width:240px}
main{flex:1;display:flex;overflow:hidden}
#left{width:280px;border-right:1px solid #2a3444;overflow-y:auto;padding:8px}
#left .g{color:#6cf;font-size:11px;margin:10px 0 4px;text-transform:uppercase}
#left button{display:block;width:100%;text-align:left;background:none;border:none;color:#ccc;padding:5px 8px;font-size:12px;cursor:pointer;border-radius:3px;font-family:monospace}
#left button:hover{background:#1a2230}
#left button.active{background:#23406b;color:#fff}
#left .m{display:inline-block;width:42px;font-weight:700}
.mPOST{color:#6f6}.mGET{color:#69f}.mDELETE{color:#f66}
#mid{flex:1;padding:16px;overflow-y:auto;border-right:1px solid #2a3444}
#mid label{display:block;font-size:12px;color:#999;margin:10px 0 4px}
#mid input,#mid select,#mid textarea{width:100%;background:#0a0e14;border:1px solid #2a3444;color:#e6e6e6;padding:8px;border-radius:4px;font-family:monospace;font-size:12px}
#mid textarea{min-height:140px;resize:vertical}
#mid button{margin-top:14px;background:#23406b;color:#fff;border:none;padding:10px 24px;border-radius:4px;cursor:pointer;font-size:13px}
#mid button:hover{background:#2d5188}
#right{flex:1;padding:16px;overflow-y:auto}
#right h3{font-size:12px;color:#999;margin-bottom:8px}
#out{background:#0a0e14;border:1px solid #2a3444;border-radius:4px;padding:12px;font-family:monospace;font-size:12px;white-space:pre-wrap;word-break:break-all;min-height:300px}
code{color:#6cf}
</style>
</head>
<body>
<header>
  <b>IM2A API</b>
  <input id="base" value="https://geefish.cn">
  <input id="tok" placeholder="Bearer token" value="im2a_demo_token">
</header>
<main>
<div id="left"></div>
<div id="mid">
  <label>方法</label><select id="method"></select>
  <label>路径（{id} 用下方替换）</label><input id="path">
  <label>路径参数</label><input id="pathParams" placeholder="例如：id=123 或 qrId=oc_xxx">
  <label>Body (JSON)</label><textarea id="body">{}</label>
  <button onclick="send()">发送 →</button>
</div>
<div id="right">
  <h3>响应</h3>
  <div id="out">点左侧端点开始</div>
</div>
</main>
<script>
const EP=[
 ['健康',['GET','/healthz',{}]],
 ['监控',['GET','/metrics',{}]],
 ['认证',['POST','/v1/auth/login',{user_id:"u_demo",device:"web"}],['GET','/v1/auth/me',{}]],
 ['微信身份',['POST','/v1/wechat/identify',{wxid:"o_demo"}]],
 ['Bot组',['POST','/v1/wechat/groups',{owner:"u_demo",name:"我的组"}],['POST','/v1/wechat/bindings',{groupId:"g_xxx",wxid:"o_demo",device:"ios"}]],
 ['OpenClaw 扫码',['POST','/v1/openclaw/qr',{owner:"u_demo",device:"ios"}],['POST','/v1/openclaw/callback',{qrId:"oc_xxx",wxid:"wx_demo"}],['GET','/v1/openclaw/qr/{qrId}',{qrId:"oc_xxx"}]],
 ['审批',['GET','/v1/approvals',{}]],
 ['协商',['GET','/v1/negotiations',{}]],
 ['付费',['GET','/v1/paid/inbox',{}]],
 ['Agent',['GET','/v1/agents/directory',{}]],
 ['临时群',['POST','/v1/tempgroup/create',{peer:"o_other"}]],
 ['Bot 路由',['POST','/v1/botroute/send',{to:"wx_target",text:"hello"}]],
 ['工作汇报',['POST','/v1/report/generate',{userId:"u_demo"}]],
];
const left=document.getElementById('left');
EP.forEach(([g,eps])=>{
  const h=document.createElement('div');h.className='g';h.textContent=g;left.appendChild(h);
  eps.forEach(ep=>{
    const b=document.createElement('button');
    b.innerHTML=` + "`" + `<span class="m m${ep[0]}">${ep[0]}</span> ${ep[1]}` + "`" + `;
    b.onclick=()=>select(b,ep);
    left.appendChild(b);
  });
});
let cur=null;
function select(btn,ep){
  document.querySelectorAll('#left button').forEach(x=>x.classList.remove('active'));
  btn.classList.add('active');cur=ep;
  document.getElementById('method').innerHTML=` + "`" + `<option>${ep[0]}</option>` + "`" + `;
  document.getElementById('path').value=ep[1];
  document.getElementById('body').value=JSON.stringify(ep[2],null,2);
  document.getElementById('out').textContent='';
}
async function send(){
  const base=document.getElementById('base').value.replace(/\/$/,'');
  let path=document.getElementById('path').value;
  const pp=document.getElementById('pathParams').value;
  if(pp) pp.split(';').forEach(kv=>{const[k,v]=kv.split('=');path=path.replace('{'+k.trim()+'}',v.trim())});
  const tok=document.getElementById('tok').value;
  const out=document.getElementById('out');
  out.textContent='请求中...';
  try{
    const opt={method:document.getElementById('method').value,headers:{Authorization:'Bearer '+tok,'Content-Type':'application/json'}};
    if(opt.method!=='GET'&&opt.method!=='DELETE') opt.body=document.getElementById('body').value;
    const t0=Date.now();
    const r=await fetch(base+path,opt);
    const txt=await r.text();
    let pretty=txt;try{pretty=JSON.stringify(JSON.parse(txt),null,2)}catch(e){}
    out.textContent=` + "`" + `HTTP ${r.status}  ${Date.now()-t0}ms\n\n${pretty}` + "`" + `;
  }catch(e){out.textContent='ERR: '+e.message}
}
</script>
</body></html>
`
const BroadcastHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>IM2A · 全量广播</title>
<style>
:root{--bg:#0d1117;--card:#161b22;--bd:#30363d;--tx:#e6edf3;--mut:#8b949e;--ok:#3fb950;--warn:#d29922;--err:#f85149;--pri:#2f81f7;}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--tx);font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:24px 18px 60px}
h1{font-size:20px;margin:0 0 4px}
.sub{color:var(--mut);margin:0 0 20px}
.card{background:var(--card);border:1px solid var(--bd);border-radius:10px;padding:16px 18px;margin-bottom:16px}
.card h2{font-size:14px;margin:0 0 12px;color:var(--mut);font-weight:600;letter-spacing:.5px}
.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}
.metric{background:#0d1117;border:1px solid var(--bd);border-radius:8px;padding:8px 12px;min-width:96px}
.metric b{display:block;font-size:20px}
.metric span{color:var(--mut);font-size:12px}
textarea{width:100%;min-height:120px;background:#0d1117;border:1px solid var(--bd);border-radius:8px;color:var(--tx);padding:12px;font:inherit;resize:vertical}
button{border:0;border-radius:8px;padding:10px 18px;font-size:14px;font-weight:600;cursor:pointer}
.btn-pri{background:var(--pri);color:#fff}
.btn-pri:disabled{opacity:.5;cursor:not-allowed}
.btn-ghost{background:transparent;color:var(--mut);border:1px solid var(--bd)}
.pill{display:inline-block;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600}
.pill.running{background:#1f2d4a;color:#79b8ff}
.pill.completed{background:#11331c;color:var(--ok)}
.pill.partial{background:#3a2f08;color:var(--warn)}
.pill.failed{background:#3d1418;color:var(--err)}
.pill.pending{background:#22262d;color:var(--mut)}
.bar{height:8px;background:#0d1117;border-radius:99px;overflow:hidden;margin:10px 0}
.bar i{display:block;height:100%;background:var(--ok);width:0;transition:width .4s}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:7px 8px;border-bottom:1px solid var(--bd)}
th{color:var(--mut);font-weight:600}
.mono{font-family:ui-monospace,Menlo,monospace;font-size:12px;color:var(--mut)}
.st-ok{color:var(--ok)} .st-fail{color:var(--err)} .st-pend{color:var(--mut)}
.muted{color:var(--mut)}
.tag{font-size:12px;color:var(--mut)}
.hist{cursor:pointer}
.hist:hover{background:#0d1117}
</style>
</head>
<body>
<div class="wrap">
  <h1>IM2A · 微信全量广播</h1>
  <p class="sub">运营后台一键群发：经官方 Bot 账号向所有绑定用户的微信逐条投递（自动限流，排除官方号自身）。</p>

  <div class="card">
    <h2>① 投放目标（实时）</h2>
    <div id="targets" class="muted">加载中…</div>
  </div>

  <div class="card">
    <h2>② 广播文案</h2>
    <textarea id="msg">【通知】2026年10月23号，IM2A 智能服务已上线。</textarea>
    <div class="row" style="margin-top:12px;justify-content:flex-end">
      <button class="btn-ghost" onclick="loadPreview()">刷新目标</button>
      <button id="sendBtn" class="btn-pri" onclick="send()">一键群发</button>
    </div>
  </div>

  <div class="card" id="progCard" style="display:none">
    <h2>③ 投递进度 <span id="progPill" class="pill running">running</span></h2>
    <div class="row" style="margin-bottom:8px">
      <div class="metric"><b id="mTotal">-</b><span>目标</span></div>
      <div class="metric"><b id="mSent" style="color:var(--ok)">-</b><span>已送达</span></div>
      <div class="metric"><b id="mFail" style="color:var(--err)">-</b><span>失败</span></div>
    </div>
    <div class="bar"><i id="bar"></i></div>
    <table>
      <thead><tr><th>目标 wxid</th><th>状态</th><th>messageId / 错误</th></tr></thead>
      <tbody id="dels"></tbody>
    </table>
  </div>

  <div class="card">
    <h2>④ 历史广播</h2>
    <table>
      <thead><tr><th>任务 ID</th><th>状态</th><th>送达/总数</th><th>时间</th></tr></thead>
      <tbody id="hist"><tr><td colspan="4" class="muted">加载中…</td></tr></tbody>
    </table>
  </div>
</div>
<script>
function tok(){var m=document.cookie.match(/admin_token=([^;]+)/);return m?decodeURIComponent(m[1]):"";}
function api(path,opt){opt=opt||{};opt.headers=Object.assign({"Content-Type":"application/json","Authorization":"Bearer "+tok()},opt.headers||{});return fetch(path,opt).then(function(r){return r.json().then(function(j){if(!r.ok)throw new Error(j.error||r.status);return j;});});}
function esc(s){return String(s==null?"":s).replace(/[&<>]/g,function(c){return {"&":"&amp;","<":"&lt;",">":"&gt;"}[c];});}
function shortWxid(w){if(!w)return "";return w.length>28?w.slice(0,14)+"…"+w.slice(-10):w;}
function pillClass(st){return ["running","completed","partial","failed","pending"].indexOf(st)>=0?st:"pending";}

function loadPreview(){
  api("/v1/admin/broadcast?preview=1").then(function(j){
    var html='<div class="row" style="margin-bottom:10px">'+
      '<div class="metric"><b>'+j.count+'</b><span>接收用户</span></div>'+
      '<div class="metric"><b style="font-size:13px">'+esc(shortWxid(j.sender_wxid))+'</b><span>官方发送号</span></div>'+
      '</div>';
    if(j.targets&&j.targets.length){
      html+='<div class="tag">将投递到：</div><div class="mono" style="margin-top:6px">'+j.targets.map(esc).join("<br>")+'</div>';
    }else{
      html+='<div class="st-fail">当前没有可投递的其他用户（仅有官方发送号在线）。</div>';
    }
    document.getElementById("targets").innerHTML=html;
  }).catch(function(e){document.getElementById("targets").innerHTML='<span class="st-fail">加载失败：'+esc(e.message)+'</span>';});
}
var sendKey="";
var sendKeyText="";
function newIdemKey(){return "bc-key-"+Date.now()+"-"+Math.random().toString(36).slice(2,10);}
function send(){
  var text=document.getElementById("msg").value.trim();
  if(!text){alert("请输入广播文案");return;}
  if(!confirm("确认向全部在线用户群发该通知？"))return;
  // 文案变化或上一轮已结束 → 换新幂等键；同一轮重试沿用旧键（绝不重复群发）。
  if(!sendKey||sendKeyText!==text){sendKey=newIdemKey();sendKeyText=text;}
  var btn=document.getElementById("sendBtn");btn.disabled=true;btn.textContent="群发中…";
  document.getElementById("progCard").style.display="block";
  api("/v1/admin/broadcast",{method:"POST",body:JSON.stringify({text:text,idempotencyKey:sendKey})}).then(function(j){
    poll(j.id);
  }).catch(function(e){
    // 弱网/移动网络下服务端可能已落库并群发，但 202 响应没回到浏览器（表现为 Failed to fetch）。
    // 不能直接报失败诱导重复群发：先按幂等键回查，找到任务即进入轮询。
    reconcile(sendKey,text,btn,e);
  });
}
function reconcile(key,text,btn,origErr){
  api("/v1/admin/broadcast?limit=10").then(function(j){
    var found="";
    (j.broadcasts||[]).forEach(function(b){
      if(!found && (b.idempotency_key===key || b.text===text)){found=b.id;}
    });
    if(found){
      document.getElementById("progCard").style.display="block";
      poll(found);
    }else{
      alert("创建失败："+origErr.message+"（服务端未查到该任务，可重试）");
      btn.disabled=false;btn.textContent="一键群发";
    }
  }).catch(function(){
    alert("创建失败："+origErr.message+"（回查也失败，请稍后刷新查看历史广播）");
    btn.disabled=false;btn.textContent="一键群发";
  });
}
function poll(id){
  api("/v1/admin/broadcast?id="+encodeURIComponent(id)).then(function(j){
    var b=j.broadcast;
    document.getElementById("mTotal").textContent=b.total;
    document.getElementById("mSent").textContent=b.sent;
    document.getElementById("mFail").textContent=b.failed;
    var p=document.getElementById("progPill");p.textContent=b.status;p.className="pill "+pillClass(b.status);
    var pct=b.total?Math.round((b.sent+b.failed)/b.total*100):0;
    document.getElementById("bar").style.width=pct+"%";
    document.getElementById("bar").style.background=b.failed>0?"var(--warn)":"var(--ok)";
    var rows=(b.deliveries||[]).map(function(d){
      var cls=d.status==="sent"?"st-ok":(d.status==="failed"?"st-fail":"st-pend");
      var info=d.status==="sent"?d.message_id:(d.error||"");
      return '<tr><td class="mono">'+esc(shortWxid(d.to_wxid))+'</td><td class="'+cls+'">'+esc(d.status)+'</td><td class="mono">'+esc(info)+'</td></tr>';
    }).join("");
    document.getElementById("dels").innerHTML=rows;
    if(b.status==="running"||b.status==="pending"){setTimeout(function(){poll(id);},1500);}
    else{var btn=document.getElementById("sendBtn");btn.disabled=false;btn.textContent="一键群发";sendKey="";sendKeyText="";loadHistory();}
  }).catch(function(e){setTimeout(function(){poll(id);},2000);});
}
function loadHistory(){
  api("/v1/admin/broadcast?limit=20").then(function(j){
    var rows=(j.broadcasts||[]).map(function(b){
      return '<tr class="hist" onclick="openHist(\''+b.id+'\')"><td class="mono">'+esc(b.id)+'</td>'+
        '<td><span class="pill '+pillClass(b.status)+'">'+esc(b.status)+'</span></td>'+
        '<td>'+b.sent+"/"+b.total+(b.failed?" ("+b.failed+" 失败)":"")+'</td>'+
        '<td class="tag">'+esc(new Date(b.created_at).toLocaleString())+'</td></tr>';
    }).join("");
    document.getElementById("hist").innerHTML=rows||'<tr><td colspan="4" class="muted">暂无</td></tr>';
  }).catch(function(){document.getElementById("hist").innerHTML='<tr><td colspan="4" class="st-fail">加载失败</td></tr>';});
}
function openHist(id){
  document.getElementById("progCard").style.display="block";poll(id);
}
loadPreview();loadHistory();
</script>
</body>
</html>`
