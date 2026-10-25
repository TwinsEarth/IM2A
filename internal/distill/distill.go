// Package distill 实现 v0.7 蒸馏闭环：
//
// 订阅审批/强化事件，按 (agent, fact key) 维度累计正负反馈，
// 自动收紧子 Agent 的决策阈值。核心约束：
//   - 只收紧不放宽（安全方向，防止阈值抖动回归）
//   - 不修改基座模型（纯 Context/规则层）
//   - 每次收紧有事件溯源
package distill

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"

	"human-agent-im/internal/eventbus"
)

// 蒸馏规则参数（可调，但默认值保守）。
const (
	// 连续 N 次负反馈后强制要求审批
	defaultNegStreakThreshold = 2
	// 负反馈占比超过该值，强制要求审批
	defaultNegRatioThreshold = 0.3
	// 至少累计多少条反馈才开始计算占比（冷启动期不收紧）
	defaultMinSamples = 4
)

// Config 蒸馏规则参数（v0.7.3：运行时可调）。
type Config struct {
	NegStreakThreshold int     `json:"negStreakThreshold"`
	NegRatioThreshold  float64 `json:"negRatioThreshold"`
	MinSamples         int     `json:"minSamples"`
}

// DefaultConfig 返回保守默认值。
func DefaultConfig() Config {
	return Config{
		NegStreakThreshold: defaultNegStreakThreshold,
		NegRatioThreshold:  defaultNegRatioThreshold,
		MinSamples:         defaultMinSamples,
	}
}

// DecisionFeedback 某 Agent 对某类事实/决策的累计反馈。
type DecisionFeedback struct {
	UserID        uint64 `json:"userId"`
	AgentID       string `json:"agentId"`
	Key           string `json:"key"`
	Pos           int    `json:"pos"`
	Neg           int    `json:"neg"`
	NegStreak     int    `json:"negStreak"`
	Tightened     bool   `json:"tightened"`
	TightenReason string `json:"tightenReason,omitempty"`
	LastDecision  string `json:"lastDecision"`
}

// Engine 蒸馏引擎。
type Engine struct {
	mu       sync.RWMutex
	feedback map[string]*DecisionFeedback // key: agentID + "|" + key
	bus      *eventbus.Bus
	config   Config
	// tightenedCount 累计收紧次数（用于可解释统计）
	tightenedCount int
	// v1.0.3：可选 PG 持久化
	db *sql.DB
}

// New 构建蒸馏引擎（用默认规则）。
func New(bus *eventbus.Bus) *Engine {
	e := &Engine{
		feedback: map[string]*DecisionFeedback{},
		bus:      bus,
		config:   DefaultConfig(),
	}
	if bus != nil {
		bus.Subscribe(eventbus.EventTypeMemoryReinforce, e.onReinforce)
	}
	return e
}

// SetPG 注入 PG 持久化（v1.0.3）。
func (e *Engine) SetPG(db *sql.DB) { e.db = db }

// SetConfig 运行时调整规则（v0.7.3，master 可调用）。
func (e *Engine) SetConfig(cfg Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cfg.NegStreakThreshold > 0 {
		e.config.NegStreakThreshold = cfg.NegStreakThreshold
	}
	if cfg.NegRatioThreshold > 0 && cfg.NegRatioThreshold <= 1 {
		e.config.NegRatioThreshold = cfg.NegRatioThreshold
	}
	if cfg.MinSamples > 0 {
		e.config.MinSamples = cfg.MinSamples
	}
}

// GetConfig 返回当前规则（v0.7.3）。
func (e *Engine) GetConfig() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}

func fbKey(agentID, key string) string {
	return agentID + "|" + key
}

func (e *Engine) onReinforce(_ context.Context, ev *eventbus.Event) error {
	delta, _ := ev.Payload["delta"].(float64)
	key, _ := ev.Payload["key"].(string)
	if ev.AgentID == "" || key == "" {
		return nil
	}
	e.Record(ev.UserID, ev.AgentID, key, delta)
	return nil
}

