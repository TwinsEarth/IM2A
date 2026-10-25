// SPDX-License-Identifier: MIT
//
// 本文件封装 openclaw CLI 的进程调用与输出裁剪/摘要等通用工具。

package openclawplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"human-agent-im/internal/openclaw"
)

// lockedBuffer 是并发安全的输出缓冲：os/exec 会为 stdout/stderr 各起一个 copy goroutine，
// 二者同时写同一 buffer 会 race，这里加锁。
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// runOpenCLI 以受控环境执行 openclaw 子命令，返回 stdout+stderr 合并输出：
// HOME 指向解析后的 home，PATH 前置 bin 目录（Node 安装的 openclaw 需要同级 node）。
//
// 关键：openclaw CLI 会把 gateway 拉成**孙进程**并继承 stdout/stderr 管道。
// 若只杀直接子进程（exec.CommandContext 默认行为），孙进程仍持有管道 →
// CombinedOutput/Wait 永久阻塞（生产事故：网关启动卡在 activating 且拉起 200MB 孤儿进程）。
// 因此这里：① 独立进程组（Setpgid）；② 超时时对整组 SIGKILL；③ 用显式 Start/Wait
// 而不是 CombinedOutput，保证超时路径一定返回。
func runOpenCLI(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+openclaw.ResolveHome(),
		"PATH="+openclaw.PathWithBinDir(),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out lockedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			return []byte(out.String()), fmt.Errorf("%v: %s", err, excerpt(out.String(), maxExcerptRunes))
		}
		return []byte(out.String()), nil
	case <-ctx.Done():
		// 杀整个进程组（负数 pid），把 CLI 拉起的 gateway 等孙进程一并回收。
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			// Wait 仍不返回（极少见）：放弃等待，避免拖死启动。
		}
		return []byte(out.String()), fmt.Errorf("timeout: %w: %s", ctx.Err(), excerpt(out.String(), maxExcerptRunes))
	}
}

// trimToJSONObject 裁剪出可解析的 JSON 对象（容忍 CLI 在 JSON 前打印横幅/日志）。
func trimToJSONObject(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if !strings.HasPrefix(s, "{") || !json.Valid([]byte(s)) {
		return nil
	}
	return []byte(s)
}

// excerpt 折叠空白并截断到 max 个字符（按 rune 截断，避免截坏 UTF-8）。
func excerpt(raw string, max int) string {
	s := strings.Join(strings.Fields(raw), " ")
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// labelOr 返回 s，空则返回 fallback。
func labelOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// formatProbeDuration 统一探测耗时展示（四舍五入到 10ms，便于运维判断 Node 冷启动慢）。
func formatProbeDuration(d time.Duration) string {
	return d.Round(10 * time.Millisecond).String()
}

// firstNonEmptyLine 返回第一个非空行（去首尾空白）。
func firstNonEmptyLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
