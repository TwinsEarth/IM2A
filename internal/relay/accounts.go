package relay

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrAccountNotFound 找不到 openclaw 账号文件。
var ErrAccountNotFound = errors.New("relay: openclaw account not found")

// AccountResolver 把 openclaw accountId 解析成 bot 自己的微信 wxid。
// 账号文件由 openclaw 扫码落地：<accountsDir>/<accountId>.json，内含 {"userId": "<wxid>"}。
// 带 30s 缓存，避免每条消息都读盘。
type AccountResolver struct {
	dir string

	mu    sync.RWMutex
	cache map[string]entry
}

type entry struct {
	wxid string
	at   time.Time
}

// NewAccountResolver dir 通常为 /root/.openclaw/openclaw-weixin/accounts。
func NewAccountResolver(dir string) *AccountResolver {
	return &AccountResolver{dir: dir, cache: make(map[string]entry)}
}

type accountFile struct {
	UserID string `json:"userId"`
}

// Resolve 返回 accountId 对应的 bot wxid。
func (r *AccountResolver) Resolve(accountID string) (string, error) {
	if accountID == "" {
		return "", errors.New("relay: empty accountId")
	}
	r.mu.RLock()
	if e, ok := r.cache[accountID]; ok && time.Since(e.at) < 30*time.Second {
		r.mu.RUnlock()
		return e.wxid, nil
	}
	r.mu.RUnlock()

	p := filepath.Join(r.dir, accountID+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", ErrAccountNotFound
	}
	var af accountFile
	if err := json.Unmarshal(raw, &af); err != nil {
		return "", err
	}
	wxid := strings.TrimSpace(af.UserID)
	if wxid == "" {
		return "", ErrAccountNotFound
	}
	r.mu.Lock()
	r.cache[accountID] = entry{wxid: wxid, at: time.Now()}
	r.mu.Unlock()
	return wxid, nil
}

// AllAccounts 扫描目录，返回 accountId → wxid 映射（供投递器使用）。
func (r *AccountResolver) AllAccounts() (map[string]string, error) {
	out := make(map[string]string)
	files, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".json") || strings.Contains(name, ".context-tokens") || strings.Contains(name, ".sync") {
			continue
		}
		accountID := strings.TrimSuffix(name, ".json")
		if wxid, err := r.Resolve(accountID); err == nil {
			out[accountID] = wxid
		}
	}
	return out, nil
}