// Record 记录一次反馈（delta>0 通过/强化；delta<0 驳回/弱化）。
func (e *Engine) Record(userID uint64, agentID, key string, delta float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k := fbKey(agentID, key)
	fb, ok := e.feedback[k]
	if !ok {
		fb = &DecisionFeedback{UserID: userID, AgentID: agentID, Key: key}
		e.feedback[k] = fb
	}
	if delta >= 0 {
		fb.Pos++
		fb.NegStreak = 0
		fb.LastDecision = "通过"
	} else {
		fb.Neg++
		fb.NegStreak++
		fb.LastDecision = "驳回"
	}
	// v1.0.3：异步落 PG improvements
	if e.db != nil {
		scn := "通过"
		if delta < 0 {
			scn = "驳回"
		}
		go func() {
			if _, err := e.db.Exec(`INSERT INTO improvements (user_id, agent_id, scenario, decision, feedback) VALUES ($1,$2,$3,$4,$5)`,
				fb.UserID, fb.AgentID, fb.Key, scn, ""); err != nil {
				log.Printf("distill: persist improvement: %v", err)
			}
		}()
	}
	// 只收紧不放宽
	if fb.Tightened {
		return
	}
	if reason, ok := e.shouldTighten(fb); ok {
		fb.Tightened = true
		fb.TightenReason = reason
		e.tightenedCount++
		e.publishTighten(fb)
	}
}

// publishTighten 发布收紧事件（best-effort）。
func (e *Engine) publishTighten(fb *DecisionFeedback) {
	if e.db != nil {
		go func() {
			if _, err := e.db.Exec(`INSERT INTO threshold_patches (user_id, agent_id, capability, old_threshold, new_threshold, confidence, reason, auto_applied) VALUES ($1,$2,$3,$4,$5,$6,$7,true)`,
				fb.UserID, fb.AgentID, fb.Key, 1.0, 0.0, 0.8, fb.TightenReason); err != nil {
				log.Printf("distill: persist threshold_patch: %v", err)
			}
		}()
	}
	if e.bus == nil {
		return
	}
	_ = e.bus.Publish(context.Background(), &eventbus.Event{
		Type:    eventbus.EventTypeDistillTighten,
		AgentID: fb.AgentID,
		Payload: map[string]any{
			"key": fb.Key, "pos": fb.Pos, "neg": fb.Neg,
			"negStreak": fb.NegStreak, "reason": fb.TightenReason,
		},
	})
}

// shouldTighten 返回是否应收紧及原因（v0.7.2 可解释）。
func (e *Engine) shouldTighten(fb *DecisionFeedback) (string, bool) {
	if fb.NegStreak >= e.config.NegStreakThreshold {
		return fmt.Sprintf("连续 %d 次驳回", fb.NegStreak), true
	}
	total := fb.Pos + fb.Neg
	if total >= e.config.MinSamples {
		ratio := float64(fb.Neg) / float64(total)
		if ratio >= e.config.NegRatioThreshold {
			return fmt.Sprintf("负反馈占比 %.0f%%（%d/%d）", ratio*100, fb.Neg, total), true
		}
	}
	return "", false
}

// RequireApproval 查询该 Agent 对某类决策是否必须走审批（蒸馏后的收紧结果）。
func (e *Engine) RequireApproval(agentID, key string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	fb, ok := e.feedback[fbKey(agentID, key)]
	if !ok {
		return false
	}
	return fb.Tightened
}

// TightenReason 返回收紧原因（未收紧返回空字符串）。
func (e *Engine) TightenReason(agentID, key string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	fb, ok := e.feedback[fbKey(agentID, key)]
	if !ok || !fb.Tightened {
		return ""
	}
	return fb.TightenReason
}

