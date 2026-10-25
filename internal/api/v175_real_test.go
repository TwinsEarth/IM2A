package api

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// v1.7.5：入站 C2C 消息真实镜像落 im_messages；未读数随已读回执归零；历史双向可查。
func TestV175_InboundMirrorHistoryUnread(t *testing.T) {
	d := v12Deps(t)
	suf := fmt.Sprintf("%d", time.Now().Unix())
	t.Cleanup(func() { cleanIMBySuf(t, d.DB, suf) })
	from := "v175_from_" + suf
	to := "v175_to_" + suf
	mk := "v175_mk_" + suf
	h := d.IMMux()

	post := func(path string, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	// 1) 真实腾讯云入站 webhook：一条 C2C 文本消息
	wh := fmt.Sprintf(`{"CallbackCommand":"C2C.Callback.AfterSendMsg","From_Account":%q,"To_Account":%q,"GroupId":"","MsgKey":%q,"MsgBody":[{"MsgContent":{"Text":"你好"}}]}`, from, to, mk)
	rec := post("/v1/im/webhook?key=v12-wh-secret", wh)
	if rec.Code != 200 {
		t.Fatalf("webhook code=%d %s", rec.Code, rec.Body.String())
	}

	// 2) im_messages recv 落库且文本被提取
	var dir, text string
	err := d.DB.QueryRow(`SELECT dir, text FROM im_messages WHERE msg_key=$1`, mk).Scan(&dir, &text)
	if err != nil {
		t.Fatalf("inbound message not mirrored: %v", err)
	}
	if dir != "recv" || text != "你好" {
		t.Fatalf("mirror wrong: dir=%q text=%q", dir, text)
	}

	// 3) 未读数 = 1（to 收到未读）
	rec = get("/v1/im/unread?user_id=" + to)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unread":1`) {
		t.Fatalf("unread must be 1, code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 4) 已读回执后未读 = 0
	_, err = d.DB.Exec(`INSERT INTO im_read_receipts(owner_id,peer_id,msg_key,source) VALUES($1,$2,$3,'webhook')`, to, from, mk)
	if err != nil {
		t.Fatalf("insert receipt: %v", err)
	}
	rec = get("/v1/im/unread?user_id=" + to)
	if !strings.Contains(rec.Body.String(), `"unread":0`) {
		t.Fatalf("unread must be 0 after read receipt, body=%s", rec.Body.String())
	}

	// 5) 双向历史可查
	rec = get("/v1/im/messages?user_id=" + to + "&peer=" + from)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), mk) || !strings.Contains(rec.Body.String(), "你好") {
		t.Fatalf("history query wrong: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
