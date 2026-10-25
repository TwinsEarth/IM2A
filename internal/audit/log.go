// Package audit 提供不可变审计日志（§8.3 / §10）。
//
// 每一次工具调用、门控放行/拦截、扣费/退款、审批决策都写一条事件。
// 设计：只追加（append-only），不修改、不删除；生产环境落对象存储 WORM / 专用审计库。
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Action 动作类型。
type Action string

const (
	ActionToolCall      Action = "TOOL_CALL"
	ActionGateAllow     Action = "GATE_ALLOW"
	ActionGateDeny      Action = "GATE_DENY"
	ActionGateApproval  Action = "GATE_REQUIRE_APPROVAL"
	ActionCharge        Action = "CHARGE"
	ActionRefund        Action = "REFUND"
	ActionApprove       Action = "APPROVE"
	ActionReject        Action = "REJECT"
	ActionEscalate      Action = "ESCALATE"
	ActionContentReject Action = "CONTENT_REJECTED"
	ActionContentAllow  Action = "CONTENT_ALLOWED"
)

// Entry 一条审计事件。
type Entry struct {
	At        time.Time      `json:"at"`
	MessageID string         `json:"message_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	UserID    uint64         `json:"user_id,omitempty"`
	Action    Action         `json:"action"`
	Decision  string         `json:"decision,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Sink 审计日志写入接口。
type Sink interface {
	Write(Entry) error
}

// FileSink 追加写入文件（O_APPEND，JSON Lines）。
type FileSink struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileSink 打开（或创建）审计日志文件。
func NewFileSink(path string) (*FileSink, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &FileSink{f: f}, nil
}

// Write 追加一条 JSON 行。
func (s *FileSink) Write(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.f.Write(append(b, '\n'))
	return err
}

// Close 关闭文件。
func (s *FileSink) Close() error {
	return s.f.Close()
}

// MemorySink 进程内环形缓冲（自测/演示；生产不用）。
type MemorySink struct {
	mu      sync.Mutex
	entries []Entry
	max     int
}

// NewMemorySink 创建环形缓冲，max<=0 时默认 1024。
func NewMemorySink(max int) *MemorySink {
	if max <= 0 {
		max = 1024
	}
	return &MemorySink{max: max}
}

// Write 写入事件。
func (s *MemorySink) Write(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= s.max {
		s.entries = s.entries[1:]
	}
	s.entries = append(s.entries, e)
	return nil
}

// Entries 返回全部事件（副本）。
func (s *MemorySink) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Count 返回按动作统计的事件数。
func (s *MemorySink) Count() map[Action]int {
	out := map[Action]int{}
	for _, e := range s.Entries() {
		out[e.Action]++
	}
	return out
}
