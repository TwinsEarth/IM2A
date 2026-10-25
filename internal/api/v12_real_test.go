package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"human-agent-im/internal/im"
)

// v12Deps 构造一个仅启用 webhook 验签、但不配置腾讯云发送凭证的真实 PG 依赖。
func v12Deps(t *testing.T) *Deps {
	t.Helper()
	t.Setenv("TIM_SECRET_KEY", "")
	t.Setenv("TIM_SDK_APP_ID", "")
	t.Setenv("TIM_ENDPOINT", "")
	t.Setenv("TIM_WEBHOOK_SECRET", "v12-wh-secret")
	return newDBDeps(t)
}

// v1.2.4：建群真实落 im_groups/im_group_members；成员列表可回读；
// 群广播真实落 im_group_messages；非成员发送被拒且不落库。
func TestV124_GroupCreatePersistAndBroadcast(t *testing.T) {
	d := v12Deps(t)
	h := d.IMMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanIMBySuf(t, d.DB, suf) })
	gid := "v12g_" + suf
	owner := "v12_owner_" + suf
	bob := "v12_bob_" + suf
	carol := "v12_carol_" + suf

	// 建群
	body := fmt.Sprintf(`{"groupId":%q,"name":"family","ownerId":%q,"members":[%q,%q]}`, gid, owner, bob, carol)
	req := httptest.NewRequest("POST", "/v1/im/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 真实落库断言
	var gcount, mcount int
	d.DB.QueryRow("SELECT count(*) FROM im_groups WHERE group_id=$1", gid).Scan(&gcount)
	d.DB.QueryRow("SELECT count(*) FROM im_group_members WHERE group_id=$1", gid).Scan(&mcount)
	if gcount != 1 || mcount != 3 {
		t.Fatalf("group/members persist wrong: groups=%d members=%d", gcount, mcount)
	}
	var ownerRole string
	d.DB.QueryRow("SELECT role FROM im_group_members WHERE group_id=$1 AND user_id=$2", gid, owner).Scan(&ownerRole)
	if ownerRole != "owner" {
		t.Fatalf("owner role want owner, got %q", ownerRole)
	}

	// 成员列表回读
	req2 := httptest.NewRequest("GET", "/v1/im/groups/"+gid+"/members", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != 200 || rec2.Body.String() == "" {
		t.Fatalf("members list code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(bob)) {
		t.Fatalf("member list missing bob: %s", rec2.Body.String())
	}

	// 非成员广播 → 403 且不落库
	badBody := fmt.Sprintf(`{"from":%q,"text":"spam"}`, "v12_stranger_"+suf)
	req3 := httptest.NewRequest("POST", "/v1/im/groups/"+gid+"/broadcast", bytes.NewReader([]byte(badBody)))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("non-member broadcast should be 403, got %d: %s", rec3.Code, rec3.Body.String())
	}

	// 成员广播 → 200 且 im_group_messages 落库
	okBody := fmt.Sprintf(`{"from":%q,"text":"dinner at 7"}`, owner)
	req4 := httptest.NewRequest("POST", "/v1/im/groups/"+gid+"/broadcast", bytes.NewReader([]byte(okBody)))
	req4.Header.Set("Content-Type", "application/json")
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("member broadcast code=%d body=%s", rec4.Code, rec4.Body.String())
	}
	if !bytes.Contains(rec4.Body.Bytes(), []byte(`"pushedCount":2`)) {
		t.Fatalf("should push to 2 other members: %s", rec4.Body.String())
	}
	var msgCount int
	d.DB.QueryRow("SELECT count(*) FROM im_group_messages WHERE group_id=$1 AND from_user=$2 AND text=$3",
		gid, owner, "dinner at 7").Scan(&msgCount)
	if msgCount != 1 {
		t.Fatalf("group message must be persisted exactly once, got %d", msgCount)
	}
}

