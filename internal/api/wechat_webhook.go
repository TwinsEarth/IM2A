package api

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"human-agent-im/internal/openclaw"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"human-agent-im/internal/inqueue"
	"human-agent-im/internal/relay"
)

// flexString 兼容 string / number / null 的 JSON 字符串字段。
// openclaw 入站会把 msgId 当 number 下发，直接 string 反序列化会报
// "cannot unmarshal number into Go struct field ... of type string"（P2 E2E 真机复现）。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = ""
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var inner string
		if err := json.Unmarshal(b, &inner); err != nil {
			return err
		}
		*f = flexString(inner)
		return nil
	}
	// number 或其它标量：原样当字符串
	*f = flexString(s)
	return nil
}

// webhookRequest 是入站消息信封（微信 → openclaw → IM2A）。
type webhookRequest struct {
	Account       string     `json:"account"`       // 哪个 openclaw bot 收到
	BotWxid       string     `json:"botWxid"`       // 可选，缺省由 account 解析
	FromWxid      string     `json:"fromWxid"`      // 发消息者 wxid
	Text          string     `json:"text"`          // 消息正文
	MsgID         flexString `json:"msgId"`         // 渠道消息唯一 id（幂等）
	NegoSessionID string     `json:"negoSessionId"` // 非空=推进已存在的协商会话
	NegoIntent    string     `json:"negoIntent"`    // ACCEPT/COUNTER/REJECT/REQUEST_INFO/REQUEST_AUTH
}

// webhookIdentity 是 wxid → 用户的识别结果（任务 #105）。
type webhookIdentity struct {
	Wxid   string `json:"wxid"`
	UserID int64  `json:"userId"`
	Bound  bool   `json:"bound"`
}

