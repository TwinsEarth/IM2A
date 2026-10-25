package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// 生命周期默认值（设计 §6.3 的"可测试的默认值"）。
const (
	DefaultStartTimeout   = 30 * time.Second
	DefaultStopTimeout    = 10 * time.Second
	DefaultKillGrace      = 5 * time.Second
	DefaultHealthInterval = 2 * time.Second
	DefaultRestartWindow  = 60 * time.Second
	DefaultMaxRestarts    = 3
	maxRestartBackoff     = 10 * time.Second
)

// ProcSpec 是启动一个 K1 插件进程所需的全部信息。
type ProcSpec struct {
	ID        string
	Version   string
	Dir       string // 版本目录（安装后 <root>/<id>/<version>）
	Command   string // 可执行文件绝对路径；为空则由 manifest 推导（见 EntrypointCommand）
	Args      []string
	Env       []string
	Health    *Health
	Lifecycle *Lifecycle
	LogDir    string
}

// Supervisor 监督一个 K1 插件进程（进程组，设计 §6.5：内核作 supervisor，systemd 只管内核）。
type Supervisor struct {
	mu       sync.Mutex
	spec     ProcSpec
	cmd      *exec.Cmd
	stopping bool
	started  time.Time
	restarts int
	lastExit error
	logFile  *os.File
	done     chan struct{}
}

// NewSupervisor 创建一个 supervisor（不启动）。
func NewSupervisor(spec ProcSpec) *Supervisor {
	return &Supervisor{spec: spec, done: make(chan struct{})}
}

func durationOr(raw string, def time.Duration) time.Duration {
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func (s *Supervisor) startTimeout() time.Duration {
	if s.spec.Lifecycle == nil {
		return DefaultStartTimeout
	}
	return durationOr(s.spec.Lifecycle.StartTimeout, DefaultStartTimeout)
}

func (s *Supervisor) stopTimeout() time.Duration {
	if s.spec.Lifecycle == nil {
		return DefaultStopTimeout
	}
	return durationOr(s.spec.Lifecycle.StopTimeout, DefaultStopTimeout)
}

func (s *Supervisor) restartPolicy() string {
	if s.spec.Lifecycle == nil || s.spec.Lifecycle.Restart == "" {
		return "on-failure"
	}
	return s.spec.Lifecycle.Restart
}

func (s *Supervisor) maxRestarts() int {
	if s.spec.Lifecycle == nil || s.spec.Lifecycle.MaxRestarts <= 0 {
		return DefaultMaxRestarts
	}
	return s.spec.Lifecycle.MaxRestarts
}

// Start 启动进程并在 startTimeout 内等待健康（失败则杀掉进程组并返回错误）。
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		return errors.New("runtime: process already started")
	}
	s.stopping = false
	s.mu.Unlock()

	if err := s.spawn(); err != nil {
		return err
	}

	timeout := s.startTimeout()
	deadline := time.Now().Add(timeout)
	for {
		alive, err := s.healthy(ctx)
		if alive && err == nil {
			s.mu.Lock()
			s.started = time.Now()
			s.mu.Unlock()
			go s.watch()
			return nil
		}
		if time.Now().After(deadline) {
			stopErr := s.Stop(context.Background())
			reason := "health check not satisfied"
			if err != nil {
				reason = err.Error()
			}
			if stopErr != nil {
				return fmt.Errorf("runtime: start timeout (%s): %s; stop: %v", timeout, reason, stopErr)
			}
			return fmt.Errorf("runtime: start timeout (%s): %s", timeout, reason)
		}
		select {
		case <-ctx.Done():
			_ = s.Stop(context.Background())
			return ctx.Err()
		case <-time.After(DefaultHealthInterval):
		}
	}
}

func (s *Supervisor) spawn() error {
	cmdPath := s.spec.Command
	args := append([]string(nil), s.spec.Args...)
	if cmdPath == "" {
		return errors.New("runtime: ProcSpec.Command is required (derive with EntrypointCommand)")
	}
	cmd := exec.Command(cmdPath, args...)
	cmd.Dir = s.spec.Dir
	cmd.Env = append(os.Environ(), s.spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logDir := s.spec.LogDir
	if logDir == "" {
		logDir = filepath.Join(s.spec.Dir, "logs")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("runtime: mkdir log dir: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", s.spec.ID, s.spec.Version))
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("runtime: open log %s: %w", logPath, err)
	}
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("runtime: spawn %s: %w", cmdPath, err)
	}
	s.mu.Lock()
	s.cmd = cmd
	s.logFile = lf
	s.mu.Unlock()
	return nil
}

