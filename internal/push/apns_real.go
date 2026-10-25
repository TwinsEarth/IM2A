package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	apns "github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
)

// APNSConfig 从环境变量读取真实 Apple 凭据（.p8 Auth Key 方式）。
type APNSConfig struct {
	AuthKeyP8 string // .p8 文件完整内容（含 -----BEGIN PRIVATE KEY-----）
	KeyID     string // APNs Key ID（如 ABC123DEFG）
	TeamID    string // Apple Team ID（如 TEAM123456）
	BundleID  string // App Bundle ID（apns-topic）
	Sandbox   bool   // true=api.sandbox.push.apple.com, false=api.push.apple.com
}

func APNSConfigFromEnv() APNSConfig {
	sb, _ := strconv.ParseBool(os.Getenv("APNS_SANDBOX"))
	return APNSConfig{
		AuthKeyP8: os.Getenv("APNS_AUTH_KEY_P8"),
		KeyID:     os.Getenv("APNS_KEY_ID"),
		TeamID:    os.Getenv("APNS_TEAM_ID"),
		BundleID:  os.Getenv("APNS_BUNDLE_ID"),
		Sandbox:   sb,
	}
}

func (cfg APNSConfig) Ready() bool {
	return cfg.AuthKeyP8 != "" && cfg.KeyID != "" && cfg.TeamID != "" && cfg.BundleID != ""
}

var errNotConfigured = errors.New("push: APNS 凭据未配置（APNS_AUTH_KEY_P8/KEY_ID/TEAM_ID/BUNDLE_ID）")

type APNSResult struct {
	StatusCode int    `json:"statusCode"`
	Reason     string `json:"reason"`
	ApnsID     string `json:"apnsId"`
	Accepted   bool   `json:"accepted"`
}

// SendAPNS 真实投递到 Apple Push Notification service（非 mock）。
func SendAPNS(ctx context.Context, cfg APNSConfig, deviceToken, title, body string, badge int) (*APNSResult, error) {
	if !cfg.Ready() {
		return nil, errNotConfigured
	}
	if deviceToken == "" {
		return nil, errors.New("push: empty device token")
	}
	authKey, err := token.AuthKeyFromBytes([]byte(cfg.AuthKeyP8))
	if err != nil {
		return nil, fmt.Errorf("push: 解析 .p8 失败: %w", err)
	}
	tok := &token.Token{AuthKey: authKey, KeyID: cfg.KeyID, TeamID: cfg.TeamID}
	client := apns.NewTokenClient(tok)
	if cfg.Sandbox {
		client.Development()
	} else {
		client.Production()
	}
	payload, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": title, "body": body},
			"badge": badge,
			"sound": "default",
		},
	})
	n := &apns.Notification{
		DeviceToken: deviceToken,
		Topic:       cfg.BundleID,
		Payload:     payload,
		Expiration:  time.Now().Add(time.Hour),
	}
	res, err := client.Push(n)
	if err != nil {
		return nil, fmt.Errorf("push: 调用 Apple 失败: %w", err)
	}
	return &APNSResult{
		StatusCode: res.StatusCode,
		Reason:     res.Reason,
		ApnsID:     res.ApnsID,
		Accepted:   res.Sent(),
	}, nil
}
