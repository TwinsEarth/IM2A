package openclaw

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

var poolQRRe = regexp.MustCompile(`https://liteapp\.weixin\.qq\.com/q/\S+`)

const (
	PoolMaxConcurrency = 10
	PoolSessionTTL     = 5 * time.Minute
	PoolWaitURL        = 30 * time.Second
	PoolQueueWait      = 10 * time.Minute // 排队超时，自动离队
	PoolDrainInterval  = 1 * time.Second
)

// ErrQueued 表示当前并发已满，请求已入队；客户端应轮询排队状态。
var ErrQueued = errors.New("openclaw: queued")

type poolProc struct {
	cancel  context.CancelFunc
	qrURL   string
	started time.Time
}

type queuedOwner struct {
	owner    string
	enqueued time.Time
}

// Pool 每用户独立 profile + 独立进程，并发上限 PoolMaxConcurrency。
// 满员时 FIFO 排队：drain 协程每秒检查，有槽位自动放行，不再直接拒绝。
type Pool struct {
	mu       sync.Mutex
	procs    map[string]*poolProc
	active   int // 当前占用槽位数
	queue    []*queuedOwner
	queuedOf map[string]bool
	stopCh   chan struct{}
}

func NewPool() *Pool {
	p := &Pool{
		procs:    make(map[string]*poolProc),
		queuedOf: make(map[string]bool),
		stopCh:   make(chan struct{}),
	}
	go p.drain()
	return p
}

func (p *Pool) Close() { close(p.stopCh) }

// memAvailMB 读取 /proc/meminfo 的 MemAvailable。
func memAvailMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 1024
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var k string
		var v int
		var u string
		if _, err := fmt.Sscanf(sc.Text(), "%s %d %s", &k, &v, &u); err == nil && k == "MemAvailable" {
			return v / 1024
		}
	}
	return 1024
}

// Start 为指定 owner 起一个独立 login 子进程，返回二维码 URL。
// 并发未满 → 立即启动；满员 → 入队并返回 ErrQueued，由 drain 自动放行。
func (p *Pool) Start(owner string) (string, error) {
	if memAvailMB() < 500 {
		return "", errors.New("openclaw: 内存不足（<500MB），请稍后再试")
	}
	p.mu.Lock()
	if existing, ok := p.procs[owner]; ok && existing.qrURL != "" && time.Since(existing.started) < PoolSessionTTL {
		p.mu.Unlock()
		return existing.qrURL, nil
	}
	if p.queuedOf[owner] {
		p.mu.Unlock()
		return "", ErrQueued
	}
	if p.active >= PoolMaxConcurrency {
		p.queue = append(p.queue, &queuedOwner{owner: owner, enqueued: time.Now()})
		p.queuedOf[owner] = true
		p.mu.Unlock()
		return "", ErrQueued
	}
	p.active++
	p.mu.Unlock()

	qr, err := p.launch(owner)
	if err != nil {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
		return "", err
	}
	return qr, nil
}

// drain 每秒放行：有空闲槽位且队列非空时，弹出队首并启动。
func (p *Pool) drain() {
	t := time.NewTicker(PoolDrainInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.mu.Lock()
			// 丢弃过期排队者
			kept := p.queue[:0]
			for _, q := range p.queue {
				if time.Since(q.enqueued) > PoolQueueWait {
					delete(p.queuedOf, q.owner)
					log.Printf("[openclaw-pool] queue timeout owner=%s", q.owner)
					continue
				}
				kept = append(kept, q)
			}
			p.queue = kept
			var next []string
			for p.active < PoolMaxConcurrency && len(p.queue) > 0 {
				q := p.queue[0]
				p.queue = p.queue[1:]
				delete(p.queuedOf, q.owner)
				p.active++
				next = append(next, q.owner)
			}
			p.mu.Unlock()
			for _, o := range next {
				go func(owner string) {
					if _, err := p.launch(owner); err != nil {
						log.Printf("[openclaw-pool] drained launch err owner=%s err=%v", owner, err)
					}
					p.mu.Lock()
					p.active--
					p.mu.Unlock()
				}(o)
			}
		}
	}
}

// launch 拉起 login 子进程并等待二维码 URL（调用方已占用槽位）。
func (p *Pool) launch(owner string) (string, error) {
	bin := ResolveBin()
	if bin == "" {
		return "", errors.New("openclaw: binary not resolved (set OPENCLAW_BIN or install openclaw)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	// 契约来源：OpenClaw `cli/channels login --channel <channel> --account <id>`。
	// 微信个人号渠道由外部 openclaw-weixin 插件提供，未安装该插件时登录会失败。
	cmd := exec.CommandContext(ctx, bin,
		"channels", "login", "--channel", ResolveChannel(),
		"--account", "im2a_"+owner,
	)
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

	proc := &poolProc{cancel: cancel, started: time.Now()}
	p.mu.Lock()
	if old, ok := p.procs[owner]; ok {
		old.cancel()
	}
	p.procs[owner] = proc
	p.mu.Unlock()

	urlCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := out.Read(buf)
			if n > 0 {
				if m := poolQRRe.Find(buf[:n]); len(m) > 0 {
					select {
					case urlCh <- string(m):
					default:
					}
				}
			}
			if rerr == io.EOF || ctx.Err() != nil {
				return
			}
		}
	}()

	select {
	case u := <-urlCh:
		proc.qrURL = u
		go func() {
			time.Sleep(PoolSessionTTL)
			p.mu.Lock()
			if p.procs[owner] == proc {
				cancel()
				delete(p.procs, owner)
			}
			p.mu.Unlock()
			p.mu.Lock()
			p.active--
			p.mu.Unlock()
		}()
		return u, nil
	case <-time.After(PoolWaitURL):
		cancel()
		p.mu.Lock()
		delete(p.procs, owner)
		p.mu.Unlock()
		return "", errors.New("openclaw: 30s 内未拿到二维码")
	}
}

// QueueStatus 返回某 owner 的排位信息（未排队 ok=false）。
func (p *Pool) QueueStatus(owner string) (position, etaSeconds, active int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	active = p.active
	for i, q := range p.queue {
		if q.owner == owner {
			// 排位从 1 开始；预计等待 ≈ 前面人数 × 单会话平均占用（保守取 TTL/4）
			wait := time.Duration(i) * PoolSessionTTL / 4
			if wait < 5*time.Second {
				wait = 5 * time.Second
			}
			return i + 1, int(wait.Seconds()), active, true
		}
	}
	return 0, 0, active, false
}

// Status 返回进程池 + 排队快照。
func (p *Pool) Status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	items := map[string]any{}
	for owner, pr := range p.procs {
		items[owner] = map[string]any{
			"qrUrl":     pr.qrURL,
			"expiresIn": int((PoolSessionTTL - time.Since(pr.started)).Seconds()),
		}
	}
	queue := make([]string, 0, len(p.queue))
	for _, q := range p.queue {
		queue = append(queue, q.owner)
	}
	return map[string]any{
		"active":   p.active,
		"max":      PoolMaxConcurrency,
		"queued":   len(p.queue),
		"queue":    queue,
		"profiles": items,
	}
}

func init() { _ = log.Default }
