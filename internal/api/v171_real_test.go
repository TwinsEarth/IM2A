package api

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// v1.7.1：加好友回调 → 自动落 im_relations 且默认 L3；删好友回调 → L4/deleted；
// 手动设档 API 真实落库；列表回读真实档位；越界 level 拒绝。
func TestV171_FriendWebhookDrivesTrustLevel(t *testing.T) {
	d := v12Deps(t)
	h := d.IMMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanIMBySuf(t, d.DB, suf) })
	owner := "v171_owner_" + suf
	peer := "v171_peer_" + suf

	postWebhook := func(cmd, from, to, msgKey string) *httptest.ResponseRecorder {
		body := []byte(fmt.Sprintf(`{"CallbackCommand":%q,"From_Account":%q,"To_Account":%q,"MsgKey":%q,"MsgBody":[]}`,
			cmd, from, to, msgKey))
		req := httptest.NewRequest("POST", "/v1/im/webhook?key=v12-wh-secret", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1) 加好友
	if rec := postWebhook("FriendAdd", owner, peer, "v171add_"+suf); rec.Code != 200 {
		t.Fatalf("FriendAdd webhook code=%d %s", rec.Code, rec.Body.String())
	}
	var level int
	var status, source string
	row := d.DB.QueryRow(`SELECT trust_level, status, source FROM im_relations WHERE owner_id=$1 AND peer_id=$2`, owner, peer)
	if err := row.Scan(&level, &status, &source); err != nil {
		t.Fatalf("FriendAdd must persist im_relations: %v", err)
	}
	if level != 3 || status != "friend" || source != "webhook" {
		t.Fatalf("after FriendAdd want level=3/friend/webhook, got %d/%s/%s", level, status, source)
	}

	// 2) 删好友 → 降级 L4 deleted
	if rec := postWebhook("FriendDelete", owner, peer, "v171del_"+suf); rec.Code != 200 {
		t.Fatalf("FriendDelete webhook code=%d", rec.Code)
	}
	row = d.DB.QueryRow(`SELECT trust_level, status FROM im_relations WHERE owner_id=$1 AND peer_id=$2`, owner, peer)
	if err := row.Scan(&level, &status); err != nil {
		t.Fatalf("FriendDelete must update row: %v", err)
	}
	if level != 4 || status != "deleted" {
		t.Fatalf("after FriendDelete want level=4/deleted, got %d/%s", level, status)
	}

	// 3) 手动提档到 L2（手动）
	setBody := []byte(fmt.Sprintf(`{"owner":%q,"peer":%q,"level":2}`, owner, peer))
	req := httptest.NewRequest("POST", "/v1/im/relations", bytes.NewReader(setBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("manual set relation code=%d %s", rec.Code, rec.Body.String())
	}
	row = d.DB.QueryRow(`SELECT trust_level, status, source FROM im_relations WHERE owner_id=$1 AND peer_id=$2`, owner, peer)
	if err := row.Scan(&level, &status, &source); err != nil {
		t.Fatalf("manual set must persist: %v", err)
	}
	if level != 2 || status != "friend" || source != "manual" {
		t.Fatalf("manual want level=2/friend/manual, got %d/%s/%s", level, status, source)
	}

	// 4) 列表回读
	lreq := httptest.NewRequest("GET", "/v1/im/relations?owner="+owner, nil)
	lrec := httptest.NewRecorder()
	h.ServeHTTP(lrec, lreq)
	if lrec.Code != 200 {
		t.Fatalf("list relations code=%d", lrec.Code)
	}
	if !bytes.Contains(lrec.Body.Bytes(), []byte(`"peer":"`+peer+`"`)) ||
		!bytes.Contains(lrec.Body.Bytes(), []byte(`"level":2`)) {
		t.Fatalf("list missing peer/level=2: %s", lrec.Body.String())
	}

	// 5) 越界 level 必须 400（代码层门控，不依赖模型）
	bad := []byte(fmt.Sprintf(`{"owner":%q,"peer":%q,"level":9}`, owner, peer))
	breq := httptest.NewRequest("POST", "/v1/im/relations", bytes.NewReader(bad))
	breq.Header.Set("Content-Type", "application/json")
	brec := httptest.NewRecorder()
	h.ServeHTTP(brec, breq)
	if brec.Code != 400 {
		t.Fatalf("level=9 must be rejected 400, got %d: %s", brec.Code, brec.Body.String())
	}
}
