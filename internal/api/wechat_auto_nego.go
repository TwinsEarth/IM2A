package api

import (
	"encoding/json"
	"net/http"

	"human-agent-im/internal/relay"
)

// autoNegoRequest 触发两个微信用户的 Agent 自动协商。
type autoNegoRequest struct {
	SessionID string            `json:"sessionId"` // 已打开的协商会话 id（wnego_xxx）
	MaxRounds int               `json:"maxRounds,omitempty"`
	Personas  map[string]string `json:"personas,omitempty"` // botWxid → 该 Agent 代表用户的立场
}

// NewWechatAutoNegoHandler 构造 POST /v1/wechat/auto-nego：
// 在用户授权后，让双方 Agent 经 IM2A A2A 状态机自动多轮协商，
// 每一轮经 openclaw 真实推送给对方微信，直到达成/拒绝（临时群随之销毁）。
//
// 鉴权与 /v1/wechat/webhook 相同（IM2A_WECHAT_WEBHOOK_KEY）。
func NewWechatAutoNegoHandler(relaySvc *relay.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodPost {
			writeWebhookError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !webhookAuthorized(r) {
			writeWebhookError(w, http.StatusUnauthorized, "invalid webhook key")
			return
		}
		var req autoNegoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeWebhookError(w, http.StatusBadRequest, "parse error: "+err.Error())
			return
		}
		if req.SessionID == "" {
			writeWebhookError(w, http.StatusBadRequest, "sessionId required")
			return
		}
		if req.MaxRounds <= 0 || req.MaxRounds > 6 {
			req.MaxRounds = 4
		}
		res, err := relaySvc.AutoNegotiate(r.Context(), req.SessionID, req.MaxRounds, req.Personas)
		if err != nil {
			writeWebhookError(w, http.StatusBadGateway, "auto negotiation: "+err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
	})
}
