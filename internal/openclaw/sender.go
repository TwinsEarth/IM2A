package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// C3：账号级熔断。errcode=-14 会话/登录过期后，该账号暂停 10 分钟，避免雪崩/被风控。
var (
	pauseMu       sync.Mutex
	pausedUntil   = map[string]time.Time{}
	pauseDuration = 10 * time.Minute
)

// IsPaused C3：账号是否处于熔断暂停中。
func IsPaused(accountID string) bool {
	pauseMu.Lock()
	defer pauseMu.Unlock()
	return time.Now().Before(pausedUntil[accountID])
}

func pauseAccount(accountID string) {
	pauseMu.Lock()
	pausedUntil[accountID] = time.Now().Add(pauseDuration)
	pauseMu.Unlock()
}

// C4：异步补发窗口。同步 10s 超时/瞬时失败的消息进 outbox，后台按退避补发，不丢消息。
type outboxJob struct {
	accountID, targetWxid, text string
}

var (
	outbox       = make(chan outboxJob, 1024)
	outboxOnce   sync.Once
	outboxSent   int64 // atomic
	outboxFailed int64 // atomic
)

// D4：出站并发上限 10。每次发送 openclaw CLI 子进程都吃内存，
// 用加权信号量把并发发送压到 10，超出排队，防 OOM、稳定承载。
var sendSem = make(chan struct{}, 10)

