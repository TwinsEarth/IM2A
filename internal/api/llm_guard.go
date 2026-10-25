// v1.3.3: LLM 出口护栏（真实 Cloud 调用）。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"human-agent-im/internal/model"
)

type GuardResult struct {
	Allowed  bool   `json:"allowed"`
	Level    string `json:"level"` // safe/warn/block
	Reason   string `json:"reason"`
	Redacted string `json:"redacted,omitempty"`
}

func (d *Deps) guardReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromAgentID string `json:"fromAgentId"`
		ToUserID    string `json:"toUserId"`
		Text        string `json:"text"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Text == "" {
		writeErr(w, 400, "text required")
		return
	}

	prompt := `审查 Agent 外发消息。检查：
- 不含手机号/身份证/银行卡/家庭住址
- 不含辱骂/威胁/歧视
- 不含恶意链接

输出 JSON: {"allowed":true/false,"level":"safe|warn|block","reason":"...","redacted":"脱敏后文本"}

原文：` + req.Text

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	resp, err := d.Model.Chat(ctx, &model.ChatRequest{
		Messages:    []model.Message{{Role: "system", Content: prompt}},
		Temperature: 0, MaxTokens: 200, AgentID: "exit-guard",
	})
	if err != nil {
		writeJSON(w, 200, GuardResult{Allowed: false, Level: "block", Reason: "护栏不可用，默认拦截"})
		return
	}
	var gr GuardResult
	if json.Unmarshal([]byte(resp.Content), &gr) != nil {
		writeJSON(w, 200, GuardResult{Allowed: false, Level: "block", Reason: "无法解析，默认拦截: " + resp.Content})
		return
	}
	writeJSON(w, 200, gr)
}

func (d *Deps) LLMGuardMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/guard/review", d.guardReview)
	return mux
}
