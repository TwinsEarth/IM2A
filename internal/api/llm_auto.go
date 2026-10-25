// v1.3.1: LLM 多轮自动协商循环。
// 自动调用 LLM 生成下一轮，直到 ACCEPT/REJECT/达到最大轮次。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"human-agent-im/internal/model"
	"human-agent-im/internal/negotiation"
)

// llmAutoRun 自动跑完整协商循环。
func (d *Deps) llmAutoRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := d.Store.Negotiation.LoadSession(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "session not found")
		return
	}

	maxRounds := 5
	if v := r.URL.Query().Get("max"); v != "" {
		fmt.Sscanf(v, "%d", &maxRounds)
	}

	type turnResult struct {
		Round   int    `json:"round"`
		Intent  string `json:"intent"`
		Payload string `json:"payload"`
		State   string `json:"state"`
	}
	history := []turnResult{}

	// 最多跑 maxRounds 轮
	for i := 0; i < maxRounds; i++ {
		if sess.State == negotiation.StateCompleted ||
			sess.State == negotiation.StateFailed ||
			sess.State == negotiation.StateCanceled {
			break
		}

		// 拼 prompt
		prompt := fmt.Sprintf("协商状态:%s 轮次:%d/%d\n历史:\n", sess.State, sess.Round, sess.MaxRounds)
		for _, t := range sess.Turns {
			prompt += fmt.Sprintf("  %s→%s %s %s\n", t.FromAgentID, t.ToAgentID, t.Intent, toJSON(t.Payload))
		}
		prompt += "只输出 JSON {\"intent\":\"PROPOSE|ACCEPT|REJECT|COUNTER\",\"payload\":{...}}"

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		resp, err := d.Model.Chat(ctx, &model.ChatRequest{
			Messages:    []model.Message{{Role: "system", Content: prompt}},
			Temperature: 0.3, MaxTokens: 300, AgentID: "auto-negotiation",
		})
		cancel()
		if err != nil {
			break
		}

		var parsed struct {
			Intent  string         `json:"intent"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal([]byte(resp.Content), &parsed) != nil {
			break
		}

		state, _ := sess.Apply(negotiation.Turn{
			SessionID: id, FromAgentID: "llm-agent", ToAgentID: "counterpart",
			Intent: negotiation.Intent(parsed.Intent), Payload: parsed.Payload,
			DeadlineUnix: time.Now().Add(30 * time.Minute).Unix(),
		})
		_ = d.Store.Negotiation.SaveSession(r.Context(), sess)

		history = append(history, turnResult{
			Round: sess.Round, Intent: parsed.Intent,
			Payload: resp.Content, State: string(state),
		})

		// ACCEPT/REJECT 终止
		if parsed.Intent == "ACCEPT" || parsed.Intent == "REJECT" {
			break
		}
	}

	d.Stream.Broadcast(id, map[string]any{
		"type": "auto_negotiation_done", "session": sessionJSON(sess), "turns": history,
	})
	writeJSON(w, 200, map[string]any{
		"id": id, "finalState": string(sess.State),
		"totalRounds": sess.Round, "turns": history,
	})
}
