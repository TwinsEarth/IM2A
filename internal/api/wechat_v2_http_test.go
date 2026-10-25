package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestWechatV2MuxHTTP(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, err := sql.Open("pgx", dsn)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { d.Close() })
	deps := &Deps{DB: d}
	srv := httptest.NewServer(deps.WechatV2Mux())
	defer srv.Close()
	ctx := context.Background()
	suf := fmt.Sprintf("v2mux_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM bot_route_messages WHERE from_wxid LIKE $1 OR to_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM temp_messages WHERE group_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM temp_group_members WHERE group_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM temp_groups WHERE id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM agent_reports WHERE user_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM agent_quota_usage WHERE user_id > 900000`)
	})

	post := func(path string, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil { t.Fatal(err) }
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(srv.URL + path)
		if err != nil { t.Fatal(err) }
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}

	// 1. identify
	code, m := post("/v1/wechat/identify", fmt.Sprintf(`{"wxid":%q}`, suf+"_a"))
	if code != 200 || m["userId"] == nil {
		t.Fatalf("identify code=%d m=%v", code, m)
	}
	uid := int64(m["userId"].(float64))

	// 2. upgrade pro
	code, _ = post("/v1/quota/upgrade", fmt.Sprintf(`{"userId":%d,"plan":"pro"}`, uid))
	if code != 200 { t.Fatalf("upgrade code=%d", code) }
	// 3. quota 查
	code, m = get(fmt.Sprintf("/v1/quota/%d", uid))
	if code != 200 || m["plan"] != "pro" {
		t.Fatalf("quota code=%d m=%v", code, m)
	}

	// 4. 临时群 create
	gid := suf + "_g"
	code, _ = post("/v1/tempgroup/create", fmt.Sprintf(`{"id":%q,"topic":"测试","createdBy":%q}`, gid, "agent-main"))
	if code != 201 { t.Fatalf("group create code=%d", code) }
	// 5. 销毁
	code, _ = post("/v1/tempgroup/destroy", fmt.Sprintf(`{"groupId":%q}`, gid))
	if code != 200 { t.Fatalf("destroy code=%d", code) }

	// 6. 汇报 generate
	code, m = post("/v1/report/generate", fmt.Sprintf(`{"userId":%q}`, suf+"_user"))
	if code != 201 || m["ID"] == nil && m["id"] == nil {
		t.Fatalf("report code=%d m=%v", code, m)
	}
	// report struct 返回的 ID 字段（大写 key JSON 是 id? 无 tag）
	_ = ctx
}
