package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"human-agent-im/internal/eventbus"
	"human-agent-im/internal/im"
	"human-agent-im/internal/trustzone"
)

// pgGroupRepo 用真实 PostgreSQL 实现 im.GroupRepo。
type pgGroupRepo struct{ db *sql.DB }

func (r pgGroupRepo) Members(groupID string) ([]string, error) {
	rows, err := r.db.Query(`SELECT user_id FROM im_group_members WHERE group_id=$1 ORDER BY user_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r pgGroupRepo) SaveGroupMessage(m im.GroupMessage) (int64, error) {
	var id int64
	err := r.db.QueryRow(
		`INSERT INTO im_group_messages(group_id, from_user, text) VALUES ($1,$2,$3) RETURNING id`,
		m.GroupID, m.FromUser, m.Text,
	).Scan(&id)
	return id, err
}

func (d *Deps) IMMux() http.Handler {
	mux := http.NewServeMux()
	appID, _ := strconv.ParseInt(os.Getenv("TIM_SDK_APP_ID"), 10, 64)
	secretKey := os.Getenv("TIM_SECRET_KEY")
	endpoint := os.Getenv("TIM_ENDPOINT")
	// 回调验签密钥：优先专用 TIM_WEBHOOK_SECRET，缺省回退 SecretKey。
	webhookSecret := os.Getenv("TIM_WEBHOOK_SECRET")
	if webhookSecret == "" {
		webhookSecret = secretKey
	}
	client := im.New(im.Config{
		SDKAppID:  appID,
		SecretKey: secretKey,
		Endpoint:  endpoint,
	})
	repo := pgGroupRepo{db: d.DB}

	// v1.2.1: UserSig 签发
	mux.HandleFunc("POST /v1/im/usersig", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		us, err := client.GenUserSig(req.UserID)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, us)
	})

	// v1.2.1: UserSig 刷新（重新签发，旧票自然过期）
	mux.HandleFunc("POST /v1/im/usersig/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		us, err := client.GenUserSig(req.UserID)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{
			"userId": us.UserID, "userSig": us.Sig, "expiresAt": us.ExpiresAt,
			"mode": us.Mode, "refreshed": true,
		})
	})

	// v1.2.2: Webhook 第三方回调（验签 + SDKAppID 校验 + MsgKey 幂等落库）。
	mux.HandleFunc("POST /v1/im/webhook", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, 400, "read body")
			return
		}
		// 验签：配置了密钥时强制；支持签名头或 ?key= 两种方式。
		verified := false
		if webhookSecret != "" {
			if qk := r.URL.Query().Get("key"); qk != "" {
				if qk != webhookSecret {
					writeErr(w, 401, "bad webhook key")
					return
				}
				verified = true
			} else {
				ts, _ := strconv.ParseInt(r.Header.Get("X-TIM-Timestamp"), 10, 64)
				sig := r.Header.Get("X-TIM-Signature")
				if err := im.VerifyWebhook(webhookSecret, ts, raw, sig); err != nil {
					writeErr(w, 401, err.Error())
					return
				}
				verified = true
			}
		} else if os.Getenv("IM2A_WEBHOOK_DEV") != "1" {
			// 安全默认：对外回调端点未配签名密钥时拒绝，防止匿名伪造业务消息。
			writeErr(w, 503, "webhook unconfigured: set TIM_WEBHOOK_SECRET (use ?key= in Tencent callback URL)")
			return
		}

		var ev im.WebhookEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			writeErr(w, 400, "bad event")
			return
		}
		if err := ev.Valid(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !client.AppIDMatches(ev) {
			writeErr(w, 401, "SdkAppID mismatch")
			return
		}

		duplicated := false
		if d.DB != nil {
			payload, _ := json.Marshal(ev)
			res, err := d.DB.Exec(`INSERT INTO im_webhook_events
				(callback_type, from_user, to_user, group_id, msg_key, sdk_app_id, payload)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (msg_key) WHERE msg_key IS NOT NULL DO NOTHING`,
				ev.CallbackType, ev.FromUserID, ev.ToUserID, ev.GroupID,
				nullIfEmpty(ev.MsgKey), ev.SdkAppID, payload)
			if err != nil {
				log.Printf("webhook persist error: %v", err)
				writeErr(w, 500, "persist webhook")
				return
			}
			if n, _ := res.RowsAffected(); n == 0 && ev.MsgKey != "" {
				duplicated = true
			}
		}
		// 腾讯云要求回调返回 200（否则重试），幂等重复也返回 200。
		// v1.7.1: 关系链/好友回调驱动五档信任矩阵（真实落 im_relations）。
		if d.DB != nil {
			applyRelationEvent(d.DB, ev)
		}
		// v1.7.2: 已读回执 + 群成员变更同步。
		if d.DB != nil {
			applyReadReceipt(d.DB, ev)
			applyGroupMembership(d.DB, ev)
			applyInboundMessage(d.DB, ev) // v1.7.5：普通 C2C 消息入站镜像
		}
		writeJSON(w, 200, map[string]any{
			"action": "allow", "eventType": ev.CallbackType,
			"from": ev.FromUserID, "to": ev.ToUserID, "group": ev.GroupID,
			"channel": im.Route(ev.CallbackType), "verified": verified,
			"duplicated": duplicated, "persisted": d.DB != nil,
		})
	})

	// v1.7.1: 关系档位查询 / 手动设档。
	mux.HandleFunc("GET /v1/im/relations", d.listRelations)
	mux.HandleFunc("POST /v1/im/relations", d.upsertRelation)
	mux.HandleFunc("GET /v1/im/messages", d.listIMMessages) // v1.7.5
	mux.HandleFunc("GET /v1/im/unread", d.imUnreadCount)    // v1.7.5

	// v1.2.0: 真实发送单聊（未配置腾讯云凭证返回 503，绝不假装成功）。
	mux.HandleFunc("POST /v1/im/send", func(w http.ResponseWriter, r *http.Request) {
		var req im.SendMessageReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad body")
			return
		}
		rcpt, err := client.SendC2C(req)
		if err != nil {
			if errors.Is(err, im.ErrNotConfigured) {
				writeJSON(w, 503, map[string]any{"error": err.Error(), "mode": client.Mode()})
				return
			}
			log.Printf("tim send c2c error: %v", err)
			writeErr(w, 502, err.Error())
			return
		}
		// v1.7.5：出站成功后本地镜像为 sent（幂等）。
		if d.DB != nil && rcpt.MsgKey != "" {
			_, _ = d.DB.Exec(`INSERT INTO im_messages(msg_key,from_user,to_user,dir,text)
				VALUES($1,$2,$3,'sent',$4) ON CONFLICT (msg_key) DO NOTHING`,
				rcpt.MsgKey, rcpt.From, rcpt.To, req.Text)
		}
		writeJSON(w, 200, rcpt)
	})

	// v1.2.3: 消息三通道路由。
	mux.HandleFunc("POST /v1/im/dispatch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MsgType string `json:"msgType"`
			From    string `json:"from"`
			To      string `json:"to"`
			Text    string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		disp := &im.Dispatcher{
			IMClient: client,
			WSPublish: func(t string, p any) {
				if d.Stream != nil {
					d.Stream.Broadcast(t, p)
				}
			},
			BusPublish: func(e string, p any) {
				evt := &eventbus.Event{
					Type:      eventbus.EventType(e),
					AgentID:   req.From,
					Timestamp: time.Now().Unix(),
					Payload:   map[string]any{"from": req.From, "to": req.To, "data": p},
				}
				if d.Bus != nil {
					_ = d.Bus.Publish(context.Background(), evt)
				}
			},
		}
		ch, err := disp.Dispatch(req.MsgType, im.SendMessageReq{
			FromUserID: req.From, ToUserID: req.To, Text: req.Text,
		})
		if err != nil {
			if errors.Is(err, im.ErrNotConfigured) {
				writeJSON(w, 503, map[string]any{"channel": ch, "error": err.Error(), "mode": client.Mode()})
				return
			}
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"msgType": req.MsgType, "channel": ch, "mode": client.Mode()})
	})

	// v1.2.4: 建群（真实落 im_groups + im_group_members）。
	mux.HandleFunc("POST /v1/im/groups", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			GroupID string   `json:"groupId"`
			Name    string   `json:"name"`
			OwnerID string   `json:"ownerId"`
			Members []string `json:"members"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad body")
			return
		}
		g, err := im.NewGroup(req.GroupID, req.Name, req.OwnerID, req.Members)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if d.DB != nil {
			if _, err := d.DB.Exec(`INSERT INTO im_groups(group_id,name,owner_id) VALUES($1,$2,$3)
				ON CONFLICT (group_id) DO NOTHING`, g.GroupID, g.Name, g.OwnerID); err != nil {
				log.Printf("create group error: %v", err)
				writeErr(w, 500, "persist group")
				return
			}
			for _, m := range g.Members {
				role := "member"
				if m == g.OwnerID {
					role = "owner"
				}
				if _, err := d.DB.Exec(`INSERT INTO im_group_members(group_id,user_id,role)
					VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, g.GroupID, m, role); err != nil {
					log.Printf("add member error: %v", err)
					writeErr(w, 500, "persist member")
					return
				}
			}
		}
		writeJSON(w, 201, g)
	})

	// v1.2.4: 群成员列表。
	mux.HandleFunc("GET /v1/im/groups/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("id")
		if d.DB == nil {
			writeJSON(w, 200, map[string]any{"groupId": gid, "members": []string{}, "count": 0})
			return
		}
		members, err := repo.Members(gid)
		if err != nil {
			log.Printf("list members error: %v", err)
			writeErr(w, 500, "list members")
			return
		}
		writeJSON(w, 200, map[string]any{"groupId": gid, "members": members, "count": len(members)})
	})

	// v1.2.4: 群消息广播（成员校验 → 落 im_group_messages → 推流给其他成员）。
	mux.HandleFunc("POST /v1/im/groups/{id}/broadcast", func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("id")
		var req struct {
			From string `json:"from"`
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad body")
			return
		}
		if d.DB == nil {
			writeJSON(w, 503, map[string]any{"error": "persistence not configured"})
			return
		}
		bc := &im.Broadcaster{
			Repo: repo,
			Push: func(topic string, payload any) {
				if d.Stream != nil {
					d.Stream.Broadcast(topic, payload)
				}
			},
		}
		pushed, msgID, err := bc.Broadcast(im.GroupMessage{
			GroupID: gid, FromUser: req.From, Text: req.Text,
		})
		if err != nil {
			if errors.Is(err, im.ErrNotMember) {
				writeErr(w, 403, err.Error())
				return
			}
			log.Printf("group broadcast error: %v", err)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{
			"groupId": gid, "from": req.From, "msgId": msgID,
			"pushed": pushed, "pushedCount": len(pushed),
		})
	})

	return mux
}

// nullIfEmpty 空字符串转 NULL（用于可空唯一键）。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// applyRelationEvent v1.7.1：把关系链类回调归一为五档信任档位变更。
// 加好友默认 L3 已知（接收留言摘要，对外回复需审批）；删好友降级 L4 陌生。
func applyRelationEvent(db *sql.DB, ev im.WebhookEvent) {
	owner, peer := ev.FromUserID, ev.ToUserID
	if owner == "" || peer == "" {
		return
	}
	switch {
	case containsAny(ev.CallbackType, "FriendAdd", "FriendImport", "RelationAdd", "FriendUpdate"):
		upsimRelation(db, owner, peer, int(trustzone.LevelKnown), "friend", "webhook")
	case containsAny(ev.CallbackType, "FriendDelete"):
		upsimRelation(db, owner, peer, int(trustzone.LevelStranger), "deleted", "webhook")
	}
}

func upsimRelation(db *sql.DB, owner, peer string, level int, status, source string) {
	_, err := db.Exec(`
INSERT INTO im_relations(owner_id, peer_id, trust_level, status, source, updated_at)
VALUES ($1,$2,$3,$4,$5, now())
ON CONFLICT (owner_id, peer_id) DO UPDATE SET
  trust_level=EXCLUDED.trust_level, status=EXCLUDED.status,
  source=EXCLUDED.source, updated_at=now()`,
		owner, peer, level, status, source)
	if err != nil {
		log.Printf("im_relations upsert %s->%s: %v", owner, peer, err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func (d *Deps) listRelations(w http.ResponseWriter, r *http.Request) {
	if d.DB == nil {
		writeJSON(w, 503, map[string]any{"error": "db unavailable"})
		return
	}
	owner := r.URL.Query().Get("owner")
	if owner == "" {
		writeErr(w, 400, "missing owner")
		return
	}
	rows, err := d.DB.Query(`SELECT peer_id, trust_level, status, source
		FROM im_relations WHERE owner_id=$1 ORDER BY updated_at DESC`, owner)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type rel struct {
		Peer      string `json:"peer"`
		Level     int    `json:"level"`
		LevelName string `json:"levelName"`
		Status    string `json:"status"`
		Source    string `json:"source"`
	}
	out := []rel{}
	for rows.Next() {
		var p, st, src string
		var lv int
		if err := rows.Scan(&p, &lv, &st, &src); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, rel{p, lv, trustzone.Level(lv).String(), st, src})
	}
	writeJSON(w, 200, map[string]any{"owner": owner, "relations": out})
}

func (d *Deps) upsertRelation(w http.ResponseWriter, r *http.Request) {
	if d.DB == nil {
		writeJSON(w, 503, map[string]any{"error": "db unavailable"})
		return
	}
	var req struct {
		Owner  string `json:"owner"`
		Peer   string `json:"peer"`
		Level  int    `json:"level"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad body")
		return
	}
	if req.Owner == "" || req.Peer == "" {
		writeErr(w, 400, "missing owner/peer")
		return
	}
	if req.Level < 0 || req.Level > 4 {
		writeErr(w, 400, "level must be 0..4")
		return
	}
	status := req.Status
	if status == "" {
		status = "friend"
	}
	upsimRelation(d.DB, req.Owner, req.Peer, req.Level, status, "manual")
	writeJSON(w, 200, map[string]any{"ok": true, "owner": req.Owner, "peer": req.Peer,
		"level": req.Level, "levelName": trustzone.Level(req.Level).String(), "status": status})
}

// applyReadReceipt v1.7.2：C2C 已读回执真实落 im_read_receipts。
func applyReadReceipt(db *sql.DB, ev im.WebhookEvent) {
	if !containsAny(ev.CallbackType, "ReadReport", "ReadReceipt", "Readed", "MsgReaded", "C2C.Read") {
		return
	}
	owner, peer := ev.FromUserID, ev.ToUserID
	if owner == "" || peer == "" {
		return
	}
	key := ev.MsgKey
	if key == "" {
		key = "single"
	}
	_, err := db.Exec(`
INSERT INTO im_read_receipts(owner_id, peer_id, msg_key, source)
VALUES ($1,$2,$3,'webhook')
ON CONFLICT (owner_id, peer_id, msg_key) DO UPDATE SET read_at=now()`,
		owner, peer, key)
	if err != nil {
		log.Printf("im_read_receipts upsert %s->%s: %v", owner, peer, err)
	}
}

// applyGroupMembership v1.7.2：群成员加/退回调真实同步 im_group_members。
func applyGroupMembership(db *sql.DB, ev im.WebhookEvent) {
	if ev.GroupID == "" {
		return
	}
	member := ev.ToUserID
	if member == "" {
		member = ev.FromUserID
	}
	if member == "" {
		return
	}
	switch {
	case containsAny(ev.CallbackType, "AddMember", "NewJoin", "JoinGroup", "GroupJoin"):
		_, err := db.Exec(`INSERT INTO im_group_members(group_id, user_id, role)
VALUES ($1,$2,'member') ON CONFLICT (group_id, user_id) DO NOTHING`, ev.GroupID, member)
		if err != nil {
			log.Printf("group add member %s/%s: %v", ev.GroupID, member, err)
		}
	case containsAny(ev.CallbackType, "DeleteMember", "QuitGroup", "GroupQuit", "RemoveMember"):
		_, err := db.Exec(`DELETE FROM im_group_members WHERE group_id=$1 AND user_id=$2`, ev.GroupID, member)
		if err != nil {
			log.Printf("group delete member %s/%s: %v", ev.GroupID, member, err)
		}
	}
}

// v1.7.5：普通 C2C 消息入站镜像为 recv（群消息/已读/关系事件不进此表）。
func applyInboundMessage(db *sql.DB, ev im.WebhookEvent) {
	if ev.GroupID != "" || ev.MsgKey == "" || ev.FromUserID == "" || ev.ToUserID == "" {
		return
	}
	text := extractInboundText(ev.MsgBody)
	if _, err := db.Exec(`INSERT INTO im_messages(msg_key,from_user,to_user,dir,text)
		VALUES($1,$2,$3,'recv',$4) ON CONFLICT (msg_key) DO NOTHING`,
		ev.MsgKey, ev.FromUserID, ev.ToUserID, text); err != nil {
		log.Printf("inbound mirror im_messages msg_key=%s: %v", ev.MsgKey, err)
	}
}

// 从腾讯云 MsgBody 尽力提取文本（结构可能是 [{MsgContent:{Text:...}}]）。
func extractInboundText(body []any) string {
	for _, b := range body {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if mc, ok := m["MsgContent"].(map[string]any); ok {
			if t, ok := mc["Text"].(string); ok {
				return t
			}
		}
		if t, ok := m["Text"].(string); ok {
			return t
		}
	}
	return ""
}

// GET /v1/im/messages?user_id=&peer= 双向会话历史（最近 100 条）。
func (d *Deps) listIMMessages(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("user_id")
	peer := r.URL.Query().Get("peer")
	if uid == "" || peer == "" {
		writeErr(w, 400, "user_id and peer required")
		return
	}
	rows, err := d.DB.Query(`SELECT msg_key, from_user, to_user, dir, text, extract(epoch from created_at)::bigint
		FROM im_messages
		WHERE (from_user=$1 AND to_user=$2) OR (from_user=$2 AND to_user=$1)
		ORDER BY created_at DESC LIMIT 100`, uid, peer)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var mk, fr, to, dir, txt string
		var at int64
		_ = rows.Scan(&mk, &fr, &to, &dir, &txt, &at)
		out = append(out, map[string]any{"msg_key": mk, "from": fr, "to": to, "dir": dir, "text": txt, "at": at})
	}
	writeJSON(w, 200, map[string]any{"user_id": uid, "peer": peer, "messages": out})
}

// GET /v1/im/unread?user_id= 该用户收到但未读的消息数。
func (d *Deps) imUnreadCount(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		writeErr(w, 400, "user_id required")
		return
	}
	var n int
	err := d.DB.QueryRow(`SELECT count(*) FROM im_messages m
		WHERE m.to_user=$1 AND m.dir='recv'
		  AND NOT EXISTS (SELECT 1 FROM im_read_receipts rr
		                  WHERE rr.owner_id=$1 AND rr.msg_key=m.msg_key)`, uid).Scan(&n)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"user_id": uid, "unread": n})
}
