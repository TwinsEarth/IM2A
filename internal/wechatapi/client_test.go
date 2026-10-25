package wechatapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSend(t *testing.T) {
	// 生成密钥对
	pub, priv, _ := ed25519.GenerateKey(nil)
	wxid := "wx_sdk_test"

	var got Envelope
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBody, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"ack","echo_type":"` + got.Type + `"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Wxid: wxid, Priv: priv}
	resp, err := c.Send(context.Background(), "agent.create", map[string]string{"role": "sub"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !json.Valid(resp) {
		t.Fatalf("resp not json: %s", resp)
	}

	// 断言信封字段
	if got.Wxid != wxid {
		t.Errorf("wxid=%s", got.Wxid)
	}
	if got.Type != "agent.create" {
		t.Errorf("type=%s", got.Type)
	}
	if got.Idem == "" {
		t.Error("idem empty")
	}
	if got.Ts == 0 {
		t.Error("ts zero")
	}
	if string(got.Payload) == "" {
		t.Error("payload empty")
	}

	// 签名可被对应公钥验过
	sig, _ := base64.StdEncoding.DecodeString(got.Sig)
	if !ed25519.Verify(pub, got.canonical(), sig) {
		t.Error("signature invalid")
	}
}
