package pluginruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Layout 描述 K1 插件的磁盘布局（设计 §6.1）：
//
//	<Root>/<id>/<version>/manifest.json + manifest.sig + payload...
//	<Root>/<id>/state.json     当前版本指针 + grants + 历史（唯一真相）
//	<Root>/<id>/logs/          supervisor 捕获的 stdout/stderr
//	<Root>/<id>/staging/       安装/升级的临时目录（成功后 rename 到位）
type Layout struct{ Root string }

var rePluginID = regexp.MustCompile(`^[a-z][a-z0-9.-]{1,63}$`)

// ValidateID 校验插件 id（与 manifest Schema 的 id 规则一致）。
func ValidateID(id string) error {
	if !rePluginID.MatchString(id) {
		return fmt.Errorf("runtime: invalid plugin id %q", id)
	}
	return nil
}

func (l Layout) PluginDir(id string) string       { return filepath.Join(l.Root, id) }
func (l Layout) VersionDir(id, ver string) string { return filepath.Join(l.Root, id, ver) }
func (l Layout) StatePath(id string) string       { return filepath.Join(l.Root, id, "state.json") }
func (l Layout) LogsDir(id string) string         { return filepath.Join(l.Root, id, "logs") }
func (l Layout) StagingDir(id string) string      { return filepath.Join(l.Root, id, "staging") }