func startOutboxWorker() {
	outboxOnce.Do(func() {
		go func() {
			// 后台补发复用同一份配置解析结果（不再硬编码 bin/home/channel）。
			sender := NewSender()
			for job := range outbox {
				// 后台补发：最多退避 5 轮，尊重账号熔断
				for i := 0; i < 6; i++ {
					if IsPaused(job.accountID) {
						time.Sleep(30 * time.Second)
						continue
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					_, err := sender.sendOnce(ctx, job.accountID, job.targetWxid, job.text)
					cancel()
					if err == nil {
						atomic.AddInt64(&outboxSent, 1)
						break
					}
					if errors.Is(err, ErrTokenExpired) {
						pauseAccount(job.accountID)
					}
					if errors.Is(err, ErrAccountUnknown) {
						// 账号不存在/未登录：重试无意义，直接放弃该条补发。
						break
					}
					time.Sleep(time.Duration(2<<uint(i)) * time.Second)
				}
				atomic.AddInt64(&outboxFailed, 1)
				log.Printf("[outbox] retry-exhausted account=%s target=%s text=%q", job.accountID, job.targetWxid, job.text)
			}
		}()
	})
}

// ErrSendFailed openclaw 出站发送失败（deliveryStatus 非 sent 或 CLI 错误）。
var ErrSendFailed = errors.New("openclaw: outbound send failed")

// C1：可分类的出站错误，区分"真限流"、"token 失效"、"账号未登录"，避免盲目重试。
var (
	// ErrTokenExpired context/登录 token 失效（errcode -14/-2 等），需重新登录/扫码，重试无用。
	ErrTokenExpired = errors.New("openclaw: token expired / relogin required")
	// ErrRateLimited 真限流（429/too many），可稍后重试。
	ErrRateLimited = errors.New("openclaw: rate limited")
	// ErrAccountUnknown 账号未登录/不存在（CLI 返回 cli_error + Unknown account），
	// 与 token 过期区分：同样不可盲目重试，但不需要熔断 10 分钟（登录后应立即可用）。
	ErrAccountUnknown = errors.New("openclaw: account not logged in / unknown account")
)

// classifySendError C1：从 CLI 输出识别错误类型。
func classifySendError(out []byte) error {
	s := strings.ToLower(string(out))
	switch {
	case strings.Contains(s, "-14") || strings.Contains(s, "-2") && strings.Contains(s, "token"),
		strings.Contains(s, "token expired"), strings.Contains(s, "login expired"),
		strings.Contains(s, "登录已失效"), strings.Contains(s, "重新登录"),
		strings.Contains(s, "context token"):
		return fmt.Errorf("%w: %s", ErrTokenExpired, strings.TrimSpace(string(out)))
	case strings.Contains(s, "unknown account") || (strings.Contains(s, "cli_error") && strings.Contains(s, "account")):
		// 实测：未登录账号发送时返回
		// {"ok":false,"error":{"type":"cli_error","message":"Unknown account \"x\" for channel y."}}
		return fmt.Errorf("%w: %s（需扫码登录该账号）", ErrAccountUnknown, strings.TrimSpace(string(out)))
	case strings.Contains(s, "429") || strings.Contains(s, "too many"),
		strings.Contains(s, "rate limit"), strings.Contains(s, "限流"):
		return fmt.Errorf("%w: %s", ErrRateLimited, strings.TrimSpace(string(out)))
	}
	return nil
}

// Sender 通过 openclaw CLI 主动向微信发送消息（IM2A → openclaw → 微信）。
//
// 等价于：
//
//	openclaw message send --channel <channel> --account <accountId> \
//	  --target <wxid> -m "<text>" --json
//
// 契约来源：OpenClaw `cli/message send`。
// bin/home/channel 均来自 bin.go 的配置解析器；IM2A gateway 以 root 运行，
// HOME 指向 openclaw 配置根即可读到其 gateway token 与账号。
type Sender struct {
	bin       string
	channel   string
	home      string
	extraPATH string
	timeout   time.Duration
	dryRun    bool
}

// backoffPlan C2：指数退避（2s→4s→8s→16s→30s），瞬时抖动自动恢复；token 失效不重试。
var backoffPlan = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// NewSender 从配置解析器读取运行参数（不再硬编码路径/渠道）：
// bin ← ResolveBin()（OPENCLAW_BIN → PATH → 已知安装位置 → 空）、
// home ← ResolveHome()（OPENCLAW_HOME → os.UserHomeDir）、
// channel ← ResolveChannel()（OPENCLAW_CHANNEL → openclaw-weixin）、
// extraPATH ← bin 所在目录（Node 安装的 openclaw 需要同级 node）；
// OPENCLAW_DRY_RUN=1 时只构造 payload 不真实发送（测试用）。
func NewSender() *Sender {
	return &Sender{
		bin:       ResolveBin(),
		channel:   ResolveChannel(),
		home:      ResolveHome(),
		extraPATH: ResolveBinDir(),
		timeout:   10 * time.Second, // C4：10s 硬超时，不卡死
		dryRun:    os.Getenv("OPENCLAW_DRY_RUN") == "1",
	}
}

// pathWithExtra 把 bin 所在目录前置到 PATH；extra 为空时原样返回 PATH。
func pathWithExtra(extra string) string {
	path := os.Getenv("PATH")
	switch {
	case extra == "":
		return path
	case path == "":
		return extra
	default:
		return extra + string(os.PathListSeparator) + path
	}
}

// sendCLIOutput 是 openclaw message send --json 的输出子集。
type sendCLIOutput struct {
	MessageID string `json:"messageId"`
	Payload   struct {
		DeliveryStatus string `json:"deliveryStatus"`
		Result         struct {
			MessageID string `json:"messageId"`
		} `json:"result"`
	} `json:"payload"`
}

// Send 由指定 openclaw 账号（bot）给 targetWxid 发送一条文本消息，
// 成功返回 openclaw messageId；deliveryStatus 非 sent 视为失败。
// dryRun=true 时追加 --dry-run，真实 exec 但不发出微信，返回 "dry-run:<target>"。
// sendOnce 单次发送（原逻辑）。
func (s *Sender) sendOnce(ctx context.Context, accountID, targetWxid, text string) (string, error) {
	// v2.3.2 A5：出站令牌桶，同账号 ≥3s/条；同步 Send 与 outbox worker 共用。
	if err := WaitOutbound(ctx, accountID); err != nil {
		return "", err
	}
	if accountID == "" || targetWxid == "" || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%w: account/target/text required", ErrSendFailed)
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	args := []string{
		"message", "send",
		"--channel", s.channel,
		"--account", accountID,
		"--target", targetWxid,
		"-m", text,
		"--json",
	}
	if s.dryRun {
		args = append(args, "--dry-run")
	}
	sendSem <- struct{}{}
	defer func() { <-sendSem }()
	cmd := exec.CommandContext(ctx, s.bin, args...)
	env := os.Environ()
	if s.home != "" {
		env = append(env, "HOME="+s.home)
	}
	env = append(env, "PATH="+pathWithExtra(s.extraPATH))
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ce := classifySendError(out); ce != nil {
			return "", ce
		}
		return "", fmt.Errorf("%w: %v: %s", ErrSendFailed, err, strings.TrimSpace(string(out)))
	}
	raw := strings.TrimSpace(string(out))
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	var parsed sendCLIOutput
	if jerr := json.Unmarshal([]byte(raw), &parsed); jerr != nil {
		return "", fmt.Errorf("%w: parse output: %v: %s", ErrSendFailed, jerr, raw)
	}
	if s.dryRun {
		return "dry-run:" + targetWxid, nil
	}
	if parsed.Payload.DeliveryStatus != "sent" {
		if ce := classifySendError([]byte(raw)); ce != nil {
			return "", ce
		}
		return "", fmt.Errorf("%w: deliveryStatus=%s", ErrSendFailed, parsed.Payload.DeliveryStatus)
	}
	mid := parsed.MessageID
	if mid == "" {
		mid = parsed.Payload.Result.MessageID
	}
	if mid == "" {
		return "", fmt.Errorf("%w: empty messageId", ErrSendFailed)
	}
	return mid, nil
}

// 长消息切分扩展点（2026-10-25，DeepSeek-Bot 子插件接入）。
//
// 背景：微信个人号通道对单条文本有长度限制，而 sendOnce 原先把整段 text 作为
// 一条 `openclaw message send -m <text>` 发出——超长时会被上游拒绝或截断丢内容。
// 这里不把切分算法写进本包（那会与 plugin/dsbot 形成依赖倒置），而是留一个可注入的
// 纯函数 + 字符上限：gateway 启动时注入 dsbotplugin.Split（移植自 DeepSeek-Bot），
// 未注入时行为与历史完全一致（单条发送）。
var (
	// TextSplitter 由调用方注入：把 text 按 limit（Unicode 码点）切成多条，拼接后等于原文。
	TextSplitter func(text string, limit int) []string
	// OutboundCharLimit 单条消息字符上限；<=0 表示不切分。
	OutboundCharLimit int
)

