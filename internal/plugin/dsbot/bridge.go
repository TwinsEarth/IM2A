// SPDX-License-Identifier: MIT

// 本文件是 DS Bot 的**可选** HTTP 桥接：对"活的 DS Bot / DSH"只做连通性探测与
// 通用消息投递，不运行 DSH、不模拟其内部协议。未配置 DSBOT_ENDPOINT 时不声明
// dsbot.bridge 能力（契约 §2/§4）。
package dsbotplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrBridgeUnavailable 桥接不可用（未配置 DSBOT_ENDPOINT 或 Start 时探测失败）。
var ErrBridgeUnavailable = errors.New("dsbotplugin: bridge unavailable (DSBOT_ENDPOINT unset or probe failed)")

// bridgeRequest 是 IM2A → 活的 DS Bot 的桥接消息体。
// 这是本插件定义的桥接契约（IM2A 侧），不是对上游 DSH/DS Bot HTTP API 的复刻：
// 上游的 Bot 通信走 DSH Session，宿主并无稳定的公共 HTTP 投递接口（契约 §0）。
type bridgeRequest struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// probeBridge 探测 DSBOT_ENDPOINT 是否可达。
//
// 判据（fail-closed）：GET 基址返回 2xx 才算健康；非 2xx、网络错误、超时一律视为不可用。
// 探测本身不携带认证信息，也不猜测上游路由。
func (p *Plugin) probeBridge(ctx context.Context) error {
	if p.endpoint == "" {
		return ErrBridgeUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, p.endpoint, nil)
	if err != nil {
		return fmt.Errorf("bad DSBOT_ENDPOINT %q: %w", p.endpoint, err)
	}
	client := &http.Client{Timeout: p.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe GET %s: %w", p.endpoint, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("probe GET %s: HTTP %d", p.endpoint, resp.StatusCode)
	}
	return nil
}

// BridgeSend 把一条文本投递到已探测通过的 DS Bot 桥接端点（能力 dsbot.bridge 的唯一实现）。
// 仅在 Start 探测通过后可用；失败返回 ErrBridgeUnavailable 或 HTTP 错误。
func (p *Plugin) BridgeSend(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("dsbotplugin: bridge send: empty text")
	}
	p.mu.RLock()
	available := p.bridgeAvailable
	endpoint := p.endpoint
	timeout := p.timeout
	p.mu.RUnlock()
	if !available || endpoint == "" {
		return ErrBridgeUnavailable
	}
	body, err := json.Marshal(bridgeRequest{Text: text, Source: "im2a"})
	if err != nil {
		return fmt.Errorf("dsbotplugin: bridge send: encode: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dsbotplugin: bridge send: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("dsbotplugin: bridge send POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dsbotplugin: bridge send POST %s: HTTP %d: %s",
			endpoint, resp.StatusCode, clipText(string(reply), 200))
	}
	return nil
}

// clipText 折叠空白并截断到 max 个字符（按 rune 截断，避免截坏 UTF-8）。
func clipText(raw string, max int) string {
	s := strings.Join(strings.Fields(raw), " ")
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
