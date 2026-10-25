// v1.3.4: LLM 蒸馏闭环（真实 Cloud 调用）。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"human-agent-im/internal/model"
)

type DistillResult struct {
	Rule           string  `json:"rule"`
	ThresholdDelta float64 `json:"thresholdDelta"`
	AppliedTo      string  `json:"appliedTo"`
}

func (d *Deps) distillFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID  string `json:"agentId"`
		Decision string `json:"decision"`
		Context  string `json:"context"`
		Feedback string `json:"feedback"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.AgentID == "" {
		writeErr(w, 400, "agentId required")
		return
	}

	prompt := `根据用户审批反馈提炼规则。
Agent: ` + req.AgentID + `
决策: ` + req.Decision + `
上下文: ` + req.Context + `
反馈: ` + req.Feedback + `

输出 JSON: {"rule":"...","thresholdDelta":0.3,"appliedTo":"` + req.AgentID + `"}
thresholdDelta: 正值收紧决策（reject 时通常 0.2-0.5），负值放松（approve 时 -0.1）。`

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := d.Model.Chat(ctx, &model.ChatRequest{
		Messages:    []model.Message{{Role: "system", Content: prompt}},
		Temperature: 0.2, MaxTokens: 200, AgentID: "distill",
	})
	if err != nil {
		writeErr(w, 500, "llm failed: "+err.Error())
		return
	}
	var res DistillResult
	if json.Unmarshal([]byte(resp.Content), &res) != nil {
		// fallback: 根据决策给默认 delta
		res.Rule = resp.Content
		if req.Decision == "reject" {
			res.ThresholdDelta = 0.3
		} else {
			res.ThresholdDelta = -0.1
		}
		res.AppliedTo = req.AgentID
	}
	if d.DB != nil {
		payload, _ := json.Marshal(res)
		d.DB.Exec(`INSERT INTO audit_events(agent_id, action, detail) VALUES($1,$2,$3)`, "distill", "distill_feedback", payload)
	}
	writeJSON(w, 200, res)
}

func (d *Deps) LLMDistillMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", d.distillFeedback)
	return mux
}
