// Package model 提供 L5 模型层抽象（§架构 v0.8）。
//
// 设计原则：
//   - 端侧小模型 ↔ 云端大模型 统一接口（Chat）
//   - 不绑定具体厂商（OpenAI 兼容协议可接 DeepSeek/通义/Kimi/本地 Ollama）
//   - 每次调用写审计事件（可解释性）
//   - 离线/无 key 时回退到规则桩（端侧意图分类）
package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message 对话消息。
type Message struct {
	Role    string `json:"role"` // system / user / assistant
	Content string `json:"content"`
}

// ChatRequest 模型调用请求。
type ChatRequest struct {
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"maxTokens,omitempty"`
	UserID      uint64    `json:"userId,omitempty"`
	AgentID     string    `json:"agentId,omitempty"`
	TimeoutMs   int       `json:"timeoutMs,omitempty"` // v0.8.7：可选 per-request 超时（毫秒），0=用默认
}

// ChatResponse 模型调用响应。
type ChatResponse struct {
	Content         string        `json:"content"`
	Model           string        `json:"model"`
	UsagePrompt     int           `json:"usagePrompt"`
	UsageCompletion int           `json:"usageCompletion"`
	Duration        time.Duration `json:"duration"`
	Confidence      float64       `json:"confidence,omitempty"` // v0.8.2：端侧意图置信度 0-1
	RoutedTo        string        `json:"routedTo,omitempty"`   // v0.8.2：edge / cloud
}

// Provider 模型提供者抽象（云端大模型 / 端侧小模型统一接口）。
type Provider interface {
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	Name() string
}

// ---- 端侧桩（规则/启发式，离线可用）----

// StubProvider 端侧规则桩：用于唤醒词、意图粗分类、敏感前置过滤。
// 无外部依赖，离线可跑。
type StubProvider struct {
	modelName string
}

// NewStub 构造端侧桩模型。
func NewStub() *StubProvider {
	return &StubProvider{modelName: "edge-stub-v1"}
}

// Name 实现 Provider。
func (s *StubProvider) Name() string { return s.modelName }

// Chat 端侧桩：根据输入做简单意图分类/回复模板。
func (s *StubProvider) Chat(_ context.Context, req *ChatRequest) (*ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("model: empty messages")
	}
	last := req.Messages[len(req.Messages)-1].Content
	reply, confidence := s.heuristicReply(last)
	return &ChatResponse{
		Content:         reply,
		Model:           s.modelName,
		UsagePrompt:     len(last) / 4,
		UsageCompletion: len(reply) / 4,
		Duration:        1 * time.Millisecond,
		Confidence:      confidence,
		RoutedTo:        "edge",
	}, nil
}

// heuristicReply 规则桩（端侧小模型占位，生产替换为真实分类器）。
// 返回 (回复, 置信度)。明确意图 0.85-0.9，模糊/未识别 0.3。
// v0.8.6：扩展意图分类（日程/采购/法律/健康/亲子/财务/物业/紧急）。
func (s *StubProvider) heuristicReply(input string) (string, float64) {
	lower := strings.ToLower(input)
	switch {
	case strings.Contains(lower, "你好") || strings.Contains(lower, "hi") || strings.Contains(lower, "hello"):
		return "你好，我是你的管家。有什么需要我处理的？", 0.9
	case strings.Contains(lower, "日程") || strings.Contains(lower, "会议") || strings.Contains(lower, "预约"):
		return "[桩] 已识别日程意图，将转交日程子 Agent。", 0.85
	case strings.Contains(lower, "采购") || strings.Contains(lower, "买") || strings.Contains(lower, "比价"):
		return "[桩] 已识别采购意图，将转交采购子 Agent。", 0.85
	case strings.Contains(lower, "法律") || strings.Contains(lower, "合同") || strings.Contains(lower, "律师"):
		return "[桩] 已识别法律意图，将转交法律子 Agent。", 0.85
	case strings.Contains(lower, "健康") || strings.Contains(lower, "医生") || strings.Contains(lower, "药") || strings.Contains(lower, "体检"):
		return "[桩] 已识别健康意图，将转交健康子 Agent（敏感健康信息请走加密通道）。", 0.8
	case strings.Contains(lower, "孩子") || strings.Contains(lower, "亲子") || strings.Contains(lower, "学校") || strings.Contains(lower, "作业"):
		return "[桩] 已识别亲子教育意图，将转交亲子子 Agent。", 0.85
	case strings.Contains(lower, "财务") || strings.Contains(lower, "报销") || strings.Contains(lower, "账单") || strings.Contains(lower, "工资"):
		return "[桩] 已识别财务意图，将转交财务子 Agent。", 0.85
	case strings.Contains(lower, "物业") || strings.Contains(lower, "房租") || strings.Contains(lower, "水电") || strings.Contains(lower, "维修"):
		return "[桩] 已识别物业意图，将转交物业子 Agent。", 0.85
	case strings.Contains(lower, "紧急") || strings.Contains(lower, "救命") || strings.Contains(lower, "报警"):
		return "[桩] 已识别紧急意图，将立即推送手表审批卡片（不走云端，端侧直接处理）。", 0.95
	default:
		return "[桩] 未识别明确意图，请补充。", 0.3
	}
}

// ---- OpenAI 兼容客户端（云端大模型）----

// OpenAICompatible OpenAI 兼容协议客户端（可接 DeepSeek/通义/Kimi/Ollama）。
type OpenAICompatible struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAICompatible 构造云端模型客户端。
// baseURL 如 "https://api.deepseek.com/v1"、"http://localhost:11434/v1"。
// 传空 apiKey 时回退桩（离线可跑）。
// sharedPool B4：全进程共享的连接池/keep-alive Transport，复用 TCP+TLS，
// 避免每次调用重新握手（省 0.2~0.5s/次）。
var sharedPoolTransport = &http.Transport{
	MaxIdleConns:        128,
	MaxIdleConnsPerHost: 32,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   true,
}

func NewOpenAICompatible(baseURL, apiKey, model string) *OpenAICompatible {
	return &OpenAICompatible{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		// B4：流式不能用硬整体超时（否则长流被切断）；连接池复用 keep-alive。
		client: &http.Client{Transport: sharedPoolTransport},
	}
}

// Name 实现 Provider。
func (o *OpenAICompatible) Name() string {
	if o.model == "" {
		return "cloud:unconfigured"
	}
	return "cloud:" + o.model
}

// chatCompletionReq OpenAI /chat/completions 请求体。
type chatCompletionReq struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// chatCompletionResp OpenAI /chat/completions 响应体。
type chatCompletionResp struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Chat 实现 Provider（v0.8.1：真实 HTTP 调用；无 key 时回退桩；v0.8.4：网络错误重试 1 次）。
func (o *OpenAICompatible) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// 离线/未配置：回退桩
	if o.apiKey == "" || o.baseURL == "" || o.model == "" {
		stub := NewStub()
		resp, err := stub.Chat(ctx, req)
		if err != nil {
			return nil, err
		}
		resp.Model = o.Name() + "(stub)"
		return resp, nil
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := o.doRequest(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// 仅对网络错误重试（4xx/5xx 不重试）
		if !isTransientNetErr(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("model: upstream after retry: %w", lastErr)
}

// doRequest 单次 HTTP 调用。
func (o *OpenAICompatible) doRequest(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	body, _ := json.Marshal(chatCompletionReq{
		Model:       o.model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	start := time.Now()
	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model: upstream: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model: upstream status %d: %s", resp.StatusCode, string(raw))
	}

	var cr chatCompletionResp
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("model: decode: %w", err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("model: upstream error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("model: empty choices")
	}

	return &ChatResponse{
		Content:         cr.Choices[0].Message.Content,
		Model:           o.model,
		UsagePrompt:     cr.Usage.PromptTokens,
		UsageCompletion: cr.Usage.CompletionTokens,
		Duration:        time.Since(start),
	}, nil
}

// isTransientNetErr 判断是否为可重试的网络错误。
func isTransientNetErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "upstream:") &&
		!strings.Contains(msg, "status") &&
		!strings.Contains(msg, "decode")
}

// ---- 组合 Provider（端侧优先，云端兜底）----

// HybridProvider 端侧优先、云端兜底（L5 架构：端侧小模型 ↔ 云端大模型）。
type HybridProvider struct {
	Edge    Provider // 端侧：唤醒/意图/过滤
	Cloud   Provider // 主云端：规划/推理/专业
	CloudFb Provider // v0.8.4：备云端（主云端失败时尝试，可空）
	bus     ModelEventBus
	stats   *callStats
	quota   *cloudQuota // v0.8.5：云端调用配额（防烧钱）
}

// cloudQuota 云端调用配额（per-agent 每分钟限制）。
type cloudQuota struct {
	perMinute int                // 每个 agent 每分钟最多云端调用次数
	calls     map[string][]int64 // agentID → 时间戳列表（Unix秒）
}

// newCloudQuota 构造配额，perMinute<=0 表示不限。
func newCloudQuota(perMinute int) *cloudQuota {
	return &cloudQuota{perMinute: perMinute, calls: map[string][]int64{}}
}

// allow 检查 agent 是否还能调云端。超限返回 false。
func (q *cloudQuota) allow(agentID string) bool {
	if q == nil || q.perMinute <= 0 {
		return true
	}
	now := time.Now().Unix()
	// 清理 60 秒前的记录
	cutoff := now - 60
	recent := make([]int64, 0, len(q.calls[agentID]))
	for _, ts := range q.calls[agentID] {
		if ts >= cutoff {
			recent = append(recent, ts)
		}
	}
	if len(recent) >= q.perMinute {
		q.calls[agentID] = recent
		return false
	}
	recent = append(recent, now)
	q.calls[agentID] = recent
	return true
}

// QuotaStatus 配额状态。
type QuotaStatus struct {
	PerMinute  int            `json:"perMinute"`
	AgentUsage map[string]int `json:"agentUsage"` // agentID → 当前窗口内调用次数
}

// Quota 返回当前配额状态（v0.8.5）。
func (h *HybridProvider) Quota() QuotaStatus {
	s := QuotaStatus{PerMinute: 0, AgentUsage: map[string]int{}}
	if h.quota == nil {
		return s
	}
	s.PerMinute = h.quota.perMinute
	now := time.Now().Unix()
	cutoff := now - 60
	for agentID, calls := range h.quota.calls {
		cnt := 0
		for _, ts := range calls {
			if ts >= cutoff {
				cnt++
			}
		}
		s.AgentUsage[agentID] = cnt
	}
	return s
}

// SetQuota 设置每分钟云端调用上限（v0.8.5）。
func (h *HybridProvider) SetQuota(perMinute int) {
	h.quota = newCloudQuota(perMinute)
}

// ModelEventBus 模型事件总线接口（避免 model 包直接依赖 eventbus）。
type ModelEventBus interface {
	PublishModelCall(agentID string, payload map[string]any)
}

// callStats 模型调用统计（内存）。
type callStats struct {
	totalCalls     int
	edgeCalls      int
	cloudCalls     int
	fallbackCalls  int
	totalPromptTok int
	totalComplTok  int
}

// NewHybrid 构造混合模型层。
func NewHybrid(edge, cloud Provider) *HybridProvider {
	return &HybridProvider{Edge: edge, Cloud: cloud, stats: &callStats{}, quota: newCloudQuota(0)}
}

// SetBus 注入事件总线（v0.8.3）。
func (h *HybridProvider) SetBus(bus ModelEventBus) {
	h.bus = bus
}

// SetCloud 运行时切换主云端模型（v0.8.7：不重启换模型）。
func (h *HybridProvider) SetCloud(p Provider) {
	h.Cloud = p
}

// SetCloudFallback 运行时设置/清除备云端模型（v0.8.7）。
func (h *HybridProvider) SetCloudFallback(p Provider) {
	h.CloudFb = p
}

// Stats 返回当前调用统计（v0.8.3）。
func (h *HybridProvider) Stats() (total, edge, cloud, fallback, promptTok, complTok int) {
	s := h.stats
	return s.totalCalls, s.edgeCalls, s.cloudCalls, s.fallbackCalls, s.totalPromptTok, s.totalComplTok
}

// routeThreshold 端侧置信度低于此值时转云端（v0.8.2）。
const routeThreshold = 0.6

// Chat 智能路由：端侧先做意图粗分类；置信度低（模糊/未识别）走云端大模型。
func (h *HybridProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	edgeResp, err := h.Edge.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	// 端侧置信度足够高：直接用端侧结果
	if edgeResp.Confidence >= routeThreshold {
		edgeResp.RoutedTo = "edge"
		h.recordCall(req, edgeResp)
		return edgeResp, nil
	}
	// 置信度低：转云端大模型（v0.8.5：先检查配额）
	agentID := req.AgentID
	if h.quota != nil && !h.quota.allow(agentID) {
		// 配额超限：降级回端侧（不报错）
		edgeResp.RoutedTo = "edge(quota)"
		h.recordCall(req, edgeResp)
		return edgeResp, nil
	}
	cloudResp, err := h.Cloud.Chat(ctx, req)
	if err != nil && h.CloudFb != nil {
		// v0.8.4：主云端失败，尝试备云端
		cloudResp, err = h.CloudFb.Chat(ctx, req)
	}
	if err != nil {
		// 云端（含备）全部失败：降级回端侧
		edgeResp.RoutedTo = "edge(fallback)"
		h.recordCall(req, edgeResp)
		return edgeResp, nil
	}
	cloudResp.RoutedTo = "cloud"
	h.recordCall(req, cloudResp)
	return cloudResp, nil
}

// recordCall 记录调用统计并发事件（v0.8.3）。
func (h *HybridProvider) recordCall(req *ChatRequest, resp *ChatResponse) {
	s := h.stats
	s.totalCalls++
	s.totalPromptTok += resp.UsagePrompt
	s.totalComplTok += resp.UsageCompletion
	switch resp.RoutedTo {
	case "edge":
		s.edgeCalls++
	case "cloud":
		s.cloudCalls++
	case "edge(fallback)":
		s.fallbackCalls++
	}
	if h.bus != nil {
		h.bus.PublishModelCall(req.AgentID, map[string]any{
			"userId":        req.UserID,
			"agentId":       req.AgentID,
			"model":         resp.Model,
			"routedTo":      resp.RoutedTo,
			"confidence":    resp.Confidence,
			"promptTok":     resp.UsagePrompt,
			"completionTok": resp.UsageCompletion,
			"durationMs":    resp.Duration.Milliseconds(),
		})
	}
}

// Name 实现 Provider。
func (h *HybridProvider) Name() string {
	return "hybrid(edge:" + h.Edge.Name() + ",cloud:" + h.Cloud.Name() + ")"
}

// ---- B1：SSE 流式调用（首 token 即出，降低感知延迟）----

// ChatStream 以 stream=true 调用上游，每个增量文本通过 onDelta 回调即时吐出。
func (o *OpenAICompatible) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(delta string)) (*ChatResponse, error) {
	if o.apiKey == "" || o.baseURL == "" || o.model == "" {
		return nil, fmt.Errorf("model: stream unavailable (stub/offline)")
	}
	payload := map[string]any{"model": o.model, "messages": req.Messages, "temperature": req.Temperature, "stream": true}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	body, _ := json.Marshal(payload)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model: upstream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("model: upstream status %d: %s", resp.StatusCode, string(raw))
	}

	var sb strings.Builder
	br := bufio.NewReader(resp.Body)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			tok := chunk.Choices[0].Delta.Content
			sb.WriteString(tok)
			if onDelta != nil {
				onDelta(tok)
			}
		}
	}
	return &ChatResponse{Content: sb.String(), Model: o.model, Duration: time.Since(start)}, nil
}
