// v1.5.0: 陌生人付费账本 HTTP 面（/v1/ledger/*）。
//
// v2.3.27（task-12）双账本修复：
//   本文件此前把全部 SQL 写在 `payment_ledger_legacy_v09` 上 —— 那是一张**退役表**
//   （根 migrations/20261023_migrate_payment_ledger_to_paid_message_ledger.sql 把它
//   从 payment_ledger 重命名而来，注释明写"老表只读归档"）。后果：
//     * 全新库上根本没有这张表 → 五个端点全部 42P01（500）；
//     * 即便手工建过表，扣费落在统计与 privacy 都不读的表里 → /v1/admin/stats 的
//       paid_total_cents / paid_charged 看不到这些流水。
//   现统一指向**权威账本** `paid_message_ledger`（001_init.sql:103 建表，
//   internal/store/postgres.go 的 pgLedger 是同表的另一条写入路径，字段语义见
//   docs/notes-ledger-consolidation.md 的字段差异表）。
//
// 两个映射必须记住（否则会写坏账）：
//   1) paid_message_ledger.message_id 是 **UUID NOT NULL UNIQUE**，而 HTTP 入参是任意
//      字符串 → 用 ledgerUUID() 做**确定性**映射（同一 messageId 永远同一 UUID，保持幂等），
//      原串写进 038 迁移新增的 biz_message_id 供列表/对账显示。
//   2) sender_id / receiver_id 是 **BIGINT** → 非数字入参一律 400（老实现把它们当 TEXT
//      存进 legacy 表；新表类型不允许，静默转换会把脏数据写进账）。
package api

import (
	"crypto/md5"
	"encoding/json"
	"net/http"
	"strconv"
)

type PaidChargeRequest struct {
	MessageID   string `json:"messageId"`
	SenderID    string `json:"senderId"`
	ReceiverID  string `json:"receiverId"`
	AmountCents int64  `json:"amountCents"`
	Mode        string `json:"mode"` // send / pay_open
}

// ledgerUUID 把外部 messageId 映射为 paid_message_ledger.message_id 需要的 UUID。
// 用 md5 派生（确定性）：同一 messageId 永远得到同一 UUID，因此 ON CONFLICT(message_id)
// 仍是可靠幂等键；不需要引入 uuid 依赖。原始字符串另存 biz_message_id。
func ledgerUUID(messageID string) string {
	sum := md5.Sum([]byte("im2a:ledger:biz:" + messageID))
	h := sum[:]
	return hexN(h[0:4]) + "-" + hexN(h[4:6]) + "-" + hexN(h[6:8]) + "-" + hexN(h[8:10]) + "-" + hexN(h[10:16])
}

func hexN(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}

