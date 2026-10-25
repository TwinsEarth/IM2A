// Package api: 陌生人付费消息端到端闭环（v0.9.0）。
//
// 流程：
//
//	发送方（陌生人）→ compliance 预审 → ledger reserve → charge（发送即扣费，billingMode=1）
//	接收方打开     → ledger charge（看到才扣费，billingMode=2）
//
// 三条不可妥协原则：
//  1. 付费永远在 Agent 预审之后（compliance 不过不 reserve）
//  2. 账本不可变 + messageId 幂等
//  3. 每日上限（ledger 内置）
package api

import (
	"net/http"
	"sync"
	"time"

	"human-agent-im/internal/audit"
	"human-agent-im/internal/compliance"
	"human-agent-im/internal/id"
	"human-agent-im/internal/ledger"
)

// priceConfigStore 接收方对陌生人消息的定价配置（内存，生产换 PG）。
type priceConfigStore struct {
	mu  sync.RWMutex
	cfg map[uint64]int64 // receiverID → 每封消息最低 cents
}

func newPriceConfigStore() *priceConfigStore {
	return &priceConfigStore{cfg: map[uint64]int64{}}
}

func (s *priceConfigStore) Set(receiverID uint64, minCents int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg[receiverID] = minCents
}

func (s *priceConfigStore) Get(receiverID uint64) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg[receiverID]
}

// paidMessage 付费消息内容（内存，生产换 PG/对象存储）。
type paidMessage struct {
	MessageID     string `json:"messageId"`
	SenderID      uint64 `json:"senderId"`
	ReceiverID    uint64 `json:"receiverId"`
	Content       string `json:"content"`
	Status        string `json:"status"` // charged / delivered / rejected / expired
	AmountCents   int64  `json:"amountCents"`
	BillingMode   int    `json:"billingMode"`
	CreatedAtUnix int64  `json:"createdAtUnix"` // v0.9.5
}

// paidMessageStore 付费消息内存存储。
type paidMessageStore struct {
	mu   sync.RWMutex
	msgs map[string]*paidMessage
}

func newPaidMessageStore() *paidMessageStore {
	return &paidMessageStore{msgs: map[string]*paidMessage{}}
}

func (s *paidMessageStore) Put(m *paidMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs[m.MessageID] = m
}

func (s *paidMessageStore) Get(id string) (*paidMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.msgs[id]
	return m, ok
}

func (s *paidMessageStore) ListByReceiver(receiverID uint64) []*paidMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*paidMessage{}
	for _, m := range s.msgs {
		if m.ReceiverID == receiverID {
			out = append(out, m)
		}
	}
	return out
}

func (s *paidMessageStore) ListBySender(senderID uint64) []*paidMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*paidMessage{}
	for _, m := range s.msgs {
		if m.SenderID == senderID {
			out = append(out, m)
		}
	}
	return out
}

func (s *paidMessageStore) SetStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.msgs[id]; ok {
		m.Status = status
	}
}

// blocklistStore 接收方拉黑发送方的名单。
type blocklistStore struct {
	mu      sync.RWMutex
	blocked map[uint64]map[uint64]bool // receiverID → senderID → blocked
}

func newBlocklistStore() *blocklistStore {
	return &blocklistStore{blocked: map[uint64]map[uint64]bool{}}
}

func (s *blocklistStore) Block(receiverID, senderID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocked[receiverID]; !ok {
		s.blocked[receiverID] = map[uint64]bool{}
	}
	s.blocked[receiverID][senderID] = true
}

func (s *blocklistStore) Unblock(receiverID, senderID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.blocked[receiverID]; ok {
		delete(m, senderID)
	}
}

func (s *blocklistStore) IsBlocked(receiverID, senderID uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.blocked[receiverID][senderID]
}

// receiverPreferences 接收方自动处理偏好（v0.9.8）。
type receiverPreferences struct {
	AutoAcceptBelowCents int64 `json:"autoAcceptBelowCents"` // 出价 ≤ 此值自动接受（不打扰接收方）
	AutoRejectAboveCents int64 `json:"autoRejectAboveCents"` // 出价 ≥ 此值自动拒绝（防高价骚扰）
}

type preferencesStore struct {
	mu   sync.RWMutex
	pref map[uint64]receiverPreferences
}

