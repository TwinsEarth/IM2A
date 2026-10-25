package openclaw

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AccountFile 对应 <accountsDir>/<accountId>.json（accountsDir 由 ResolveAccountsDir 解析）。
// 契约来源：OpenClaw `accounts/<accountId>.json` 的 userId/token 字段。
type AccountFile struct {
	UserID string `json:"userId"`
	Token  string `json:"token"`
}

// Watcher 扫描 OpenClaw 账号目录，新账号出现时自动回调本地 auto-bind。
type Watcher struct {
	dir    string
	cbURL  string
	seen   map[string]bool
	client *http.Client
	stopCh chan struct{}
}

// NewWatcher 创建账号目录 watcher；dir 为空时使用 ResolveAccountsDir() 解析。
func NewWatcher(dir, cbURL string) *Watcher {
	if dir == "" {
		dir = ResolveAccountsDir()
	}
	return &Watcher{
		dir:    dir,
		cbURL:  cbURL,
		seen:   map[string]bool{},
		client: &http.Client{Timeout: 5 * time.Second},
		stopCh: make(chan struct{}),
	}
}

func (w *Watcher) Start() {
	// 账号目录解析失败（无 OPENCLAW_HOME 等）时明确降级为不监听，不扫描相对路径。
	if w.dir == "" {
		log.Printf("[openclaw-watcher] disabled: accounts dir unresolved (set OPENCLAW_ACCOUNTS_DIR or OPENCLAW_HOME)")
		return
	}
	// 启动时先扫一次，把已存在账号标记为已见（避免误触发）
	_ = w.scanOnce()
	go w.loop()
	log.Printf("[openclaw-watcher] watching %s", w.dir)
}

func (w *Watcher) loop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-t.C:
			_ = w.scan()
		}
	}
}

func (w *Watcher) scanOnce() error {
	entries, err := fs.ReadDir(os.DirFS(w.dir), ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		w.seen[filepath.Join(w.dir, e.Name())] = true
	}
	return nil
}

func (w *Watcher) scan() error {
	entries, err := fs.ReadDir(os.DirFS(w.dir), ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(w.dir, e.Name())
		if w.seen[full] {
			continue
		}
		body, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var af AccountFile
		if err := json.Unmarshal(body, &af); err != nil || af.UserID == "" {
			continue
		}
		w.seen[full] = true
		accountID := strings.TrimSuffix(e.Name(), ".json")
		log.Printf("[openclaw-watcher] new account %s wxid=%s account=%s", e.Name(), af.UserID, accountID)
		go w.bind(af.UserID, accountID)
	}
	return nil
}

func (w *Watcher) bind(wxid, accountID string) {
	payload := `{"wxid":"` + wxid + `","device":"ios","owner":"admin","account":"` + accountID + `"}`
	req, _ := http.NewRequest("POST", w.cbURL, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer im2a_demo_token")
	resp, err := w.client.Do(req)
	if err != nil {
		log.Printf("[openclaw-watcher] bind err wxid=%s err=%v", wxid, err)
		return
	}
	defer resp.Body.Close()
	log.Printf("[openclaw-watcher] bind wxid=%s status=%d", wxid, resp.StatusCode)
}