// Feedback 返回某 Agent 的全部反馈状态（master 查看/审计）。
func (e *Engine) Feedback(agentID string) []*DecisionFeedback {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]*DecisionFeedback, 0)
	for _, fb := range e.feedback {
		if fb.AgentID == agentID {
			cp := *fb
			result = append(result, &cp)
		}
	}
	return result
}

// FeedbackByUser 列出某用户所有 Agent 的反馈（v0.7.4）。
func (e *Engine) FeedbackByUser(userID uint64) []*DecisionFeedback {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]*DecisionFeedback, 0)
	for _, fb := range e.feedback {
		if fb.UserID == userID {
			cp := *fb
			result = append(result, &cp)
		}
	}
	return result
}

// ExportUser 导出某用户全部蒸馏反馈（v0.7.4，数据主权延伸）。
func (e *Engine) ExportUser(userID uint64) []*DecisionFeedback {
	return e.FeedbackByUser(userID)
}

// ImportUser 导入蒸馏反馈列表（覆盖同 key 状态）。
func (e *Engine) ImportUser(list []*DecisionFeedback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, fb := range list {
		if fb == nil || fb.AgentID == "" || fb.Key == "" {
			continue
		}
		cp := *fb
		e.feedback[fbKey(fb.AgentID, fb.Key)] = &cp
		if cp.Tightened {
			e.tightenedCount++
		}
	}
}

// UserReport 某用户的蒸馏汇总报表（v0.7.5）。
type UserReport struct {
	UserID        uint64              `json:"userId"`
	TotalKeys     int                 `json:"totalKeys"`
	TightenedKeys int                 `json:"tightenedKeys"`
	TotalPos      int                 `json:"totalPos"`
	TotalNeg      int                 `json:"totalNeg"`
	PassRate      float64             `json:"passRate"` // 通过/(通过+驳回)
	TightenedList []*DecisionFeedback `json:"tightenedList"`
}

// ReportUser 生成某用户的蒸馏汇总（v0.7.5，master 查看）。
func (e *Engine) ReportUser(userID uint64) UserReport {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r := UserReport{UserID: userID}
	for _, fb := range e.feedback {
		if fb.UserID != userID {
			continue
		}
		r.TotalKeys++
		r.TotalPos += fb.Pos
		r.TotalNeg += fb.Neg
		if fb.Tightened {
			r.TightenedKeys++
			cp := *fb
			r.TightenedList = append(r.TightenedList, &cp)
		}
	}
	total := r.TotalPos + r.TotalNeg
	if total > 0 {
		r.PassRate = float64(r.TotalPos) / float64(total)
	}
	return r
}

// Stats 蒸馏引擎整体统计。
type Stats struct {
	TrackedKeys    int `json:"trackedKeys"`
	TightenedKeys  int `json:"tightenedKeys"`
	TightenedTotal int `json:"tightenedTotal"`
}

// GetStats 统计。
func (e *Engine) GetStats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s := Stats{TrackedKeys: len(e.feedback), TightenedTotal: e.tightenedCount}
	for _, fb := range e.feedback {
		if fb.Tightened {
			s.TightenedKeys++
		}
	}
	return s
}

// Reset 手动重置某 key 的收紧状态（仅 master 允许，正常运行不自动调）。
func (e *Engine) Reset(agentID, key string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	fb, ok := e.feedback[fbKey(agentID, key)]
	if !ok {
		return fmt.Errorf("no feedback for %s/%s", agentID, key)
	}
	fb.Tightened = false
	fb.NegStreak = 0
	fb.TightenReason = ""
	return nil
}

// ResetUser 批量解除某用户所有收紧（v0.7.6，master 清理），返回解除条数。
func (e *Engine) ResetUser(userID uint64) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, fb := range e.feedback {
		if fb.UserID == userID && fb.Tightened {
			fb.Tightened = false
			fb.NegStreak = 0
			fb.TightenReason = ""
			e.tightenedCount--
			n++
		}
	}
	return n
}
