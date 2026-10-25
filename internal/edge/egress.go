// v1.4.4: "原始音频不出端" 的可验证证明（不是可声明）。
//
// 关键设计：旧实现把 hasAudio 作为调用方传入的 bool，无法自证（调用方撒谎即可绕过）。
// EgressGuard 改为真实扫描待出端的 body 字节：检测音频文件 magic、超大二进制、
// JSON 中的音频字段名。任何端侧出端请求必须经过 Allow()，被拒绝则物理上不能发。
package edge

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// maxBody 端侧单次出端允许的最大字节（文本 JSON 远小于此；音频流必然超过）。
const maxBody = 64 * 1024

// audioMagic 已知容器/帧头。
var audioMagic = [][]byte{
	{'R', 'I', 'F', 'F'}, // RIFF....WAVE
	{'I', 'D', '3'},      // mp3
	{'O', 'g', 'g', 'S'}, // ogg
	{'f', 'L', 'a', 'C'}, // flac
	{'f', 't', 'y', 'p'}, // mp4/m4a
	{0xFF, 0xFB},         // mp3 frame sync
}

// audioField JSON 字段名出现即视为携带音频。
var audioField = []string{"audio", "pcm", "wave", "wav", "voice", "speech", "raw_audio", "audiodata"}

type EgressEntry struct {
	At       time.Time
	Endpoint string
	Allowed  bool
	Reason   string
	Bytes    int
}

type EgressGuard struct {
	mu      sync.Mutex
	hosts   map[string]bool // 允许出端的 host（不含 scheme）
	entries []EgressEntry
}

// NewEgressGuard 指定允许出端的 host（端侧只允许回到自家后端）。
//
// 安全语义（v2.3.28 起）：**空白名单 = fail-closed，一律拒绝任何出端**。
// 旧实现把 len==0 当作"不限制"（fail-open），这会让"忘了配置白名单"变成"全部放行"，
// 与"原始音频/未授权内容不出端"的承诺相反。合规要求：默认值不得包含任何生产地址，
// 白名单必须由部署方显式配置（如 IM2A_EGRESS_ALLOW）。
func NewEgressGuard(allowedHosts []string) *EgressGuard {
	h := map[string]bool{}
	for _, x := range allowedHosts {
		if host := strings.TrimSpace(x); host != "" {
			h[strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"))] = true
		}
	}
	return &EgressGuard{hosts: h}
}

// Allow 真实裁决一次待出端请求。body 为即将发送的原始字节。
func (g *EgressGuard) Allow(endpoint string, body []byte) (bool, string) {
	host := hostOf(endpoint)
	reason := ""
	allowed := true

	switch {
	case len(g.hosts) == 0:
		// 空白名单 fail-closed：不配置就是拒绝，绝不因为"没配"而放行。
		allowed, reason = false, "egress allowlist empty (fail-closed; configure IM2A_EGRESS_ALLOW)"
	case !g.hosts[host]:
		allowed, reason = false, "host not whitelisted: "+host
	default:
		if magic, ok := detectAudioMagic(body); ok {
			allowed, reason = false, "audio stream detected (magic "+magic+")"
		} else if len(body) > maxBody {
			allowed, reason = false, "body too large for text egress"
		} else if f, ok := detectAudioField(body); ok {
			allowed, reason = false, "audio field in payload: "+f
		}
	}

	g.mu.Lock()
	g.entries = append(g.entries, EgressEntry{
		At: time.Now(), Endpoint: endpoint, Allowed: allowed, Reason: reason, Bytes: len(body),
	})
	g.mu.Unlock()
	return allowed, reason
}

// Report 汇总：总请求数、被拦截数、违规列表（用于"音频不出端"审计报告）。
func (g *EgressGuard) Report() (total, blocked int, violations []EgressEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		total++
		if !e.Allowed {
			blocked++
			violations = append(violations, e)
		}
	}
	return total, blocked, violations
}

// Snapshot 返回历史决策（供 API 落 audit_events）。
func (g *EgressGuard) Snapshot() []EgressEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]EgressEntry, len(g.entries))
	copy(out, g.entries)
	return out
}

func hostOf(endpoint string) string {
	s := endpoint
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "/")
	if i := strings.IndexAny(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && strings.Contains(s, "/") == false {
		s = s[:i]
	}
	return strings.ToLower(s)
}

func detectAudioMagic(body []byte) (string, bool) {
	for _, m := range audioMagic {
		if bytesHasPrefix(body, m) {
			return string(m), true
		}
	}
	// ftyp 在偏移 4
	if len(body) >= 8 && string(body[4:8]) == "ftyp" {
		return "ftyp", true
	}
	return "", false
}

func bytesHasPrefix(body, prefix []byte) bool {
	if len(body) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if body[i] != p {
			return false
		}
	}
	return true
}

// detectAudioField 仅当 body 是 JSON 时扫描键名；非 JSON 不做字段检查（由 magic/长度拦截）。
func detectAudioField(body []byte) (string, bool) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", false
	}
	for k := range m {
		lk := strings.ToLower(k)
		for _, af := range audioField {
			if strings.Contains(lk, af) {
				return k, true
			}
		}
	}
	return "", false
}
