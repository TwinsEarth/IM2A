package wechatapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client 是微信 Bot 端 SDK：自动构造信封、用私钥签名、生成幂等键、POST 到服务器。
// 与服务器 internal/wechatapi.Service.Handle 对端。
type Client struct {
	BaseURL string
	Wxid    string
	Priv    ed25519.PrivateKey
	HTTP    *http.Client
}

// Send 发送一个上行请求。typ 见协议 v2.0.4（bind.confirm/agent.create/approval.decision/...）。
// payload 会被 marshal 为 JSON。返回服务器下行响应。
func (c *Client) Send(ctx context.Context, typ string, payload any) ([]byte, error) {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	payloadB, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	e := Envelope{
		V:       1,
		Wxid:    c.Wxid,
		Ts:      time.Now().Unix(),
		Idem:    newIdem(),
		Type:    typ,
		Payload: payloadB,
	}
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(c.Priv, e.canonical()))
	body, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	url := c.BaseURL + "/v1/wechatbot/inbox"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("wechatapi: http %d: %s", resp.StatusCode, out)
	}
	return out, nil
}

func newIdem() string {
	return fmt.Sprintf("idem-%d-%d", time.Now().UnixNano(), time.Now().UnixMilli()%1000)
}
