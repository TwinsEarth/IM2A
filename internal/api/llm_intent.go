// v1.3.2: LLM 意图预审（真实 Cloud 调用）。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"human-agent-im/internal/model"
)

// IntentResult 预审结果。
type IntentResult struct {
	Category string `json:"category"` // spam/ad/suspicious/normal
	Reason   string `json:"reason"`
	Risk     int    `json:"risk"`   // 0-10
	Charge   bool   `json:"charge"` // 是否收费
}

func (d *Deps) intentPrecheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		FromID string `json:"fromId"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Text == "" {
		writeErr(w, 400, "text required")
		return
	}

	prompt := `你是陌生人消息预审员。判断来意：
- spam: 垃圾/广告/诈骗/刷屏
- ad: 商业推广/带货
- suspicious: 可疑但不确定
- normal: 正常沟通

输出 JSON: {"category":"...","reason":"...","risk":0-10,"charge":true/false}
charge=true 表示需要付费才能触达。

消息：` + req.Text

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	resp, err := d.Model.Chat(ctx, &model.ChatRequest{
		Messages:    []model.Message{{Role: "system", Content: prompt}},
		Temperature: 0, MaxTokens: 200, AgentID: "intent-precheck",
	})
	if err != nil {
		// LLM 失败安全兜底：suspicious, risk=7, charge=true
		writeJSON(w, 200, IntentResult{Category: "suspicious", Reason: "预审服务暂不可用，默认高风险", Risk: 7, Charge: true})
		return
	}

	var ir IntentResult
	if json.Unmarshal([]byte(resp.Content), &ir) != nil {
		writeJSON(w, 200, IntentResult{Category: "suspicious", Reason: "无法解析: " + resp.Content, Risk: 7, Charge: true})
		return
	}
	if d.DB != nil {
		payload, _ := json.Marshal(ir)
		d.DB.Exec(`INSERT INTO audit_events(agent_id, action, detail) VALUES($1,$2,$3)`, "intent-precheck", "intent_precheck", payload)
	}
	writeJSON(w, 200, ir)
}

func (d *Deps) LLMIntentMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/intent/precheck", d.intentPrecheck)
	return mux
}
