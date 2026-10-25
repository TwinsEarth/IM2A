package openclaw

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AccountNotifier 掉线/失效时的回调（发送"需重扫"提示 + 记录事件）。
type AccountNotifier interface {
	OnOffline(accountID, wxid string)
	OnOnline(accountID string)
}

// HealthChecker 周期性巡检已绑定账号目录：
// 账号文件消失 = 该微信被踢下线/登录态失效（openclaw 会移除对应账号文件）。
// 在线→离线跳变时通知管理员和用户，提示重新扫码。
type HealthChecker struct {
	dir      string
	interval time.Duration
	known    map[string]bool // accountID -> 是否在线
	alerted  map[string]bool // accountID -> 已就 token 失效告警过（防刷屏）
	notify   AccountNotifier
	mu       sync.Mutex
	stopCh   chan struct{}
}

// NewHealthChecker 创建账号健康巡检；dir 为空时使用 ResolveAccountsDir() 解析。
func NewHealthChecker(dir string, interval time.Duration, n AccountNotifier) *HealthChecker {
	if dir == "" {
		dir = ResolveAccountsDir()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &HealthChecker{
		dir:      dir,
		interval: interval,
		known:    map[string]bool{},
		alerted:  map[string]bool{},
		notify:   n,
		stopCh:   make(chan struct{}),
	}
}

func (h *HealthChecker) Start() {
	// 账号目录解析失败时明确降级为不巡检，避免把相对路径当账号目录。
	if h.dir == "" {
		log.Printf("[openclaw-health] disabled: accounts dir unresolved (set OPENCLAW_ACCOUNTS_DIR or OPENCLAW_HOME)")
		return
	}
	go h.loop()
	log.Printf("[openclaw-health] watching %s every %s", h.dir, h.interval)
}

func (h *HealthChecker) Stop() { close(h.stopCh) }

// snapshot 返回当前在线账号集合。
func (h *HealthChecker) onlineSet() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if strings.HasSuffix(name, ".context-tokens.json") || strings.HasSuffix(name, ".sync.json") {
			continue
		}
		out[name[:len(name)-len(".json")]] = true
	}
	return out
}

func (h *HealthChecker) loop() {
	t := time.NewTicker(h.interval)
	defer t.Stop()
	// 首轮把现状记为基线，避免启动即误报
	h.mu.Lock()
	h.known = h.onlineSet()
	h.mu.Unlock()
	for {
		select {
		case <-h.stopCh:
			return
		case <-t.C:
			h.check()
		}
	}
}

func (h *HealthChecker) check() {
	now := h.onlineSet()
	h.mu.Lock()
	prev := h.known
	h.known = now
	h.mu.Unlock()

	for acc := range prev {
		if !now[acc] {
			log.Printf("[openclaw-health] OFFLINE account=%s（账号文件消失，疑似被踢/掉线）", acc)
			if h.notify != nil {
				h.notify.OnOffline(acc, "")
			}
		}
	}
	for acc := range now {
		if !prev[acc] {
			log.Printf("[openclaw-health] ONLINE account=%s", acc)
			if h.notify != nil {
				h.notify.OnOnline(acc)
			}
		}
		// D3：主动识别 token 失效（C3 熔断态），不静默死；恢复后清除告警标记。
		if IsPaused(acc) {
			if !h.alerted[acc] {
				log.Printf("[openclaw-health] TOKEN-EXPIRED account=%s（会话失效，已熔断，待重扫）", acc)
				if h.notify != nil {
					h.notify.OnOffline(acc, "token-expired")
				}
				h.alerted[acc] = true
			}
		} else {
			delete(h.alerted, acc)
		}
	}
}

// Status 返回健康巡检快照。
func (h *HealthChecker) Status() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	online := make([]string, 0, len(h.known))
	for acc := range h.known {
		online = append(online, acc)
	}
	paused := []string{}
	for acc := range h.known {
		if IsPaused(acc) {
			paused = append(paused, acc)
		}
	}
	return map[string]any{
		"online":      online,
		"onlineCount": len(online),
		"paused":      paused,
		"pausedCount": len(paused),
	}
}

// 编译期保证 context 被使用（健康检查预留带 ctx 的探测扩展）。
var _ = context.Background