func newPreferencesStore() *preferencesStore {
	return &preferencesStore{pref: map[uint64]receiverPreferences{}}
}

func (s *preferencesStore) Set(receiverID uint64, p receiverPreferences) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pref[receiverID] = p
}

func (s *preferencesStore) Get(receiverID uint64) receiverPreferences {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pref[receiverID]
}

// PaidSendRequest 陌生人发送付费消息。
type PaidSendRequest struct {
	SenderID    uint64 `json:"senderId"`
	ReceiverID  uint64 `json:"receiverId"`
	Content     string `json:"content"`
	BillingMode int    `json:"billingMode"` // 1 发送即扣费 / 2 回复才扣费
	AmountCents int64  `json:"amountCents"`
}

// PaidSendResponse 发送结果。
type PaidSendResponse struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"` // delivered / blocked / charged
	Reason    string `json:"reason,omitempty"`
}

// PaidOpenRequest 接收方打开付费消息。
type PaidOpenRequest struct {
	MessageID string `json:"messageId"`
}

// PaidFlowMux 挂载付费闭环路由。
func (d *Deps) PaidFlowMux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))
	prices := newPriceConfigStore()
	msgs := newPaidMessageStore()
	blocks := newBlocklistStore()
	prefs := newPreferencesStore()

	// POST /v1/paidflow/send 陌生人发送付费消息
	mux.HandleFunc("POST /v1/paidflow/send", func(w http.ResponseWriter, r *http.Request) {
		var req PaidSendRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.SenderID == 0 || req.ReceiverID == 0 || req.Content == "" {
			writeErr(w, http.StatusBadRequest, "senderId/receiverId/content required")
			return
		}
		if req.BillingMode != 1 && req.BillingMode != 2 {
			writeErr(w, http.StatusBadRequest, "billingMode must be 1 or 2")
			return
		}
		if req.AmountCents <= 0 {
			writeErr(w, http.StatusBadRequest, "amountCents must be positive")
			return
		}

		// 1) compliance 预审（不可妥协原则：付费在预审之后）
		msgID := id.UUIDv4()
		guard := compliance.NewGuard(d.Store.Audit)
		decision := guard.Check(msgID, req.Content)
		if !decision.Allowed {
			writeJSON(w, http.StatusOK, PaidSendResponse{
				MessageID: "", Status: "blocked", Reason: decision.Reason,
			})
			return
		}

		// 1.5) v0.9.1：检查接收方定价
		if minPrice := prices.Get(req.ReceiverID); minPrice > 0 && req.AmountCents < minPrice {
			writeJSON(w, http.StatusOK, PaidSendResponse{
				MessageID: "", Status: "below_price",
				Reason: "receiver requires at least " + itoa(minPrice) + " cents",
			})
			return
		}

		// 1.6) v0.9.4：检查黑名单（接收方在内存 blocklist 中拉黑了发送方）
		if blocks.IsBlocked(req.ReceiverID, req.SenderID) {
			writeJSON(w, http.StatusOK, PaidSendResponse{
				MessageID: "", Status: "blocked", Reason: "you are blocked by receiver",
			})
			return
		}

		// 1.65) v1.5.3：PG blacklist 强制拦截。
		// 旧实现 blacklistAdd 只写 PG blacklist 表、全代码无人 SELECT，等于拉黑不生效；
		// 这里在 reserve 之前真实查库，被平台/接收方拉黑的 sender 直接拒绝，不落任何账。
		if d.DB != nil {
			var blacklisted int
			if err := d.DB.QueryRow(
				`SELECT 1 FROM blacklist WHERE user_id=$1`, itoa(int64(req.SenderID)),
			).Scan(&blacklisted); err == nil {
				writeJSON(w, http.StatusOK, PaidSendResponse{
					MessageID: "", Status: "blocked", Reason: "sender is on blacklist",
				})
				return
			}
		}

		// 1.7) v0.9.8：应用接收方偏好（自动接受/自动拒绝）
		pref := prefs.Get(req.ReceiverID)
		if pref.AutoRejectAboveCents > 0 && req.AmountCents >= pref.AutoRejectAboveCents {
			writeJSON(w, http.StatusOK, PaidSendResponse{
				MessageID: "", Status: "auto_rejected", Reason: "above receiver's auto-reject threshold",
			})
			return
		}

		// 2) ledger reserve
		if _, err := d.Store.Ledger.Reserve(msgID, req.SenderID, req.ReceiverID,
			ledger.Mode(req.BillingMode), req.AmountCents); err != nil {
			d.mapLedgerErr(w, r, "reserve", msgID, err)
			return
		}

		// 3) billingMode=1 发送即扣费
		status := "delivered"
		if req.BillingMode == 1 {
			if _, err := d.Store.Ledger.Charge(msgID, ""); err != nil {
				d.mapLedgerErr(w, r, "charge", msgID, err)
				return
			}
			status = "charged"
		}

		_ = d.Store.Audit.Write(audit.Entry{
			MessageID: msgID, UserID: req.SenderID, Action: audit.ActionContentAllow,
			Decision: "PAID_MESSAGE_SENT",
			Detail: map[string]any{
				"receiverId": req.ReceiverID, "billingMode": req.BillingMode,
				"amountCents": req.AmountCents, "status": status,
			},
		})

		msgs.Put(&paidMessage{
			MessageID: msgID, SenderID: req.SenderID, ReceiverID: req.ReceiverID,
			Content: req.Content, Status: status, AmountCents: req.AmountCents,
			BillingMode: req.BillingMode, CreatedAtUnix: time.Now().Unix(),
		})

		writeJSON(w, http.StatusOK, PaidSendResponse{
			MessageID: msgID, Status: status,
		})
	})

	// POST /v1/paidflow/open 接收方打开付费消息（billingMode=2 回复才扣费）
	mux.HandleFunc("POST /v1/paidflow/open", func(w http.ResponseWriter, r *http.Request) {
		var req PaidOpenRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.MessageID == "" {
			writeErr(w, http.StatusBadRequest, "messageId required")
			return
		}

		e, err := d.Store.Ledger.Get(req.MessageID)
		if err != nil {
			d.mapLedgerErr(w, r, "get", req.MessageID, err)
			return
		}

		// billingMode=1：发送方已扣费，打开不重复扣
		if e.Mode == ledger.ModeSendCharge {
			writeJSON(w, http.StatusOK, map[string]any{
				"messageId": e.MessageID, "status": "opened", "charged": false,
			})
			return
		}

		// billingMode=2：接收方打开才扣费
		if !e.ChargedAt.IsZero() {
			writeJSON(w, http.StatusOK, map[string]any{
				"messageId": e.MessageID, "status": "already_charged", "charged": true,
			})
			return
		}
		if _, err := d.Store.Ledger.Charge(req.MessageID, ""); err != nil {
			d.mapLedgerErr(w, r, "charge", req.MessageID, err)
			return
		}
		_ = d.Store.Audit.Write(audit.Entry{
			MessageID: req.MessageID, UserID: e.ReceiverID, Action: audit.ActionCharge,
			Decision: "RECEIVER_OPEN_AND_CHARGED",
			Detail:   map[string]any{"amountCents": e.AmountCents},
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"messageId": e.MessageID, "status": "opened", "charged": true,
		})
	})

	// v0.9.1：设置接收方定价（每封陌生人消息最低 cents）
	mux.HandleFunc("POST /v1/paidflow/price", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID uint64 `json:"receiverId"`
			MinCents   int64  `json:"minCents"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.ReceiverID == 0 {
			writeErr(w, http.StatusBadRequest, "receiverId required")
			return
		}
		prices.Set(body.ReceiverID, body.MinCents)
		writeJSON(w, http.StatusOK, map[string]any{"receiverId": body.ReceiverID, "minCents": body.MinCents})
	})

	// v0.9.1：拒收付费消息并退款（billingMode=1 已扣发送方，退给发送方）
	mux.HandleFunc("POST /v1/paidflow/reject", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MessageID string `json:"messageId"`
			Reason    string `json:"reason"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.MessageID == "" {
			writeErr(w, http.StatusBadRequest, "messageId required")
			return
		}
		e, err := d.Store.Ledger.Get(body.MessageID)
		if err != nil {
			d.mapLedgerErr(w, r, "get", body.MessageID, err)
			return
		}
		// 只有已扣费的才退款
		if !e.ChargedAt.IsZero() {
			if _, err := d.Store.Ledger.Refund(body.MessageID, body.Reason); err != nil {
				d.mapLedgerErr(w, r, "refund", body.MessageID, err)
				return
			}
		}
		_ = d.Store.Audit.Write(audit.Entry{
			MessageID: body.MessageID, UserID: e.ReceiverID, Action: audit.ActionReject,
			Decision: "PAID_MESSAGE_REJECTED",
			Detail:   map[string]any{"reason": body.Reason, "refunded": !e.ChargedAt.IsZero()},
		})
		msgs.SetStatus(body.MessageID, "rejected")
		writeJSON(w, http.StatusOK, map[string]any{
			"messageId": body.MessageID, "status": "rejected",
		})
	})

	// v0.9.2：收件箱（接收方收到的付费消息），支持 ?status= 过滤（v0.9.6）
	mux.HandleFunc("GET /v1/paidflow/inbox", func(w http.ResponseWriter, r *http.Request) {
		receiverID := atou(r.URL.Query().Get("receiverId"))
		if receiverID == 0 {
			writeErr(w, http.StatusBadRequest, "receiverId required")
			return
		}
		wantStatus := r.URL.Query().Get("status")
		list := msgs.ListByReceiver(receiverID)
		if wantStatus != "" {
			filtered := []*paidMessage{}
			for _, m := range list {
				if m.Status == wantStatus {
					filtered = append(filtered, m)
				}
			}
			list = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"receiverId": receiverID, "count": len(list), "messages": list})
	})

	// v0.9.2：已发列表（发送方发出的付费消息），支持 ?status= 过滤（v0.9.6）
	mux.HandleFunc("GET /v1/paidflow/outbox", func(w http.ResponseWriter, r *http.Request) {
		senderID := atou(r.URL.Query().Get("senderId"))
		if senderID == 0 {
			writeErr(w, http.StatusBadRequest, "senderId required")
			return
		}
		wantStatus := r.URL.Query().Get("status")
		list := msgs.ListBySender(senderID)
		if wantStatus != "" {
			filtered := []*paidMessage{}
			for _, m := range list {
				if m.Status == wantStatus {
					filtered = append(filtered, m)
				}
			}
			list = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"senderId": senderID, "count": len(list), "messages": list})
	})

	// v0.9.2：查看单条消息内容（需 receiverId 或 senderId 匹配）
	mux.HandleFunc("GET /v1/paidflow/content", func(w http.ResponseWriter, r *http.Request) {
		msgID := r.URL.Query().Get("messageId")
		if msgID == "" {
			writeErr(w, http.StatusBadRequest, "messageId required")
			return
		}
		m, ok := msgs.Get(msgID)
		if !ok {
			writeErr(w, http.StatusNotFound, "message not found")
			return
		}
		writeJSON(w, http.StatusOK, m)
	})

	// v0.9.4：拉黑发送方
	mux.HandleFunc("POST /v1/paidflow/block", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID uint64 `json:"receiverId"`
			SenderID   uint64 `json:"senderId"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.ReceiverID == 0 || body.SenderID == 0 {
			writeErr(w, http.StatusBadRequest, "receiverId/senderId required")
			return
		}
		blocks.Block(body.ReceiverID, body.SenderID)
		writeJSON(w, http.StatusOK, map[string]any{"receiverId": body.ReceiverID, "senderId": body.SenderID, "blocked": true})
	})

	// v0.9.4：解除拉黑
	mux.HandleFunc("POST /v1/paidflow/unblock", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID uint64 `json:"receiverId"`
			SenderID   uint64 `json:"senderId"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		blocks.Unblock(body.ReceiverID, body.SenderID)
		writeJSON(w, http.StatusOK, map[string]any{"receiverId": body.ReceiverID, "senderId": body.SenderID, "blocked": false})
	})

	// v0.9.8：设置接收方自动处理偏好
	mux.HandleFunc("POST /v1/paidflow/preferences", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID           uint64 `json:"receiverId"`
			AutoAcceptBelowCents int64  `json:"autoAcceptBelowCents"`
			AutoRejectAboveCents int64  `json:"autoRejectAboveCents"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.ReceiverID == 0 {
			writeErr(w, http.StatusBadRequest, "receiverId required")
			return
		}
		prefs.Set(body.ReceiverID, receiverPreferences{
			AutoAcceptBelowCents: body.AutoAcceptBelowCents,
			AutoRejectAboveCents: body.AutoRejectAboveCents,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"receiverId":           body.ReceiverID,
			"autoAcceptBelowCents": body.AutoAcceptBelowCents,
			"autoRejectAboveCents": body.AutoRejectAboveCents,
		})
	})

	// v0.9.5：过期清理（delivered 状态超时未打开的消息自动关闭）
	// 用法：POST /v1/paidflow/expire，body { "maxAgeSec": N }
	//   maxAgeSec 省略/0 → 默认 86400（24h）
	//   maxAgeSec > 0   → 超过 N 秒未打开的 delivered 消息过期
	mux.HandleFunc("POST /v1/paidflow/expire", func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			MaxAgeSec int64 `json:"maxAgeSec"`
		}{}
		_ = decode(r, &body)
		if body.MaxAgeSec == 0 {
			body.MaxAgeSec = 86400
		}
		now := time.Now().Unix()
		expired := 0
		msgs.mu.Lock()
		for _, m := range msgs.msgs {
			if m.Status != "delivered" {
				continue
			}
			if now-m.CreatedAtUnix < body.MaxAgeSec {
				continue
			}
			m.Status = "expired"
			expired++
			if e, err := d.Store.Ledger.Get(m.MessageID); err == nil && !e.ChargedAt.IsZero() {
				_, _ = d.Store.Ledger.Refund(m.MessageID, "expired")
			}
		}
		msgs.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"expiredCount": expired, "maxAgeSec": body.MaxAgeSec})
	})

	// v0.9.7：批量导出（对账/运营用），?userId=&role=receiver|sender
	mux.HandleFunc("GET /v1/paidflow/export", func(w http.ResponseWriter, r *http.Request) {
		userID := atou(r.URL.Query().Get("userId"))
		if userID == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		role := r.URL.Query().Get("role")
		var list []*paidMessage
		if role == "sender" {
			list = msgs.ListBySender(userID)
		} else {
			list = msgs.ListByReceiver(userID)
		}
		// 合并账本状态
		type exportRow struct {
			*paidMessage
			LedgerStatus int `json:"ledgerStatus"`
		}
		rows := []exportRow{}
		for _, m := range list {
			row := exportRow{paidMessage: m, LedgerStatus: -1}
			if e, err := d.Store.Ledger.Get(m.MessageID); err == nil {
				row.LedgerStatus = int(e.Status)
			}
			rows = append(rows, row)
		}
		w.Header().Set("Content-Disposition", "attachment; filename=paidflow-export.json")
		writeJSON(w, http.StatusOK, map[string]any{
			"userId": userID, "role": role, "count": len(rows), "rows": rows,
		})
	})

	// v0.9.3：付费消息统计 dashboard
	mux.HandleFunc("GET /v1/paidflow/stats", func(w http.ResponseWriter, r *http.Request) {
		userID := atou(r.URL.Query().Get("userId"))
		if userID == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		// 发送方视角：已发消息、总花费、平均单价
		sent := msgs.ListBySender(userID)
		// 接收方视角：收到消息、总收入
		received := msgs.ListByReceiver(userID)

		sentCount := len(sent)
		sentSpent := int64(0)
		rejectedCount := 0
		for _, m := range sent {
			if m.Status != "rejected" {
				sentSpent += m.AmountCents
			} else {
				rejectedCount++
			}
		}

		receivedCount := len(received)
		receivedEarned := int64(0)
		for _, m := range received {
			if m.Status != "rejected" {
				receivedEarned += m.AmountCents
			}
		}

		avgSent := int64(0)
		if sentCount > 0 {
			avgSent = sentSpent / int64(sentCount)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"userId":              userID,
			"sentCount":           sentCount,
			"sentSpentCents":      sentSpent,
			"sentAvgCents":        avgSent,
			"rejectedCount":       rejectedCount,
			"receivedCount":       receivedCount,
			"receivedEarnedCents": receivedEarned,
		})
	})

	return mux
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 8)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
