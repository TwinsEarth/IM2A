// SPDX-License-Identifier: MIT
//
// Package bootstrap 负责网关进程的装配：环境解析、依赖构建、插件宿主、watcher/health、
// relay/webhook 接线，以及启动期副作用（inqueue worker、审批超时升级）。
//
// Wave A 重构：以上逻辑原先内联在 services/gateway/main.go，逐段搬移、语义不变；
// 行为守门由 internal/kernel/golden 的黑盒快照负责。
package bootstrap

import (
	"errors"
	"human-agent-im/internal/kernel/native"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// B2-a（契约 v2 §1）：CONN_MODE 合法值。
const (
	ConnModeEmbedded   = "embedded"   // 默认：/v1/ws 走 internal/stream.Hub，与 Wave A 逐字一致
	ConnModeStandalone = "standalone" // 独立 conn-gateway 承载 WS：/v1/ws 返回 421 + hint
	ConnModeDual       = "dual"       // 过渡占位：端点语义同 embedded（镜像投递属 B2-b）

	// DefaultConnHint standalone 模式 /v1/ws 421 响应里的 hint（可用 CONN_HINT 覆盖）。
	DefaultConnHint = ":18081"
	// DefaultConnUDSPath 内核→conn-gateway 的 UDS 缺省路径（B2-c；CONN_UDS_PATH 覆盖）。
	DefaultConnUDSPath = "/run/im2a/conn.sock"
)

// ErrConnModeMisconfigured CONN_MODE 非法；由 Assemble 返回，错误信息含合法值列表。
var ErrConnModeMisconfigured = errors.New("invalid CONN_MODE")

// ValidConnModes 返回合法 CONN_MODE 列表（错误信息与文档共用，顺序固定）。
func ValidConnModes() []string {
	return []string{ConnModeEmbedded, ConnModeStandalone, ConnModeDual}
}

// validConnMode 判断 mode 是否为合法值。
func validConnMode(mode string) bool {
	for _, m := range ValidConnModes() {
		if mode == m {
			return true
		}
	}
	return false
}

// Config 是网关运行配置（启动期一次性解析；字段语义与重构前 main() 内联逻辑一致）。
type Config struct {
	Port int

	GatewayToken    string   // GATEWAY_TOKEN：wrap 链的静态 token（空=开发模式跳过认证）
	AdminProbeToken string   // IM2A_ADMIN_TOKEN 原值：/metrics、/healthz、/v1/ops/*、adminAuth 用
	AdminToken      string   // IM2A_ADMIN_TOKEN，空则回退 GatewayToken：/v1/admin/* 等用
	JWTSecret       string   // IM2A_JWT_SECRET
	CorsAllowed     []string // CORS_ALLOWED，缺省两个官方域名
	LimitRPS        int      // RATE_LIMIT_RPS，缺省 100
	LimitBurst      int      // RATE_LIMIT_BURST，缺省 200
	PushBaseURL     string   // TIM_BASE_URL，缺省腾讯云 IM 控制台端点
	WebhookAsync    bool     // WEBHOOK_ASYNC=on

	// B2-a（契约 v2 §1）：连接网关模式与 standalone 提示文案。
	ConnMode string // CONN_MODE：embedded(缺省)|standalone|dual；非法值由 Assemble 报错
	ConnHint string // CONN_HINT：standalone 下 /v1/ws 421 的 hint，缺省 ":18081"

	// B2-c：内核→conn-gateway 的 UDS 路径（standalone/dual 装配事件镜像投递用）。
	// embedded 忽略；缺省 DefaultConnUDSPath。
	ConnUDSPath string // CONN_UDS_PATH

	// C1（task-21）：原生通道能力开关（默认 off，embedded/黄金基线行为不变）。
	// NATIVE_ENABLED=on 时装配 presence 状态机与 /v1/native/presence* 路由。
	NativeEnabled      bool          // NATIVE_ENABLED=on|1|true
	NativePresenceTTL  time.Duration // NATIVE_PRESENCE_TTL（缺省 45s）
	NativePresenceTick time.Duration // NATIVE_PRESENCE_TICK（缺省 5s；仅测试可调小）

	// C4（task-26）：
	//   NativePresenceDownlink：presence 下行通道 "state27"(缺省，kind 27 fanout) |
	//     "ephemeral"(旧:TypePresence 数据帧；网关未实现 27 时降级用)；
	//   NativeAgentTokenTTL：agent token 短 TTL（缺省 10min）。
	NativePresenceDownlink string
	NativeAgentTokenTTL    time.Duration

	// C5b：NATIVE_ROLLOUT 灰度白名单（nil = 未配置 = 全量放行）。
	// 语法：user:<id> / 裸数字 / agent:<agent_id> / device:<deviceID> / *。
	// 未开启 NATIVE_ENABLED 时 /v1/native/* 返回 404 NATIVE_DISABLED（可解释拒绝码）。
	NativeRollout *native.Rollout
}

// Load 从环境变量解析运行配置（语义与重构前 services/gateway/main.go 一致）。
func Load(port int) Config {
	cfg := Config{
		Port:            port,
		GatewayToken:    os.Getenv("GATEWAY_TOKEN"),
		AdminProbeToken: os.Getenv("IM2A_ADMIN_TOKEN"),
		JWTSecret:       os.Getenv("IM2A_JWT_SECRET"),
		CorsAllowed:     []string{"https://geefish.cn", "https://www.geefish.cn"},
		LimitRPS:        100,
		LimitBurst:      200,
	}
	if v := os.Getenv("CORS_ALLOWED"); v != "" {
		cfg.CorsAllowed = strings.Split(v, ",")
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "im2a-dev-jwt-secret-CHANGE-ME"
		log.Println("WARN: IM2A_JWT_SECRET not set; using insecure dev secret")
	}
	if v := os.Getenv("RATE_LIMIT_RPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.LimitRPS = n
		}
	}
	if v := os.Getenv("RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.LimitBurst = n
		}
	}
	// v1.7.6：运营后台 Token 缺省回退网关 Token（判定顺序与重构前一致）。
	cfg.AdminToken = cfg.AdminProbeToken
	if cfg.AdminToken == "" {
		cfg.AdminToken = cfg.GatewayToken
	}
	cfg.PushBaseURL = os.Getenv("TIM_BASE_URL")
	if cfg.PushBaseURL == "" {
		// 与 internal/im 默认端点一致，避免未配置时静默空 URL
		cfg.PushBaseURL = "https://console.tim.qq.com"
	}
	cfg.WebhookAsync = os.Getenv("WEBHOOK_ASYNC") == "on"
	// B2-a（契约 v2 §1）：CONN_MODE 缺省 embedded（=Wave A 行为）；非法值不在此报错，
	// 由 Assemble 前置校验返回 ErrConnModeMisconfigured（见 kernel.go）。
	cfg.ConnMode = strings.ToLower(strings.TrimSpace(os.Getenv("CONN_MODE")))
	if cfg.ConnMode == "" {
		cfg.ConnMode = ConnModeEmbedded
	}
	cfg.ConnHint = strings.TrimSpace(os.Getenv("CONN_HINT"))
	if cfg.ConnHint == "" {
		cfg.ConnHint = DefaultConnHint
	}
	// B2-c：跨进程投递的 UDS 路径（standalone/dual 才使用；embedded 忽略）。
	cfg.ConnUDSPath = strings.TrimSpace(os.Getenv("CONN_UDS_PATH"))
	if cfg.ConnUDSPath == "" {
		cfg.ConnUDSPath = DefaultConnUDSPath
	}
	// C1：原生通道能力开关（默认 off；presence TTL/tick 仅影响 NATIVE_ENABLED=on）。
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NATIVE_ENABLED"))) {
	case "1", "on", "true", "yes":
		cfg.NativeEnabled = true
	}
	cfg.NativePresenceTTL = 45 * time.Second
	cfg.NativePresenceTick = 5 * time.Second
	if v := os.Getenv("NATIVE_PRESENCE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.NativePresenceTTL = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.NativePresenceTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("NATIVE_PRESENCE_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.NativePresenceTick = d
		}
	}
	// C4：presence 下行通道（默认 kind 27；网关未实现时用 ephemeral 降级）。
	cfg.NativePresenceDownlink = strings.ToLower(strings.TrimSpace(os.Getenv("NATIVE_PRESENCE_DOWNLINK")))
	switch cfg.NativePresenceDownlink {
	case "ephemeral", "state27":
	default:
		cfg.NativePresenceDownlink = "state27"
	}
	cfg.NativeRollout = native.ParseRollout(os.Getenv("NATIVE_ROLLOUT"))
	cfg.NativeAgentTokenTTL = 10 * time.Minute
	if v := os.Getenv("NATIVE_AGENT_TOKEN_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.NativeAgentTokenTTL = d
		}
	}
	return cfg
}
