// Package push v1.7.3：腾讯云离线推送。
// 真实逻辑：推送任务落 PG 队列；Dispatch 时按凭据调用腾讯云 IM 离线推送 REST。
// 证书未接入时任务保持 pending 并记录 last_error，绝不 panic、绝不静默丢消息。
package push

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// Credential 推送凭据（无证书时 CertRef 为空，仍可落库占位）。
type Credential struct {
	UserID      string
	Platform    string
	CertRef     string
	DeviceToken string
}

// OfflinePushMsg 待推送内容。
type OfflinePushMsg struct {
	UserID string
	MsgKey string
	Title  string
	Body   string
}

// Enqueue 离线消息落推送队列（pending）。真实落库。
func Enqueue(ctx context.Context, db *sql.DB, m OfflinePushMsg) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO im_push_tasks(user_id, msg_key, title, body) VALUES($1,$2,$3,$4)`,
		m.UserID, m.MsgKey, m.Title, m.Body)
	return err
}

// Client 腾讯云离线推送 REST 客户端。BaseURL 可注入（测试指向 httptest）。
type Client struct {
	BaseURL  string
	SDKAppID string
	HTTP     *http.Client
}

// SendOfflinePush 真实 HTTP 调用腾讯云离线推送端点。
func (cl *Client) SendOfflinePush(ctx context.Context, cred Credential, m OfflinePushMsg) (*http.Response, error) {
	if cl.BaseURL == "" {
		return nil, fmt.Errorf("push: empty base url")
	}
	payload := map[string]any{
		"UserIDList": []string{m.UserID},
		"MsgKey":     m.MsgKey,
		"OfflinePushInfo": map[string]any{
			"Title": m.Title,
			"Desc":  m.Body,
			"PushInfo": map[string]any{
				"Platform":    cred.Platform,
				"CertRef":     cred.CertRef,
				"DeviceToken": cred.DeviceToken,
			},
		},
	}
	b, _ := json.Marshal(payload)
	url := cl.BaseURL + "/v4/openim/offlinepush?sdkappid=" + cl.SDKAppID
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return cl.HTTP.Do(req)
}

// DispatchPending 取出全部 pending 任务并真实下发（后台调度 ticker 用）。
// 多实例并发安全：事务内 SELECT ... FOR UPDATE SKIP LOCKED 抢占任务，
// 同一时刻每条任务只被一个调度者处理，避免网关多实例重复推送。
func DispatchPending(ctx context.Context, db *sql.DB, cl *Client) (sent, attempted int, err error) {
	return dispatch(ctx, db, cl, "")
}

// DispatchUserPending 仅调度指定用户的 pending 任务（运营"手动重试该用户"使用），
// 绝不越权推送其他用户的任务。
func DispatchUserPending(ctx context.Context, db *sql.DB, cl *Client, userID string) (sent, attempted int, err error) {
	if userID == "" {
		return 0, 0, fmt.Errorf("push: empty user id")
	}
	return dispatch(ctx, db, cl, userID)
}

// dispatch 是全量/按用户调度的共同实现。userID 为空表示全量。
// 整批在单个事务中处理：行锁随事务持有到 commit，配合 SKIP LOCKED 实现跨实例去重；
// 失败任务只累加 attempts/last_error、不改 status（保持 pending 供下轮重试）。
func dispatch(ctx context.Context, db *sql.DB, cl *Client, userID string) (sent, attempted int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	query := `SELECT id, user_id, msg_key, title, body FROM im_push_tasks WHERE status='pending'`
	args := []any{}
	if userID != "" {
		query += ` AND user_id=$1`
		args = append(args, userID)
	}
	query += ` ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, 0, err
	}
	type task struct {
		id                          int64
		userID, msgKey, title, body string
	}
	var tasks []task
	for rows.Next() {
		var tk task
		if e := rows.Scan(&tk.id, &tk.userID, &tk.msgKey, &tk.title, &tk.body); e != nil {
			rows.Close()
			return sent, attempted, e
		}
		tasks = append(tasks, tk)
	}
	rows.Close()

	for _, tk := range tasks {
		attempted++
		var cred Credential
		cre := tx.QueryRowContext(ctx,
			`SELECT user_id, platform, COALESCE(cert_ref,''), COALESCE(device_token,'')
			 FROM im_push_credentials WHERE user_id=$1 ORDER BY platform LIMIT 1`, tk.userID)
		if e := cre.Scan(&cred.UserID, &cred.Platform, &cred.CertRef, &cred.DeviceToken); e != nil {
			if _, e2 := tx.ExecContext(ctx,
				`UPDATE im_push_tasks SET attempts=attempts+1, last_error=$2, updated_at=now() WHERE id=$1`,
				tk.id, "no_credential"); e2 != nil {
				log.Printf("push task %d mark no_credential: %v", tk.id, e2)
			}
			continue
		}
		if cred.CertRef == "" {
			if _, e2 := tx.ExecContext(ctx,
				`UPDATE im_push_tasks SET attempts=attempts+1, last_error=$2, updated_at=now() WHERE id=$1`,
				tk.id, "cert_not_configured"); e2 != nil {
				log.Printf("push task %d mark cert_not_configured: %v", tk.id, e2)
			}
			continue
		}
		resp, e := cl.SendOfflinePush(ctx, cred, OfflinePushMsg{tk.userID, tk.msgKey, tk.title, tk.body})
		if e != nil || resp == nil || resp.StatusCode >= 300 {
			msg := "http_error"
			if resp != nil {
				msg = fmt.Sprintf("http_%d", resp.StatusCode)
				resp.Body.Close()
			}
			if _, e2 := tx.ExecContext(ctx,
				`UPDATE im_push_tasks SET attempts=attempts+1, last_error=$2, updated_at=now() WHERE id=$1`,
				tk.id, msg); e2 != nil {
				log.Printf("push task %d mark %s: %v", tk.id, msg, e2)
			}
			log.Printf("push task %d failed: %s", tk.id, msg)
			continue
		}
		resp.Body.Close()
		if _, e2 := tx.ExecContext(ctx,
			`UPDATE im_push_tasks SET status='sent', last_error='', updated_at=now() WHERE id=$1`, tk.id); e2 != nil {
			log.Printf("push task %d mark sent: %v", tk.id, e2)
		}
		sent++
	}

	if err := tx.Commit(); err != nil {
		return sent, attempted, err
	}
	committed = true
	return sent, attempted, nil
}
