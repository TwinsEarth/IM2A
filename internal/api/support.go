package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// v1.7.8 客服工单：用户反馈 → 工单 → 处理闭环。
// 状态：1 待处理 / 2 处理中 / 3 已解决 / 4 已关闭；回复方：1 用户 / 2 客服。
type supportTicket struct {
	ID         int64          `json:"id"`
	TicketNo   string         `json:"ticket_no"`
	UserID     int64          `json:"user_id"`
	Contact    string         `json:"contact"`
	Category   string         `json:"category"`
	Subject    string         `json:"subject"`
	Content    string         `json:"content"`
	Priority   int            `json:"priority"`
	Status     int            `json:"status"`
	Assignee   string         `json:"assignee"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
	ResolvedAt string         `json:"resolved_at"`
	Replies    []supportReply `json:"replies,omitempty"`
}

type supportReply struct {
	ID         int64  `json:"id"`
	TicketID   int64  `json:"ticket_id"`
	AuthorType int    `json:"author_type"`
	Author     string `json:"author"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at"`
}

// SupportMux 用户侧：提交 / 查询 / 补充。
func (d *Deps) SupportMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/support/tickets", d.supportCreate)
	mux.HandleFunc("GET /v1/support/tickets", d.supportList)
	mux.HandleFunc("GET /v1/support/tickets/{id}", d.supportDetail)
	mux.HandleFunc("POST /v1/support/tickets/{id}/reply", d.supportUserReply)
	return mux
}

// AdminSupportMux 注册到 AdminMux 的客服处理路由。
func (d *Deps) adminSupportRegister(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/tickets", d.adminSupportList)
	mux.HandleFunc("GET /v1/admin/tickets/{id}", d.adminSupportDetail)
	mux.HandleFunc("POST /v1/admin/tickets/{id}/reply", d.adminSupportReply)
	mux.HandleFunc("POST /v1/admin/tickets/{id}/status", d.adminSupportStatus)
}

