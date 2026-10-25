// payment-svc HTTP 端点：付费消息占位/扣费/退款/日用量（幂等）。
package api

import (
	"errors"
	"net/http"

	"human-agent-im/internal/audit"
	"human-agent-im/internal/id"
	"human-agent-im/internal/ledger"
)

// ReserveRequest 占位请求（预审通过后才调用）。
type ReserveRequest struct {
	MessageID   string `json:"messageId,omitempty"` // 可空，服务端生成 UUID
	SenderID    uint64 `json:"senderId"`
	ReceiverID  uint64 `json:"receiverId"`
	BillingMode int    `json:"billingMode"` // 1 发送即扣费 / 2 回复才扣费
	AmountCents int64  `json:"amountCents"`
}

// ChargeRequest 扣费请求（幂等键 messageId）。
type ChargeRequest struct {
	MessageID      string `json:"messageId"`
	ApprovalCardID string `json:"approvalCardId"`
}

// RefundRequest 退款请求。
type RefundRequest struct {
	MessageID string `json:"messageId"`
	Reason    string `json:"reason"`
}

// PaymentMux 挂载 payment-svc 路由。
func (d *Deps) PaymentMux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/paid/reserve", func(w http.ResponseWriter, r *http.Request) {
		var req ReserveRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.SenderID == 0 || req.ReceiverID == 0 || (req.BillingMode != 1 && req.BillingMode != 2) {
			writeErr(w, http.StatusBadRequest, "invalid sender/receiver/billingMode")
			return
		}
		if req.MessageID == "" {
			req.MessageID = id.UUIDv4()
		}
		e, err := d.Store.Ledger.Reserve(req.MessageID, req.SenderID, req.ReceiverID,
			ledger.Mode(req.BillingMode), req.AmountCents)
		if err != nil {
			d.mapLedgerErr(w, r, "reserve", req.MessageID, err)
			return
		}
		writeJSON(w, http.StatusOK, entryJSON(e))
	})

	mux.HandleFunc("POST /v1/paid/charge", func(w http.ResponseWriter, r *http.Request) {
		var req ChargeRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		e, err := d.Store.Ledger.Charge(req.MessageID, req.ApprovalCardID)
		if err != nil {
			d.mapLedgerErr(w, r, "charge", req.MessageID, err)
			return
		}
		_ = d.Store.Audit.Write(audit.Entry{
			MessageID: req.MessageID, UserID: e.SenderID, Action: audit.ActionCharge,
			Decision: "CHARGED", Detail: map[string]any{"amountCents": e.AmountCents, "card": e.ApprovalCardID},
		})
		writeJSON(w, http.StatusOK, entryJSON(e))
	})

	mux.HandleFunc("POST /v1/paid/refund", func(w http.ResponseWriter, r *http.Request) {
		var req RefundRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		e, err := d.Store.Ledger.Refund(req.MessageID, req.Reason)
		if err != nil {
			d.mapLedgerErr(w, r, "refund", req.MessageID, err)
			return
		}
		_ = d.Store.Audit.Write(audit.Entry{
			MessageID: req.MessageID, UserID: e.ReceiverID, Action: audit.ActionRefund,
			Decision: "REFUNDED", Detail: map[string]any{"reason": req.Reason},
		})
		writeJSON(w, http.StatusOK, entryJSON(e))
	})

	mux.HandleFunc("GET /v1/paid/usage", func(w http.ResponseWriter, r *http.Request) {
		sender := atou(r.URL.Query().Get("sender"))
		sent, limit := d.Store.Ledger.DailyUsage(sender)
		writeJSON(w, http.StatusOK, map[string]any{"senderId": sender, "sent": sent, "limit": limit})
	})

	mux.HandleFunc("GET /v1/paid/get", func(w http.ResponseWriter, r *http.Request) {
		msg := r.URL.Query().Get("messageId")
		e, err := d.Store.Ledger.Get(msg)
		if err != nil {
			d.mapLedgerErr(w, r, "get", msg, err)
			return
		}
		writeJSON(w, http.StatusOK, entryJSON(e))
	})

	return mux
}

// mapLedgerErr 把账本领域错误映射为 HTTP 状态码（保持幂等语义显式）。
func (d *Deps) mapLedgerErr(w http.ResponseWriter, r *http.Request, op, messageID string, err error) {
	_ = r
	_ = d.Store.Audit.Write(audit.Entry{MessageID: messageID, Action: audit.ActionCharge,
		Decision: op + "_ERROR", Detail: map[string]any{"error": err.Error()}})
	switch {
	case errors.Is(err, ledger.ErrAlreadyProcessed):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ledger.ErrDailyLimitExceeded):
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ledger.ErrInvalidTransition):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ledger.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func entryJSON(e *ledger.Entry) map[string]any {
	charged := int64(0)
	if !e.ChargedAt.IsZero() {
		charged = e.ChargedAt.Unix()
	}
	return map[string]any{
		"messageId":      e.MessageID,
		"senderId":       e.SenderID,
		"receiverId":     e.ReceiverID,
		"billingMode":    int(e.Mode),
		"amountCents":    e.AmountCents,
		"status":         int(e.Status),
		"chargedAtUnix":  charged,
		"approvalCardId": e.ApprovalCardID,
	}
}