// parseUserID 把 "1001" 这类入参转成 BIGINT；非数字返回 ok=false（调用方回 400）。
// 老接口允许 wxid 字符串，但 paid_message_ledger 的 sender_id/receiver_id 是 BIGINT：
// 与其隐式哈希成一个假用户，不如显式拒绝（账本必须可对账）。
func parseUserID(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// billingMode 把老接口的 mode 字符串映射成 paid_message_ledger.billing_mode(1|2)。
// 与根迁移的确定性映射一致：send→1（发送即扣）, pay_open→2（回复才扣）。
func billingMode(mode string) int16 {
	if mode == "pay_open" {
		return 2
	}
	return 1
}

// statusText 把 SMALLINT 状态映射回老接口的字符串（保持响应体兼容）。
func statusText(status int16) string {
	switch status {
	case 1:
		return "charged"
	case 2:
		return "refunded"
	default:
		return "pending"
	}
}

// paidCharge 发送即扣费。
func (d *Deps) paidCharge(w http.ResponseWriter, r *http.Request) {
	var req PaidChargeRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.MessageID == "" || req.SenderID == "" || req.ReceiverID == "" {
		writeErr(w, 400, "messageId/senderId/receiverId required")
		return
	}
	if req.AmountCents <= 0 {
		writeErr(w, 400, "amount must > 0")
		return
	}
	sender, ok := parseUserID(req.SenderID)
	if !ok {
		writeErr(w, 400, "senderId must be a numeric user id")
		return
	}
	receiver, ok := parseUserID(req.ReceiverID)
	if !ok {
		writeErr(w, 400, "receiverId must be a numeric user id")
		return
	}

	mid := ledgerUUID(req.MessageID)
	if d.DB != nil {
		// v1.5.2: 日限额（每个 sender 每天最多 10 次扣费）——语义与老实现一致：
		// 统计当日已落账的全部流水（含已退款），不看 status。
		const dailyLimit = 10
		var todayCount int
		_ = d.DB.QueryRow(`SELECT COUNT(*) FROM paid_message_ledger
			WHERE sender_id=$1 AND created_at >= CURRENT_DATE`, sender).Scan(&todayCount)
		if todayCount >= dailyLimit {
			writeErr(w, 429, "daily limit exceeded (10/day)")
			return
		}

		// 幂等：message_id 唯一键冲突即已扣过费。用 ON CONFLICT 而不是先 SELECT 后 INSERT，
		// 避免并发重复回调把同一笔扣两次（老实现的 SELECT→INSERT 有窗口）。
		res, err := d.DB.Exec(`INSERT INTO paid_message_ledger
			(message_id, sender_id, receiver_id, billing_mode, amount_cents, status, charged_at, biz_message_id)
			VALUES ($1::uuid,$2,$3,$4,$5,1,now(),$6)
			ON CONFLICT (message_id) DO NOTHING`,
			mid, sender, receiver, billingMode(req.Mode), req.AmountCents, req.MessageID)
		if err != nil {
			writeErr(w, 500, "ledger insert failed: "+err.Error())
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// 已存在：返回当前真实状态（而不是无脑声称 charged）。
			var status int16
			if err := d.DB.QueryRow(
				`SELECT status FROM paid_message_ledger WHERE message_id=$1::uuid`, mid).Scan(&status); err != nil {
				writeErr(w, 500, "ledger lookup failed: "+err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{
				"messageId": req.MessageID, "status": statusText(status), "idempotent": true,
			})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "status": "charged", "amountCents": req.AmountCents})
}

// paidList 查账本。仍按"发送方或接收方"过滤，但走 paid_message_ledger。
func (d *Deps) paidList(w http.ResponseWriter, r *http.Request) {
	uidStr := r.URL.Query().Get("userId")
	list := []map[string]any{}
	if d.DB != nil {
		uid, ok := parseUserID(uidStr)
		if !ok {
			writeErr(w, 400, "userId must be a numeric user id")
			return
		}
		rows, err := d.DB.Query(`SELECT COALESCE(biz_message_id, message_id::text),
				sender_id::text, receiver_id::text, amount_cents,
				CASE billing_mode WHEN 2 THEN 'pay_open' ELSE 'send' END,
				CASE status WHEN 1 THEN 'charged' WHEN 2 THEN 'refunded' ELSE 'pending' END,
				created_at::text
			FROM paid_message_ledger WHERE sender_id=$1 OR receiver_id=$1
			ORDER BY id DESC LIMIT 50`, uid)
		if err == nil && rows != nil {
			defer rows.Close()
			for rows.Next() {
				var mid, sid, rid, mode, status, ts string
				var amt int64
				if err := rows.Scan(&mid, &sid, &rid, &amt, &mode, &status, &ts); err != nil {
					continue
				}
				list = append(list, map[string]any{
					"messageId": mid, "senderId": sid, "receiverId": rid,
					"amountCents": amt, "mode": mode, "status": status, "createdAt": ts,
				})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"userId": uidStr, "entries": list, "count": len(list)})
}

// paidOpen v1.5.1: 回复才扣费（pay_open 模式）。
// 收件人查看消息并回复时，才向发送方扣费。
func (d *Deps) paidOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MessageID  string `json:"messageId"`
		ReceiverID string `json:"receiverId"`
		Replied    bool   `json:"replied"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.MessageID == "" || req.ReceiverID == "" {
		writeErr(w, 400, "messageId/receiverId required")
		return
	}
	if !req.Replied {
		// 仅查看未回复，不扣费
		writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "charged": false, "reason": "not replied"})
		return
	}
	if d.DB != nil {
		mid := ledgerUUID(req.MessageID)
		var sender, receiver, amount int64
		var status, mode int16
		err := d.DB.QueryRow(`SELECT sender_id, receiver_id, amount_cents, status, billing_mode
			FROM paid_message_ledger WHERE message_id=$1::uuid`, mid).
			Scan(&sender, &receiver, &amount, &status, &mode)
		if err != nil {
			writeErr(w, 404, "message not found")
			return
		}
		if strconv.FormatInt(receiver, 10) != req.ReceiverID {
			writeErr(w, 403, "not the receiver")
			return
		}
		if status == 1 {
			writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "charged": true, "idempotent": true})
			return
		}
		if status == 2 {
			writeErr(w, 409, "message already refunded")
			return
		}
		// pending(0) → charged(1)：与 pgLedger.Charge 同一状态机（只允许 0→1）。
		if _, err := d.DB.Exec(`UPDATE paid_message_ledger
			SET status=1, charged_at=now(), billing_mode=2
			WHERE message_id=$1::uuid AND status=0`, mid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_, _ = d.DB.Exec(`INSERT INTO paid_message_events(message_id, from_status, to_status, note)
			VALUES ($1::uuid,0,1,'charged(pay_open)')`, mid)
		writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "charged": true, "amountCents": amount, "mode": "pay_open"})
		return
	}
	writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "charged": true, "mode": "pay_open"})
}

// paidRefund v1.5.3: 退款。
// 状态机与 paid_message_ledger 保持一致：只有 charged(1) 能退成 refunded(2)。
func (d *Deps) paidRefund(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MessageID string `json:"messageId"`
		Reason    string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.MessageID == "" {
		writeErr(w, 400, "messageId required")
		return
	}
	if d.DB != nil {
		mid := ledgerUUID(req.MessageID)
		var status int16
		err := d.DB.QueryRow(`SELECT status FROM paid_message_ledger WHERE message_id=$1::uuid`, mid).Scan(&status)
		if err != nil {
			writeErr(w, 404, "message not found")
			return
		}
		if status == 2 {
			writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "refunded": true, "idempotent": true})
			return
		}
		if status != 1 {
			writeErr(w, 409, "only charged messages can be refunded")
			return
		}
		if _, err := d.DB.Exec(`UPDATE paid_message_ledger
			SET status=2, refunded_at=now(), refund_reason=$2
			WHERE message_id=$1::uuid AND status=1`, mid, req.Reason); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_, _ = d.DB.Exec(`INSERT INTO paid_message_events(message_id, from_status, to_status, note)
			VALUES ($1::uuid,1,2,$2)`, mid, req.Reason)
	}
	writeJSON(w, 200, map[string]any{"messageId": req.MessageID, "refunded": true})
}

// blacklistAdd v1.5.3: 加黑名单（blacklist 表在 036 迁移补建）。
func (d *Deps) blacklistAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"userId"`
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.UserID == "" {
		writeErr(w, 400, "userId required")
		return
	}
	if d.DB != nil {
		_, err := d.DB.Exec(`INSERT INTO blacklist(user_id, reason) VALUES($1,$2) ON CONFLICT (user_id) DO NOTHING`,
			req.UserID, req.Reason)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"userId": req.UserID, "blacklisted": true})
}

// PaidMux 付费账本路由。
func (d *Deps) PaidMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ledger/charge", d.paidCharge)
	mux.HandleFunc("GET /v1/ledger/", d.paidList)
	mux.HandleFunc("POST /v1/ledger/open", d.paidOpen)
	mux.HandleFunc("POST /v1/ledger/refund", d.paidRefund)
	mux.HandleFunc("POST /v1/ledger/blacklist", d.blacklistAdd)
	return mux
}
