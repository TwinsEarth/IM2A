package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"human-agent-im/internal/a2a"
	"human-agent-im/internal/agentdir"
	"human-agent-im/internal/agentquota"
	"human-agent-im/internal/botroute"
	"human-agent-im/internal/errcode"
	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/report"
	"human-agent-im/internal/tempgroup"
	"human-agent-im/internal/userkey"
	"human-agent-im/internal/wechatapi"
	"human-agent-im/internal/wechatbot"
	"human-agent-im/internal/wechatid"
)

// WechatV2Mux 统一接入 v2.0.x 微信 Bot 体系全部端点。
func (d *Deps) WechatV2Mux() http.Handler {
	mux := http.NewServeMux()
	w := func(wr http.ResponseWriter, code int, v any) {
		wr.Header().Set("Content-Type", "application/json; charset=utf-8")
		wr.WriteHeader(code)
		_ = json.NewEncoder(wr).Encode(v)
	}

	// ---- v2.0.5 协议入口（验签/幂等/分发，全依赖注入）----
	wapi := wechatapi.New(d.DB, userkey.New(d.DB)).WithOptions(wechatapi.Options{
		Quota:    agentquota.New(d.DB),
		Dir:      agentdir.New(d.DB),
		A2A:      a2a.New(d.DB),
		WechatID: wechatid.New(d.DB),
	})
	mux.HandleFunc("POST /v1/wechatbot/inbox", func(wr http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		out, err := wapi.Handle(r.Context(), body)
		if err != nil {
			code := 400
			if strings.Contains(err.Error(), "sig") || strings.Contains(err.Error(), "unbound") {
				code = 401
			}
			if strings.Contains(err.Error(), "quota") {
				code = 409
			}
			w(wr, code, map[string]any{"error": err.Error()})
			return
		}
		wr.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = wr.Write(out)
	})

	// ---- v2.0.0 Bot 组绑定 ----
	bot := wechatbot.New(d.DB)
	mux.HandleFunc("POST /v1/wechat/groups", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Id, Owner, Name string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		g, err := bot.CreateGroup(r.Context(), b.Id, b.Owner, b.Name)
		if err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 201, g)
	})
	mux.HandleFunc("POST /v1/wechat/bindings", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Id, GroupId, Wxid, Device string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		bind, err := bot.BindDevice(r.Context(), b.Id, b.GroupId, b.Wxid, b.Device)
		if err != nil {
			w(wr, 409, errBody{err.Error()})
			return
		}
		w(wr, 201, bind)
	})
	mux.HandleFunc("GET /v1/wechat/groups/{id}", func(wr http.ResponseWriter, r *http.Request) {
		g, bs, err := bot.Group(r.Context(), r.PathValue("id"))
		if err != nil {
			w(wr, 404, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"group": g, "bindings": bs})
	})

	// ---- v2.0.1 wxid 识别 + 额度 ----
	wid := wechatid.New(d.DB)
	q := agentquota.New(d.DB)
	mux.HandleFunc("POST /v1/wechat/identify", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Wxid string }
		if err := decode(r, &b); err != nil || b.Wxid == "" {
			w(wr, 400, errBody{"missing wxid"})
			return
		}
		uid, isNew, err := wid.Identify(r.Context(), b.Wxid)
		if err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"userId": uid, "isNew": isNew})
	})
	mux.HandleFunc("GET /v1/quota/{userId}", func(wr http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseInt(r.PathValue("userId"), 10, 64)
		u, err := q.Usage(r.Context(), uid)
		if err != nil {
			w(wr, 404, errBody{err.Error()})
			return
		}
		w(wr, 200, u)
	})
	mux.HandleFunc("POST /v1/quota/upgrade", func(wr http.ResponseWriter, r *http.Request) {
		var b struct {
			UserId int64
			Plan   string
		}
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if err := q.Upgrade(r.Context(), b.UserId, b.Plan); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"userId": b.UserId, "plan": b.Plan, "upgraded": true})
	})

	// ---- v2.0.8 临时协商群 ----
	tg := tempgroup.New(d.DB)
	mux.HandleFunc("POST /v1/tempgroup/create", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Id, Topic, CreatedBy string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if err := tg.Create(r.Context(), b.Id, b.Topic, b.CreatedBy); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 201, map[string]any{"id": b.Id, "active": true})
	})
	mux.HandleFunc("POST /v1/tempgroup/join", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ GroupId, AgentId string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if err := tg.Join(r.Context(), b.GroupId, b.AgentId); err != nil {
			w(wr, 409, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"joined": true})
	})
	mux.HandleFunc("POST /v1/tempgroup/post", func(wr http.ResponseWriter, r *http.Request) {
		var b struct {
			GroupId, FromAgent string
			Payload            any
		}
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if err := tg.Post(r.Context(), b.GroupId, b.FromAgent, b.Payload); err != nil {
			w(wr, 409, errBody{err.Error()})
			return
		}
		w(wr, 201, map[string]any{"posted": true})
	})
	mux.HandleFunc("POST /v1/tempgroup/destroy", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ GroupId string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if err := tg.Destroy(r.Context(), b.GroupId); err != nil {
			w(wr, 409, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"destroyed": true})
	})
	mux.HandleFunc("GET /v1/tempgroup/history/{id}", func(wr http.ResponseWriter, r *http.Request) {
		h, err := tg.History(r.Context(), r.PathValue("id"), 50)
		if err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"messages": h, "count": len(h)})
	})

	// ---- v2.0.9 Bot 间路由 ----
	br := botroute.New(d.DB)
	mux.HandleFunc("POST /v1/botroute/send", func(wr http.ResponseWriter, r *http.Request) {
		var b struct {
			From, To, Type string
			Payload        any
		}
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if _, err := br.Route(r.Context(), b.From, b.To, b.Type, b.Payload); err != nil {
			w(wr, 409, errBody{err.Error()})
			return
		}
		w(wr, 202, map[string]any{"routed": true})
	})
	mux.HandleFunc("GET /v1/botroute/inbox/{wxid}", func(wr http.ResponseWriter, r *http.Request) {
		items, err := br.Inbox(r.Context(), r.PathValue("wxid"), 50)
		if err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"items": items, "count": len(items)})
	})

	// ---- v2.1.0 工作汇报 ----
	rep := report.New(d.DB)
	mux.HandleFunc("POST /v1/report/generate", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ UserId string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		rp, err := rep.Generate(r.Context(), b.UserId)
		if err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 201, rp)
	})
	mux.HandleFunc("POST /v1/report/{id}/approve", func(wr http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err := rep.Approve(r.Context(), id); err != nil {
			w(wr, 409, errBody{err.Error()})
			return
		}
		w(wr, 200, map[string]any{"id": id, "status": "approved"})
	})

	// ---- v2.1.2 OpenClaw 扫码绑定 ----
	ocids := wechatid.New(d.DB)
	ocbots := wechatbot.New(d.DB)
	ocSvc := openclaw.New(d.DB, ocids, ocbots)
	mux.HandleFunc("POST /v1/openclaw/qr", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ QRID, Owner, Device string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if b.QRID == "" {
			b.QRID = "oc_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		se, err := ocSvc.NewSession(r.Context(), b.QRID, b.Owner, b.Device)
		if err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		w(wr, 201, se)
	})
	mux.HandleFunc("POST /v1/openclaw/callback", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ QRID, Wxid string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		se, err := ocSvc.Callback(r.Context(), b.QRID, b.Wxid)
		if err != nil {
			code := 400
			if se != nil {
				w(wr, code, se)
				return
			}
			w(wr, code, errBody{err.Error()})
			return
		}
		w(wr, 200, se)
	})
	mux.HandleFunc("GET /v1/openclaw/qr/{id}", func(wr http.ResponseWriter, r *http.Request) {
		se, err := ocSvc.Status(r.Context(), r.PathValue("id"))
		if err != nil {
			w(wr, 404, errBody{err.Error()})
			return
		}
		w(wr, 200, se)
	})

	// ---- v2.1.3 OpenClaw 进程池：每用户独立 profile，并发 15 ----
	ocPool := openclaw.NewPool()
	mux.HandleFunc("POST /v1/openclaw/start", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Owner string }
		_ = decode(r, &b)
		if b.Owner == "" {
			b.Owner = "visitor_" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
		}
		u, err := ocPool.Start(b.Owner)
		if err != nil {
			if errors.Is(err, openclaw.ErrQueued) {
				pos, eta, active, _ := ocPool.QueueStatus(b.Owner)
				h := errcode.Of(errcode.QUEUED)
				w(wr, 202, map[string]any{
					"queued": true, "owner": b.Owner,
					"position": pos, "etaSeconds": eta,
					"active": active, "max": openclaw.PoolMaxConcurrency,
					"poll": "/v1/openclaw/pool/queue?owner=" + b.Owner,
					"code": h.Code, "message": h.Message, "nextStep": h.NextStep,
				})
				return
			}
			h := errcode.FromErr(err)
			code := 429
			w(wr, code, map[string]any{"error": err.Error(), "code": h.Code, "message": h.Message, "nextStep": h.NextStep})
			return
		}
		h := errcode.Of(errcode.OK)
		w(wr, 200, map[string]any{"qrUrl": u, "owner": b.Owner, "expiresIn": 300, "queued": false,
			"code": h.Code, "message": h.Message, "nextStep": h.NextStep})
	})
	mux.HandleFunc("GET /v1/openclaw/pool", func(wr http.ResponseWriter, r *http.Request) {
		w(wr, 200, ocPool.Status())
	})
	// A7 排队可视化：当前第几个 / 预计等待 / 是否已放行拿到二维码
	mux.HandleFunc("GET /v1/openclaw/pool/queue", func(wr http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		if owner == "" {
			w(wr, 400, errBody{"owner required"})
			return
		}
		pos, eta, active, waiting := ocPool.QueueStatus(owner)
		// 不在队列但已有活跃会话 → 直接给二维码
		st := ocPool.Status()
		if prof, ok := st["profiles"].(map[string]any)[owner]; ok {
			if pm, ok2 := prof.(map[string]any); ok2 && pm["qrUrl"] != "" {
				h := errcode.Of(errcode.CONFIRMED)
				w(wr, 200, map[string]any{"queued": false, "ready": true, "owner": owner, "qrUrl": pm["qrUrl"], "active": active, "max": openclaw.PoolMaxConcurrency, "code": h.Code, "message": h.Message, "nextStep": h.NextStep})
				return
			}
		}
		if !waiting {
			w(wr, 200, map[string]any{"queued": false, "ready": false, "owner": owner, "active": active, "max": openclaw.PoolMaxConcurrency})
			return
		}
		h := errcode.Of(errcode.QUEUED)
		w(wr, 200, map[string]any{
			"queued": true, "ready": false, "owner": owner,
			"position": pos, "etaSeconds": eta,
			"active": active, "max": openclaw.PoolMaxConcurrency,
			"code": h.Code, "message": h.Message, "nextStep": h.NextStep,
		})
	})

	// ---- A12 绑定结果回调：外部/手动绑定成功后 POST {account,wxid} 触发欢迎语+建组闭环 ----
	mux.HandleFunc("POST /v1/wechat/bind-callback", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Account, Wxid string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if b.Wxid == "" {
			w(wr, 400, errBody{"wxid required"})
			return
		}
		go sendWelcomeOnBind(d, b.Account, b.Wxid)
		w(wr, 200, map[string]any{"ok": true, "wxid": b.Wxid, "triggered": b.Account != ""})
	})

	// ---- v2.1.2 watcher 自动回调：新账号落库，每用户独立组 ----
	mux.HandleFunc("POST /v1/openclaw/auto-bind", func(wr http.ResponseWriter, r *http.Request) {
		var b struct{ Wxid, Device, Account string }
		if err := decode(r, &b); err != nil {
			w(wr, 400, errBody{err.Error()})
			return
		}
		if b.Wxid == "" {
			w(wr, 400, errBody{"wxid required"})
			return
		}
		if b.Device == "" {
			b.Device = "ios"
		}
		// 1) identify
		ident, _, err := wid.Identify(r.Context(), b.Wxid)
		if err != nil {
			w(wr, 500, errBody{err.Error()})
			return
		}
		// 1.5) 自动开 free 额度（若不存在）
		_, _ = d.DB.ExecContext(r.Context(),
			`INSERT INTO agent_quota_usage (user_id, plan_key) VALUES ($1,'free') ON CONFLICT (user_id) DO NOTHING`, ident)
		// 2) 每个用户独立 owner：从 wxid 派生（md5 前 10 位）
		sum := md5.Sum([]byte(b.Wxid))
		owner := "u_" + hex.EncodeToString(sum[:5])
		// 3) 独立组
		gid := "grp_" + owner + "_001"
		if _, _, gerr := bot.Group(r.Context(), gid); gerr != nil {
			if _, err := bot.CreateGroup(r.Context(), gid, owner, "我的Bot组"); err != nil {
				w(wr, 500, errBody{err.Error()})
				return
			}
		}
		// 4) 绑设备（bid 含 owner 不冲突）
		bid := "bind_" + owner + "_" + b.Device
		bind, err := bot.BindDevice(r.Context(), bid, gid, b.Wxid, b.Device)
		if err != nil {
			w(wr, 409, map[string]any{"wxid": b.Wxid, "userId": ident, "owner": owner, "groupId": gid, "bind": err.Error()})
			return
		}
		// A12 绑定结果回调：新绑定成功即发欢迎语（统一走 sendWelcomeOnBind）
		go sendWelcomeOnBind(d, b.Account, b.Wxid)
		w(wr, 200, map[string]any{"wxid": b.Wxid, "userId": ident, "owner": owner, "groupId": gid, "bind": bind})
	})
	return mux
}

// sendWelcomeOnBind A12 绑定结果回调：绑定成功即从指定 openclaw 账号给用户发欢迎语并热启 context。
// 所有绑定入口（watcher 自动绑、C端零扫码、手动绑定回调）统一走这里，保证建组闭环。
// 出站经 Deps 注入的插件单例（契约 §6），未接线时 nil-safe 回退默认构造。
func sendWelcomeOnBind(d *Deps, account, wxid string) {
	if account == "" || wxid == "" {
		return
	}
	bg, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	if mid, err := resolveOpenClawSender(d.OpenClawSender).Send(bg, account, wxid, "已绑定，欢迎来到地球新手村！"); err != nil {
		log.Printf("[A12-bind-callback] welcome send failed account=%s err=%v", account, err)
	} else {
		log.Printf("[A12-bind-callback] welcome sent account=%s wxid=%s mid=%s", account, wxid, mid)
	}
}
