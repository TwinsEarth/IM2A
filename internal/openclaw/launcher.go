package openclaw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

var qrURLRe = regexp.MustCompile(`https://liteapp\.weixin\.qq\.com/q/\S+`)

// Launcher 管理一个常驻 openclaw channels login 子进程。
// 同时间只允许一个 active 登录会话。
type Launcher struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	qrURL   string
	started time.Time
}

func NewLauncher() *Launcher { return &Launcher{} }

// Start 启动 login 子进程并提取二维码 URL（最多等 30s）。
// 返回二维码 URL（也是扫码跳转链接）。5 分钟后自动 kill。
func (l *Launcher) Start() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmd != nil && l.qrURL != "" && time.Since(l.started) < 5*time.Minute {
		return l.qrURL, nil
	}
	l.killLocked()

	bin := ResolveBin()
	if bin == "" {
		return "", errors.New("openclaw: binary not resolved (set OPENCLAW_BIN or install openclaw)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	// 契约来源：OpenClaw `cli/channels login --channel <channel>`。
	// 微信个人号渠道由外部 openclaw-weixin 插件提供，未安装该插件时登录会失败。
	cmd := exec.CommandContext(ctx, bin, "channels", "login", "--channel", ResolveChannel())
	if home := ResolveHome(); home != "" {
		cmd.Dir = home
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return "", err
	}
	l.cmd = cmd
	l.cancel = cancel
	l.started = time.Now()

	urlCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				m := qrURLRe.Find(buf[:n])
				if len(m) > 0 {
					select {
					case urlCh <- string(m):
					default:
					}
				}
			}
			if err == io.EOF || ctx.Err() != nil {
				return
			}
		}
	}()

	select {
	case u := <-urlCh:
		l.qrURL = u
		// 5 分钟后自动 kill
		go func() {
			time.Sleep(5 * time.Minute)
			l.mu.Lock()
			if l.cmd == cmd {
				l.killLocked()
			}
			l.mu.Unlock()
		}()
		return u, nil
	case <-time.After(30 * time.Second):
		cancel()
		return "", errors.New("openclaw: timeout waiting for QR URL")
	}
}

func (l *Launcher) killLocked() {
	if l.cancel != nil {
		l.cancel()
	}
	if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Process.Kill()
		_, _ = l.cmd.Process.Wait()
	}
	l.cmd = nil
	l.cancel = nil
	l.qrURL = ""
}

// Status 返回当前会话状态。
func (l *Launcher) Status() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.qrURL == "" {
		return map[string]any{"active": false}
	}
	remaining := 5*time.Minute - time.Since(l.started)
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{
		"active":    l.cmd != nil,
		"qrUrl":     l.qrURL,
		"expiresIn": int(remaining.Seconds()),
	}
}

func init() {
	// 防止启动失败时刷屏
	_ = log.Default()
	_ = fmt.Sprint
}