// webhookResponse 是 webhook 响应。
type webhookResponse struct {
	OK       bool             `json:"ok"`
	Identity *webhookIdentity `json:"identity,omitempty"`
	Result   *relay.Result    `json:"result,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// NewWechatWebhookHandler 构造 v2.2.1 标准 REST webhook：POST /v1/wechat/webhook。
//
// 与 MCP 端点（/v2/wechat/mcp）共用同一个 relay.Service，提供给 openclaw / 其他渠道
// 在收到微信消息后做确定性 HTTP POST（不依赖 agent 自主决定调工具）。
//
// 鉴权：query ?key= 或 Header X-Webhook-Key，需等于环境变量 IM2A_WECHAT_WEBHOOK_KEY；
// 该变量为空时表示仅在 loopback 部署、不做 key 校验（与 MCP 端点一致）。
func NewWechatWebhookHandler(relaySvc *relay.Service, db *sql.DB, openclawSender func() *openclaw.Sender) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeWebhookError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !webhookAuthorized(r) {
			writeWebhookError(w, http.StatusUnauthorized, "invalid webhook key")
			return
		}

		var req webhookRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeWebhookError(w, http.StatusBadRequest, "parse error: "+err.Error())
			return
		}
		// D5：全链路统一 deadline 30s（识别→LLM→出站），不无限等，结果可预期。
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if req.Account == "" || req.FromWxid == "" || req.Text == "" {
			writeWebhookError(w, http.StatusBadRequest, "account/fromWxid/text required")
			return
		}

		// 协商推进分支：带 negoSessionId 时推进 8 态状态机，而非新建消息。
		if req.NegoSessionID != "" {
			sess, nerr := relaySvc.NegoReply(ctx, req.NegoSessionID, req.FromWxid, req.NegoIntent, req.Text)
			if nerr != nil {
				writeWebhookError(w, http.StatusBadRequest, "nego reply: "+nerr.Error())
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"nego": map[string]any{"id": sess.ID, "state": sess.State.String(), "round": sess.Round},
			})
			return
		}

		// 识别 wxid → 查用户（best-effort，未绑定也允许走 self 助手，由 relay 决定）。
		ident := identifyWxid(ctx, db, req.FromWxid)

		// A10 C端零扫码：新用户第一次给预登录账号发消息 → 自动开通（无需扫码）。
		if !ident.Bound {
			if uid, isNew := autoProvision(ctx, db, req.Account, req.FromWxid); isNew {
				ident.UserID = uid
				ident.Bound = true
				go func(account, wxid string) {
					bg, cancel := context.WithTimeout(context.Background(), 50*time.Second)
					defer cancel()
					if _, serr := resolveOpenClawSender(openclawSender).Send(bg, account, wxid, "欢迎来到 IM2A，我是你的专属助手，直接发消息就能用啦。"); serr != nil {
						log.Printf("[A10-provision] welcome send failed account=%s err=%v", account, serr)
					}
				}(req.Account, req.FromWxid)
			}
		}

		// 幂等键优先用渠道 msgId，否则用账号/发送者/正文兜底。
		idem := string(req.MsgID)
		if idem == "" {
			idem = "wh_" + req.Account + "_" + req.FromWxid + "_" + req.Text
		}

		// v2.3.1 A1：WEBHOOK_ASYNC=on 时只入队立即 ACK，由后台 worker 调 relay，
		// 避免 openclaw 同步等 30s 触发回调超时；崩溃后 queued 事件自动重投不丢。
		if os.Getenv("WEBHOOK_ASYNC") == "on" {
			var negoSession, negoIntent string
			if req.NegoSessionID != "" {
				negoSession, negoIntent = req.NegoSessionID, req.NegoIntent
			}
			_, dup, enqErr := inqueue.Enqueue(r.Context(), db, idem, req.Account, req.FromWxid, req.Text, negoSession, negoIntent)
			if enqErr != nil {
				writeWebhookError(w, http.StatusInternalServerError, "enqueue: "+enqErr.Error())
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"ok":       true,
				"queued":   true,
				"dup":      dup,
				"identity": ident,
			})
			return
		}

		res, err := relaySvc.Relay(ctx, req.Account, req.FromWxid, req.Text, idem)
		if err != nil {
			writeWebhookError(w, http.StatusBadGateway, "relay: "+err.Error())
			return
		}
		json.NewEncoder(w).Encode(webhookResponse{OK: true, Identity: ident, Result: res})
	})
}

// identifyWxid 查 wechat_identity 把 wxid 映射到 user_id。
func identifyWxid(ctx context.Context, db *sql.DB, wxid string) *webhookIdentity {
	ident := &webhookIdentity{Wxid: wxid, Bound: false}
	if db == nil || wxid == "" {
		return ident
	}
	var uid int64
	err := db.QueryRowContext(ctx, `SELECT user_id FROM wechat_identity WHERE wxid=$1`, wxid).Scan(&uid)
	if err == nil {
		ident.UserID = uid
		ident.Bound = true
	}
	return ident
}

func webhookAuthorized(r *http.Request) bool {
	want := os.Getenv("IM2A_WECHAT_WEBHOOK_KEY")
	if want == "" {
		return true // 仅 loopback 部署，不校验
	}
	got := r.URL.Query().Get("key")
	if got == "" {
		got = r.Header.Get("X-Webhook-Key")
	}
	return got == want
}

func writeWebhookError(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(webhookResponse{OK: false, Error: msg})
}

// autoProvision 给首次接触的新用户自动开通账号（C端零扫码）：
// 复用 wxid→user 映射、独立组、设备绑定，落到预登录的 openclaw 账号上。
// 返回 (userID, 是否新建)。
func autoProvision(ctx context.Context, db *sql.DB, accountID, wxid string) (int64, bool) {
	if db == nil || wxid == "" {
		return 0, false
	}
	var uid int64
	err := db.QueryRowContext(ctx, `SELECT user_id FROM wechat_identity WHERE wxid=$1`, wxid).Scan(&uid)
	if err == nil {
		return uid, false // 已绑定过，不重复开通
	}
	// 新建用户
	var newUID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO wechat_identity(wxid,user_id) VALUES($1, nextval('seq_user_id')) RETURNING user_id`, wxid).Scan(&newUID); err != nil {
		log.Printf("[A10-provision] upsert identity err=%v", err)
		return 0, false
	}
	sum := md5.Sum([]byte(wxid))
	owner := "u_" + hex.EncodeToString(sum[:5])
	gid := "grp_" + owner + "_001"
	_, _ = db.ExecContext(ctx,
		`INSERT INTO wechat_bot_groups (id, owner_user, name) VALUES ($1,$2,'我的Bot组') ON CONFLICT (id) DO NOTHING`, gid, owner)
	bid := "bind_" + owner + "_pre"
	_, _ = db.ExecContext(ctx,
		`INSERT INTO wechat_bindings (id, group_id, wxid, device_type) VALUES ($1,$2,$3,'ios') ON CONFLICT (id) DO NOTHING`,
		bid, gid, wxid)
	_, _ = db.ExecContext(ctx,
		`INSERT INTO agent_quota_usage (user_id, plan_key) VALUES ($1,'free') ON CONFLICT (user_id) DO NOTHING`, newUID)
	log.Printf("[A10-provision] new user auto-bound uid=%d wxid=%s account=%s", newUID, wxid, accountID)
	return newUID, true
}