// Ensure 创建插件目录骨架。
func (l Layout) Ensure(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	for _, dir := range []string{l.PluginDir(id), l.LogsDir(id), l.StagingDir(id)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("runtime: mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// VersionsOnDisk 返回某插件目录下所有版本（按 semver 升序）。
func (l Layout) VersionsOnDisk(id string) ([]string, error) {
	entries, err := os.ReadDir(l.PluginDir(id))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "logs" || name == "staging" {
			continue
		}
		if _, err := os.Stat(filepath.Join(l.PluginDir(id), name, "manifest.json")); err != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return compareSemver(out[i], out[j]) < 0 })
	return out, nil
}

// ---- state.json ----

// TxnPhase 是进行中的事务阶段（用于崩溃后 reconcile）。
type TxnPhase string

const (
	TxnPhaseStaged      TxnPhase = "staged"
	TxnPhaseUpgrading   TxnPhase = "upgrading"
	TxnPhaseRollingBack TxnPhase = "rolling_back"
)

// Txn 记录进行中的安装/升级/回滚事务（设计 §6.4 的 4 个崩溃点）。
type Txn struct {
	Phase       TxnPhase `json:"phase"`
	FromVersion string   `json:"fromVersion,omitempty"`
	ToVersion   string   `json:"toVersion,omitempty"`
	FromHash    string   `json:"fromHash,omitempty"`
	ToHash      string   `json:"toHash,omitempty"`
	StartedAt   string   `json:"startedAt"`
}

// Grant 记录一次"权限提升"的显式批准（manifestHash 绑定，换 manifest 即失效）。
type Grant struct {
	ManifestHash string   `json:"manifestHash"`
	Version      string   `json:"version"`
	ApprovedBy   string   `json:"approvedBy"`
	ApprovedAt   string   `json:"approvedAt"`
	Permissions  []string `json:"permissions"`
}

// HistoryEntry 是一次版本切换的历史（审计用）。
type HistoryEntry struct {
	Version string `json:"version"`
	Hash    string `json:"hash,omitempty"`
	Action  string `json:"action"` // install | upgrade | rollback | uninstall
	At      string `json:"at"`
	Prev    string `json:"prev,omitempty"`
}

// State 是 <id>/state.json 的内容（唯一真相，原子写）。
type State struct {
	PluginID      string         `json:"pluginId"`
	ActiveVersion string         `json:"activeVersion,omitempty"`
	Previous      string         `json:"previousVersion,omitempty"`
	ActiveHash    string         `json:"activeManifestHash,omitempty"`
	Grants        []Grant        `json:"grants,omitempty"`
	History       []HistoryEntry `json:"history,omitempty"`
	Txn           *Txn           `json:"txn,omitempty"`
	UpdatedAt     string         `json:"updatedAt"`
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// LoadState 读取 state.json；不存在时返回空状态（非错误）。
func LoadState(l Layout, id string) (*State, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(l.StatePath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{PluginID: id}, nil
		}
		return nil, fmt.Errorf("runtime: read state %s: %w", l.StatePath(id), err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("runtime: parse state %s: %w", l.StatePath(id), err)
	}
	if st.PluginID == "" {
		st.PluginID = id
	}
	return &st, nil
}

// SaveState 原子写 state.json：tmp → fsync(file) → rename → fsync(dir)。
func SaveState(l Layout, st *State) error {
	if err := ValidateID(st.PluginID); err != nil {
		return err
	}
	if err := l.Ensure(st.PluginID); err != nil {
		return err
	}
	st.UpdatedAt = nowRFC3339()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("runtime: marshal state: %w", err)
	}
	dir := l.PluginDir(st.PluginID)
	tmp := l.StatePath(st.PluginID) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("runtime: open state tmp: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("runtime: write state tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("runtime: fsync state tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("runtime: close state tmp: %w", err)
	}
	if err := os.Rename(tmp, l.StatePath(st.PluginID)); err != nil {
		return fmt.Errorf("runtime: rename state: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ReconcileAction 描述 reconcile 的处置建议。
type ReconcileAction string

const (
	ReconcileNone     ReconcileAction = "none"
	ReconcileCleanup  ReconcileAction = "cleanup_staging"
	ReconcileRollback ReconcileAction = "rollback_to_previous"
	ReconcileComplete ReconcileAction = "complete_upgrade"
)

// Reconcile 处理内核启动时发现的未完成事务（设计 §6.4）。
// healthy 由调用方判定：目标版本是否已通过健康检查（无法判定时传 false → 回滚）。
func Reconcile(l Layout, st *State, healthy bool) (ReconcileAction, error) {
	if st.Txn == nil {
		return ReconcileNone, nil
	}
	switch st.Txn.Phase {
	case TxnPhaseStaged:
		return ReconcileCleanup, nil
	case TxnPhaseUpgrading:
		if healthy {
			return ReconcileComplete, nil
		}
		return ReconcileRollback, nil
	case TxnPhaseRollingBack:
		return ReconcileRollback, nil
	default:
		return ReconcileNone, nil
	}
}

// ApplyReconcile 执行 reconcile 决定并落盘。
func ApplyReconcile(l Layout, st *State, action ReconcileAction) error {
	switch action {
	case ReconcileNone:
		return nil
	case ReconcileCleanup:
		if err := os.RemoveAll(l.StagingDir(st.PluginID)); err != nil {
			return err
		}
		st.Txn = nil
		return SaveState(l, st)
	case ReconcileRollback:
		if st.Previous == "" {
			st.Txn = nil
			st.ActiveVersion = ""
			st.ActiveHash = ""
			appendHistory(st, HistoryEntry{Action: "rollback_failed", At: nowRFC3339()})
			return SaveState(l, st)
		}
		st.ActiveVersion, st.Previous = st.Previous, ""
		st.ActiveHash = ""
		st.Txn = nil
		appendHistory(st, HistoryEntry{Action: "rollback", At: nowRFC3339(), Version: st.ActiveVersion})
		return SaveState(l, st)
	case ReconcileComplete:
		st.Previous = ""
		st.Txn = nil
		appendHistory(st, HistoryEntry{Action: "upgrade", At: nowRFC3339(), Version: st.ActiveVersion})
		return SaveState(l, st)
	default:
		return nil
	}
}

func appendHistory(st *State, e HistoryEntry) {
	const max = 50
	st.History = append(st.History, e)
	if len(st.History) > max {
		st.History = st.History[len(st.History)-max:]
	}
}

// GrantFor 查找绑定到指定 manifestHash 的批准记录。
func (s *State) GrantFor(manifestHash string) (Grant, bool) {
	for _, g := range s.Grants {
		if strings.EqualFold(g.ManifestHash, manifestHash) {
			return g, true
		}
	}
	return Grant{}, false
}

// AddGrant 记录一次显式批准（同一 manifest 只保留一条）。
func (s *State) AddGrant(g Grant) {
	out := s.Grants[:0]
	for _, old := range s.Grants {
		if !strings.EqualFold(old.ManifestHash, g.ManifestHash) {
			out = append(out, old)
		}
	}
	s.Grants = append(out, g)
}