// v1.2.2：webhook 验签 + MsgKey 幂等（重投只落一条）+ 错签名 401。
func TestV122_WebhookSignatureAndIdempotency(t *testing.T) {
	d := v12Deps(t)
	h := d.IMMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanIMBySuf(t, d.DB, suf) })
	key := "v12key_" + suf
	raw := []byte(fmt.Sprintf(`{"CallbackType":"C2C.CallbackAfterMsg","From_Account":"v12a_%s","To_Account":"v12b_%s","MsgKey":%q}`, suf, suf, key))
	ts := time.Now().Unix()
	sig := im.SignWebhook("v12-wh-secret", ts, raw)

	post := func(body []byte, sigHeader string, tsHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/im/webhook", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if sigHeader != "" {
			req.Header.Set("X-TIM-Signature", sigHeader)
			req.Header.Set("X-TIM-Timestamp", tsHeader)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 错签名 → 401
	if rec := post(raw, "deadbeef", fmt.Sprintf("%d", ts)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature should be 401, got %d: %s", rec.Code, rec.Body.String())
	}
	// 正确签名 → 200 verified
	rec1 := post(raw, sig, fmt.Sprintf("%d", ts))
	if rec1.Code != http.StatusOK || !bytes.Contains(rec1.Body.Bytes(), []byte(`"verified":true`)) {
		t.Fatalf("valid webhook should be 200 verified: %d %s", rec1.Code, rec1.Body.String())
	}
	// 重投同 MsgKey → 200 duplicated，库里仍只有一条
	rec2 := post(raw, sig, fmt.Sprintf("%d", ts))
	if rec2.Code != http.StatusOK || !bytes.Contains(rec2.Body.Bytes(), []byte(`"duplicated":true`)) {
		t.Fatalf("replayed webhook should be 200 duplicated: %d %s", rec2.Code, rec2.Body.String())
	}
	var n int
	d.DB.QueryRow("SELECT count(*) FROM im_webhook_events WHERE msg_key=$1", key).Scan(&n)
	if n != 1 {
		t.Fatalf("idempotent webhook should persist exactly once, got %d", n)
	}
}

// v1.2.0：未配置腾讯云凭证，发送必须 503，绝不返回固定成功回执。
func TestV120_SendNotConfiguredIs503(t *testing.T) {
	d := v12Deps(t)
	h := d.IMMux()
	req := httptest.NewRequest("POST", "/v1/im/send",
		bytes.NewReader([]byte(`{"from":"a","to":"b","text":"hi"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured TIM send must be 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("gateway-hmac")) {
		t.Fatalf("503 body should expose mode=gateway-hmac: %s", rec.Body.String())
	}
}

// v1.2.3：审批类消息经 dispatch 必须真实落 audit_events（不是内存假分发）。
func TestV123_DispatchApprovalPersistsAudit(t *testing.T) {
	d := v12Deps(t)
	h := d.IMMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	// audit_events append-only，不删除；v12agent_<suf> 唯一标识隔离。
	body := fmt.Sprintf(`{"msgType":"approval_card","from":"v12agent_%s","to":"v12owner_%s","text":"approve"}`, suf, suf)
	req := httptest.NewRequest("POST", "/v1/im/dispatch", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"channel":"approval"`)) {
		t.Fatalf("approval dispatch code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 异步落 audit_events，轮询等待
	deadline := time.Now().Add(3 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		d.DB.QueryRow("SELECT count(*) FROM audit_events WHERE agent_id=$1", "v12agent_"+suf).Scan(&n)
		if n >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n < 1 {
		t.Fatal("approval dispatch must persist a real audit_events row")
	}
}

// v1.2.2：未配置 webhook 密钥时，对外回调端点必须安全默认拒绝（不允许匿名伪造）。
func TestV122_WebhookRequiresSecretByDefault(t *testing.T) {
	d := newDBDeps(t) // 真实 PG
	t.Setenv("TIM_WEBHOOK_SECRET", "")
	t.Setenv("TIM_SECRET_KEY", "")
	t.Setenv("IM2A_WEBHOOK_DEV", "")
	h := d.IMMux()
	req := httptest.NewRequest("POST", "/v1/im/webhook",
		bytes.NewReader([]byte(`{"CallbackType":"C2C.CallbackAfterMsg","From_Account":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("webhook without secret must be 503, got %d: %s", rec.Code, rec.Body.String())
	}
}
