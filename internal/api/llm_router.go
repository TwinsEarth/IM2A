// v1.3.5: LLM 总 Agent 路由（真实 Cloud 调用）。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"human-agent-im/internal/model"
)

type RouteResult struct {
	SubAgent   string  `json:"subAgent"`
	Intent     string  `json:"intent"`
	Priority   int     `json:"priority"`
	Summary    string  `json:"summary"`
	Confidence float64 `json:"confidence"`
}

func (d *Deps) llmRoute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"userId"`
		Text   string `json:"text"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Text == "" {
		writeErr(w, 400, "text required")
		return
	}

	prompt := `把用户消息路由到子 Agent：
- schedule: 日程/会议/预约
- purchase: 购物/比价
- property: 物业/维修
- legal: 合同/法务
- education: 学习/教育
- parent: 亲子/家庭
- general: 其他

输出 JSON: {"subAgent":"...","intent":"...","priority":1-5,"summary":"...","confidence":0.9}

消息：` + req.Text

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	resp, err := d.Model.Chat(ctx, &model.ChatRequest{
		Messages:    []model.Message{{Role: "system", Content: prompt}},
		Temperature: 0, MaxTokens: 200, AgentID: "router",
	})
	if err != nil {
		writeJSON(w, 200, RouteResult{SubAgent: "general", Priority: 3, Confidence: 0.5, Summary: "路由暂不可用"})
		return
	}
	var rr RouteResult
	if json.Unmarshal([]byte(resp.Content), &rr) != nil {
		writeJSON(w, 200, RouteResult{SubAgent: "general", Priority: 3, Confidence: 0.5, Summary: resp.Content})
		return
	}
	if d.Stream != nil {
		d.Stream.Broadcast("subagent:"+rr.SubAgent, map[string]any{"type": "new_task", "text": req.Text})
	}
	writeJSON(w, 200, rr)
}

func (d *Deps) LLMRouterMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", d.llmRoute)
	return mux
}
