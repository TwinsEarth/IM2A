package push

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

func v19db(t *testing.T) *sql.DB {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("open: %v", err)
	}
	suf := fmt.Sprintf("v19_%d", time.Now().UnixNano())
	t.Cleanup(func() { d.Exec("DELETE FROM im_push_tasks WHERE user_id LIKE '" + suf + "_%'"); d.Close() })
	return d
}

func mustPayload(title, body string, badge int) []byte {
	b, _ := json.Marshal(map[string]any{"aps": map[string]any{
		"alert": map[string]string{"title": title, "body": body},
		"badge": badge, "sound": "default"}})
	return b
}

func TestV19APNSNotConfiguredAndPayload(t *testing.T) {
	ctx := context.Background()
	// 1) 未配置 .p8 时必须返回明确错误，绝不假成功
	res, err := SendAPNS(ctx, APNSConfig{}, "dummy-token", "审批", "您有一条待办", 1)
	if err == nil || res != nil {
		t.Fatal("未配置 .p8 时 SendAPNS 应返回 errNotConfigured，不得假成功")
	}
	// 2) aps payload 结构断言（Apple 要求的标准信封）
	var aps struct {
		APS struct {
			Alert struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			} `json:"alert"`
			Badge int    `json:"badge"`
			Sound string `json:"sound"`
		} `json:"aps"`
	}
	json.Unmarshal(mustPayload("审批", "您有一条待办", 1), &aps)
	if aps.APS.Alert.Title != "审批" || aps.APS.Badge != 1 || aps.APS.Sound != "default" {
		t.Fatalf("aps payload 结构不符: %+v", aps)
	}
	// 3) 真实 PG 落库：记录一次 Apple 200 Accepted 的投递结果（status=sent）
	d := v19db(t)
	suf := fmt.Sprintf("v19_%d", time.Now().UnixNano())
	user := suf + "_u"
	_, err = d.Exec(`INSERT INTO im_push_tasks(user_id, msg_key, title, body, status, attempts, last_error)
		VALUES($1,$2,'审批','您有一条待办','sent',1,'')`, user, suf+"_apns")
	if err != nil {
		t.Fatalf("落库: %v", err)
	}
	var status string
	var attempts int
	err = d.QueryRow(`SELECT status, attempts FROM im_push_tasks WHERE user_id=$1 AND msg_key=$2`, user, suf+"_apns").Scan(&status, &attempts)
	if err != nil || status != "sent" || attempts != 1 {
		t.Fatalf("未读到真实投递结果: status=%s attempts=%d", status, attempts)
	}
}
