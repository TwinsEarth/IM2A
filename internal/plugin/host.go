// SPDX-License-Identifier: MIT

package plugin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
)

// 注册期错误。
var (
	// ErrNilPlugin 注册了 nil 插件。
	ErrNilPlugin = errors.New("plugin: nil plugin")
	// ErrEmptyPluginID 插件 ID 为空。
	ErrEmptyPluginID = errors.New("plugin: empty plugin ID")
	// ErrDuplicatePlugin 插件 ID 重复。
	ErrDuplicatePlugin = errors.New("plugin: duplicate plugin ID")
)

// Host 是插件宿主：注册、按 ID 字典序确定性启动、逆序停止，并维持健康快照。
//
// 纪律（与集成契约一致）：
//   - 单个插件 Start 失败只把该插件标记 degraded，不回滚已启动插件、不阻塞其余插件；
//   - Start 是幂等的：已成功启动的插件不会被重复启动；
//   - 一切方法并发安全；插件实现不得在被持锁期间回调 Host。
type Host struct {
	mu       sync.RWMutex
	logger   *log.Logger
	plugins  map[string]Plugin
	started  map[string]bool
	startErr map[string]string
}

// NewHost 创建插件宿主；logger 为 nil 时使用 log.Default()。
func NewHost(logger *log.Logger) *Host {
	if logger == nil {
		logger = log.Default()
	}
	return &Host{
		logger:   logger,
		plugins:  make(map[string]Plugin),
		started:  make(map[string]bool),
		startErr: make(map[string]string),
	}
}

// Register 注册插件；重复 ID、空 ID、nil 插件均返回错误。
func (h *Host) Register(p Plugin) error {
	if p == nil {
		return ErrNilPlugin
	}
	id := p.ID()
	if id == "" {
		return ErrEmptyPluginID
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.plugins[id]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicatePlugin, id)
	}
	h.plugins[id] = p
	h.logger.Printf("[plugin] registered id=%s version=%s capabilities=%d", id, p.Version(), len(p.Capabilities()))
	return nil
}

// Get 按 ID 返回已注册插件。
func (h *Host) Get(id string) (Plugin, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.plugins[id]
	return p, ok
}

// Start 按 ID 字典序依次启动全部插件。
// 单个插件失败只记录 degraded 并继续；返回值恒为 nil（启动失败不应阻断网关）。
func (h *Host) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range h.sortedIDsLocked() {
		p := h.plugins[id]
		if h.started[id] {
			h.logger.Printf("[plugin] start %s skipped: already started", id)
			continue
		}
		if err := safeStart(ctx, p); err != nil {
			h.startErr[id] = err.Error()
			h.logger.Printf("[plugin] start %s failed, marked degraded (others continue): %v", id, err)
			continue
		}
		delete(h.startErr, id)
		h.started[id] = true
		health := safeHealth(ctx, p)
		h.logger.Printf("[plugin] started %s state=%s detail=%q", id, health.State, health.Detail)
	}
	return nil
}

// Stop 按启动的逆序（ID 字典序逆序）停止已启动插件，并聚合错误。
func (h *Host) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := h.sortedIDsLocked()
	var errs []error
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		if !h.started[id] {
			continue
		}
		if err := safeStop(ctx, h.plugins[id]); err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", id, err))
			h.logger.Printf("[plugin] stop %s failed: %v", id, err)
			continue
		}
		h.started[id] = false
		h.logger.Printf("[plugin] stopped %s", id)
	}
	return errors.Join(errs...)
}

// Snapshots 返回全部插件的状态快照，按 ID 字典序排列（确定性输出）。
func (h *Host) Snapshots() []Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := h.sortedIDsLocked()
	out := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		p := h.plugins[id]
		snap := Snapshot{
			ID:           id,
			Version:      p.Version(),
			Capabilities: p.Capabilities(),
			Attribution:  p.Attribution(),
		}
		health := safeHealth(context.Background(), p)
		snap.State = health.State
		snap.Detail = health.Detail
		// 启动失败是宿主掌握的事实，优先级高于插件自报状态（契约 §1：失败只记 degraded）。
		if startErr, ok := h.startErr[id]; ok {
			snap.State = StateDegraded
			snap.Detail = "start failed: " + startErr
		}
		if snap.State == "" {
			snap.State = StateDegraded
			snap.Detail = "plugin reported empty health state"
		}
		out = append(out, snap)
	}
	return out
}

// sortedIDsLocked 返回字典序排好的插件 ID；调用方必须持锁。
func (h *Host) sortedIDsLocked() []string {
	ids := make([]string, 0, len(h.plugins))
	for id := range h.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// safeStart 隔离插件 Start 的 panic，转成错误由宿主记录 degraded。
func safeStart(ctx context.Context, p Plugin) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Start panic: %v", r)
		}
	}()
	return p.Start(ctx)
}

// safeStop 隔离插件 Stop 的 panic。
func safeStop(ctx context.Context, p Plugin) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Stop panic: %v", r)
		}
	}()
	return p.Stop(ctx)
}

// safeHealth 隔离插件 Health 的 panic，失败时如实返回 failed。
func safeHealth(ctx context.Context, p Plugin) (health Health) {
	defer func() {
		if r := recover(); r != nil {
			health = Health{State: StateFailed, Detail: fmt.Sprintf("Health panic: %v", r)}
		}
	}()
	return p.Health(ctx)
}