// SplitForSend 按注入的切分器把文本切成待发送的片段。
// 未注入切分器、未设上限、或文本未超限时返回单元素切片，保持历史行为。
func SplitForSend(text string) []string {
	if TextSplitter == nil || OutboundCharLimit <= 0 || len([]rune(text)) <= OutboundCharLimit {
		return []string{text}
	}
	parts := TextSplitter(text, OutboundCharLimit)
	if len(parts) == 0 {
		return []string{text}
	}
	return parts
}

// Send C2：带指数退避重试；超长文本按 SplitForSend 切成多条顺序发送。
// token 失效（ErrTokenExpired）立即返回不重试；其它瞬时错误按 backoffPlan 退避重试，最多 5 次。
func (s *Sender) Send(ctx context.Context, accountID, targetWxid, text string) (string, error) {
	// C3：账号熔断期直接返回，不发请求。
	if IsPaused(accountID) {
		return "", fmt.Errorf("%w: account paused for %s after session expiry", ErrTokenExpired, pauseDuration)
	}
	chunks := SplitForSend(text)
	lastID := ""
	for i, chunk := range chunks {
		mid, err := s.sendWithRetry(ctx, accountID, targetWxid, chunk)
		if err != nil {
			if i > 0 {
				// 已发出前 i 条：如实告知部分成功，避免调用方误以为整段未发出。
				return lastID, fmt.Errorf("%w: chunk %d/%d delivered, then failed: %v",
					ErrSendFailed, i, len(chunks), err)
			}
			return mid, err
		}
		lastID = mid
	}
	return lastID, nil
}

// sendWithRetry 是单条消息的退避重试逻辑（原 Send 实现）。
func (s *Sender) sendWithRetry(ctx context.Context, accountID, targetWxid, text string) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= len(backoffPlan); attempt++ {
		if attempt > 0 {
			wait := backoffPlan[attempt-1]
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		}
		mid, err := s.sendOnce(ctx, accountID, targetWxid, text)
		if err == nil {
			return mid, nil
		}
		lastErr = err
		// token 失效：熔断该账号 10 分钟，不重试、不进补发
		if errors.Is(err, ErrTokenExpired) {
			pauseAccount(accountID)
			return "", err
		}
		// 账号未登录/不存在：重试无意义，直接返回（不熔断，扫码登录后应立即可用）
		if errors.Is(err, ErrAccountUnknown) {
			return "", err
		}
	}
	// C4：同步退避仍失败（含 10s 超时）→ 进异步补发窗口，不丢消息、不卡死调用方。
	startOutboxWorker()
	select {
	case outbox <- outboxJob{accountID, targetWxid, text}:
		return "queued:" + targetWxid, nil
	default:
		return "", lastErr
	}
}

// SendTyping C5：发送"正在输入"状态。微信个人号暂无 typing 接口，
// 当前以 B7 的即时占位语承担"正在输入"体感；保留方法便于后续 openclaw 支持时接入。
func (s *Sender) SendTyping(ctx context.Context, accountID, targetWxid string) error {
	// 预留：若 openclaw 后续提供 typing action，在此调用。
	return nil
}

// OutboxStats A7：出站排队/并发/熔断可观测（admin 自查看"当前第几个/预计等待"）。
type OutboxStat struct {
	OutboxDepth    int             `json:"outboxDepth"`
	OutboxCapacity int             `json:"outboxCapacity"`
	ActiveSends    int             `json:"activeSends"`
	MaxConcurrency int             `json:"maxConcurrency"`
	PausedAccounts []PausedAccount `json:"pausedAccounts"`
	SentTotal      int64           `json:"sentTotal"`
	FailedTotal    int64           `json:"failedTotal"`
}

type PausedAccount struct {
	AccountID    string `json:"accountId"`
	Until        string `json:"until"`
	RemainingSec int    `json:"remainingSec"`
}

// OutboxStats 快照当前出站状态（不阻塞发送）。
func SnapshotOutbox() OutboxStat {
	st := OutboxStat{
		OutboxDepth:    len(outbox),
		OutboxCapacity: cap(outbox),
		ActiveSends:    len(sendSem),
		MaxConcurrency: cap(sendSem),
		SentTotal:      atomic.LoadInt64(&outboxSent),
		FailedTotal:    atomic.LoadInt64(&outboxFailed),
	}
	pauseMu.Lock()
	now := time.Now()
	for acc, until := range pausedUntil {
		if now.Before(until) {
			st.PausedAccounts = append(st.PausedAccounts, PausedAccount{
				AccountID:    acc,
				Until:        until.Format(time.RFC3339),
				RemainingSec: int(time.Until(until).Seconds()),
			})
		}
	}
	pauseMu.Unlock()
	if st.PausedAccounts == nil {
		st.PausedAccounts = []PausedAccount{}
	}
	return st
}