// watch 在进程退出后按 restart 策略决定是否重启（崩溃隔离 + 退避）。
func (s *Supervisor) watch() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	err := cmd.Wait()

	s.mu.Lock()
	s.lastExit = err
	stopping := s.stopping
	s.cmd = nil
	if s.logFile != nil {
		s.logFile.Close()
		s.logFile = nil
	}
	policy := s.restartPolicy()
	max := s.maxRestarts()
	restarts := s.restarts
	s.mu.Unlock()

	if stopping {
		close(s.done)
		return
	}
	wantRestart := policy == "always" || (policy == "on-failure" && err != nil)
	if !wantRestart || restarts >= max {
		close(s.done)
		return
	}

	backoff := time.Duration(1<<uint(restarts)) * 500 * time.Millisecond
	if backoff > maxRestartBackoff {
		backoff = maxRestartBackoff
	}
	time.Sleep(backoff)
	if s.Stopped() {
		close(s.done)
		return
	}
	s.mu.Lock()
	s.restarts++
	s.mu.Unlock()
	if spawnErr := s.spawn(); spawnErr != nil {
		s.mu.Lock()
		s.lastExit = spawnErr
		s.mu.Unlock()
		close(s.done)
		return
	}
	go s.watch()
}

// Stop 停止进程组：SIGTERM → 等待 stopTimeout → SIGKILL（设计 §6.3）。
// 注意：进程退出由 watch() 通过 cmd.Wait 回收，这里等待 s.done，避免重复 Wait。
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping || s.cmd == nil {
		s.stopping = true
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	pid := s.cmd.Process.Pid
	s.mu.Unlock()

	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(s.stopTimeout()):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-s.done:
		case <-time.After(DefaultKillGrace):
		}
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return ctx.Err()
	}

	s.mu.Lock()
	s.cmd = nil
	if s.logFile != nil {
		s.logFile.Close()
		s.logFile = nil
	}
	s.mu.Unlock()
	return nil
}

// Stopped 报告是否已收到停止信号或已自行结束。
func (s *Supervisor) Stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// Running 报告进程是否仍在运行。
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && !s.stopping
}

// Uptime 返回本次启动以来的时长（未启动返回 0）。
func (s *Supervisor) Uptime() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.IsZero() {
		return 0
	}
	return time.Since(s.started)
}

// Restarts 返回已重启次数。
func (s *Supervisor) Restarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

// Wait 等待 supervisor 结束（进程退出且不再重启）。
func (s *Supervisor) Wait() {
	<-s.done
}

// healthy 按 manifest 的 health 配置做一次健康探测。
// 返回 (alive, err)：alive 表示进程还在；err 表示健康探测的具体失败原因。
func (s *Supervisor) healthy(ctx context.Context) (bool, error) {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return false, errors.New("process not running")
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return false, fmt.Errorf("process exited: %v", cmd.ProcessState)
	}
	if s.spec.Health == nil {
		// 无健康检查：存活即视为成功（K1 最小可用路径）。
		return true, nil
	}
	timeout := durationOr(s.spec.Health.Timeout, 2*time.Second)
	switch s.spec.Health.Type {
	case "", "exec":
		if s.spec.Health.Target == "" {
			return true, nil
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		probe := exec.CommandContext(cctx, "/bin/sh", "-c", s.spec.Health.Target)
		probe.Dir = s.spec.Dir
		if out, err := probe.CombinedOutput(); err != nil {
			return true, fmt.Errorf("health exec failed: %v (%s)", err, trimForLog(out))
		}
		return true, nil
	case "http":
		if s.spec.Health.Target == "" {
			return true, nil
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.spec.Health.Target, nil)
		if err != nil {
			return true, fmt.Errorf("health http request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return true, fmt.Errorf("health http failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return true, fmt.Errorf("health http status %d", resp.StatusCode)
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported health type %q (exec|http)", s.spec.Health.Type)
	}
}

func trimForLog(b []byte) string {
	const max = 200
	s := string(b)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