func (d *Deps) fetchReplies(ctx context.Context, ticketID int64) []supportReply {
	rows, err := d.DB.QueryContext(ctx, `SELECT id, ticket_id, author_type, COALESCE(author,''), content, COALESCE(created_at::text,'') FROM support_ticket_replies WHERE ticket_id=$1 ORDER BY id ASC`, ticketID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []supportReply{}
	for rows.Next() {
		var r supportReply
		if err := rows.Scan(&r.ID, &r.TicketID, &r.AuthorType, &r.Author, &r.Content, &r.CreatedAt); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func (d *Deps) scanTicket(scan interface{ Scan(dest ...any) error }) (*supportTicket, error) {
	var t supportTicket
	var resolved *string
	if err := scan.Scan(&t.ID, &t.TicketNo, &t.UserID, &t.Contact, &t.Category, &t.Subject, &t.Content,
		&t.Priority, &t.Status, &t.Assignee, &t.CreatedAt, &t.UpdatedAt, &resolved); err != nil {
		return nil, err
	}
	if resolved != nil {
		t.ResolvedAt = *resolved
	}
	return &t, nil
}

const supportCols = `id, ticket_no, COALESCE(user_id,0), COALESCE(contact,''), category, subject, content, priority, status, COALESCE(assignee,''), COALESCE(created_at::text,''), COALESCE(updated_at::text,''), resolved_at::text`

func (d *Deps) supportCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID   int64  `json:"user_id"`
		Contact  string `json:"contact"`
		Category string `json:"category"`
		Subject  string `json:"subject"`
		Content  string `json:"content"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Subject) == "" || strings.TrimSpace(req.Content) == "" {
		writeErr(w, 400, "subject and content required")
		return
	}
	if req.Category == "" {
		req.Category = "other"
	}
	if req.Priority < 1 || req.Priority > 3 {
		req.Priority = 1
	}
	var id int64
	tmp := fmt.Sprintf("NEW-%d", time.Now().UnixNano())
	if err := d.DB.QueryRowContext(r.Context(),
		`INSERT INTO support_tickets(ticket_no,user_id,contact,category,subject,content,priority) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		tmp, req.UserID, req.Contact, req.Category, req.Subject, req.Content, req.Priority).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ticketNo := fmt.Sprintf("ST-%s-%05d", time.Now().Format("20060102"), id)
	if _, err := d.DB.ExecContext(r.Context(), `UPDATE support_tickets SET ticket_no=$1 WHERE id=$2`, ticketNo, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 写入审计事件
	d.DB.ExecContext(r.Context(), `INSERT INTO audit_events(occurred_at,trace_id,action,decision,detail) VALUES(now(),$1,'support_ticket','CREATED',$2)`,
		ticketNo, `{"id":`+strconv.FormatInt(id, 10)+`,"category":"`+req.Category+`"}`)
	t, err := d.fetchTicketByID(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (d *Deps) fetchTicketByID(ctx context.Context, id int64) (*supportTicket, error) {
	t, err := d.scanTicket(d.DB.QueryRowContext(ctx, `SELECT `+supportCols+` FROM support_tickets WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	t.Replies = d.fetchReplies(ctx, id)
	return t, nil
}

func (d *Deps) supportList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var rows interface {
		Close() error
		Next() bool
		Scan(dest ...any) error
	}
	var err error
	if uid := q.Get("user_id"); uid != "" {
		rows, err = d.DB.QueryContext(r.Context(), `SELECT `+supportCols+` FROM support_tickets WHERE user_id=$1 ORDER BY id DESC LIMIT 100`, uid)
	} else {
		rows, err = d.DB.QueryContext(r.Context(), `SELECT `+supportCols+` FROM support_tickets ORDER BY id DESC LIMIT 100`)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []supportTicket{}
	for rows.Next() {
		t, err := d.scanTicket(rows)
		if err != nil {
			continue
		}
		t.Replies = d.fetchReplies(r.Context(), t.ID)
		out = append(out, *t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "tickets": out})
}

func (d *Deps) supportDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad ticket id")
		return
	}
	t, err := d.fetchTicketByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "ticket not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (d *Deps) supportUserReply(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad ticket id")
		return
	}
	var req struct {
		Author  string `json:"author"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		writeErr(w, 400, "content required")
		return
	}
	if _, err := d.DB.ExecContext(r.Context(), `INSERT INTO support_ticket_replies(ticket_id,author_type,author,content) VALUES($1,1,$2,$3)`, id, req.Author, req.Content); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 用户补充后，若已解决/关闭则重新打开为处理中。
	d.DB.ExecContext(r.Context(), `UPDATE support_tickets SET updated_at=now(), status=CASE WHEN status IN (3,4) THEN 2 ELSE status END, resolved_at=CASE WHEN status IN (3,4) THEN NULL ELSE resolved_at END WHERE id=$1`, id)
	t, err := d.fetchTicketByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "ticket not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---- admin 侧 ----

func (d *Deps) adminSupportList(w http.ResponseWriter, r *http.Request) {
	where := ""
	args := []any{}
	if s := r.URL.Query().Get("status"); s != "" {
		where = " WHERE status=$1"
		args = append(args, s)
	}
	args = append(args, atoiDefault(r.URL.Query().Get("limit"), 50))
	rows, err := d.DB.QueryContext(r.Context(), `SELECT `+supportCols+` FROM support_tickets`+where+` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []supportTicket{}
	for rows.Next() {
		t, err := d.scanTicket(rows)
		if err != nil {
			continue
		}
		t.Replies = d.fetchReplies(r.Context(), t.ID)
		out = append(out, *t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "tickets": out})
}

func (d *Deps) adminSupportDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	t, err := d.fetchTicketByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (d *Deps) adminSupportReply(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Author   string `json:"author"`
		Assignee string `json:"assignee"`
		Content  string `json:"content"`
		Status   int    `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		writeErr(w, 400, "content required")
		return
	}
	// 客服回复后默认进入处理中，除非显式置为已解决/关闭。
	status := req.Status
	if status == 0 {
		status = 2
	}
	if status < 1 || status > 4 {
		writeErr(w, 400, "bad status")
		return
	}
	if _, err := d.DB.ExecContext(r.Context(), `INSERT INTO support_ticket_replies(ticket_id,author_type,author,content) VALUES($1,2,$2,$3)`, id, req.Author, req.Content); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := d.DB.ExecContext(r.Context(),
		`UPDATE support_tickets SET status=$1, assignee=COALESCE(NULLIF($2,''),assignee), updated_at=now(), resolved_at=CASE WHEN $4 IN (3,4) THEN now() ELSE NULL END WHERE id=$3`,
		status, req.Assignee, id, status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t, err := d.fetchTicketByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (d *Deps) adminSupportStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Status   int    `json:"status"`
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status < 1 || req.Status > 4 {
		writeErr(w, 400, "status 1-4 required")
		return
	}
	if _, err := d.DB.ExecContext(r.Context(),
		`UPDATE support_tickets SET status=$1, assignee=COALESCE(NULLIF($2,''),assignee), updated_at=now(), resolved_at=CASE WHEN $4 IN (3,4) THEN now() ELSE NULL END WHERE id=$3`,
		req.Status, req.Assignee, id, req.Status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t, err := d.fetchTicketByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}
