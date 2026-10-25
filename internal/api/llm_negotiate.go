// v1.3.0：LLM 驱动协商轮次。
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

// llmTurn 让 Agent 自动调用大模型生成下一轮提议。
func (d *Deps) llmTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := d.Store.Negotiation.LoadSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}

	history := ""
	for i, t := range sess.Turns {
		history += fmt.Sprintf("轮次%d: %s→%s, %s, %s\n", i+1, t.FromAgentID, t.ToAgentID, t.Intent, toJSON(t.Payload))
	}
	sysPrompt := fmt.Sprintf("你是谈判Agent。状态:%s 轮次:%d/%d\n历史:\n%s\n只输出JSON {\"intent\":\"PROPOSE|ACCEPT|REJECT|COUNTER\",\"payload\":{...}}",
		sess.State, sess.Round, sess.MaxRounds, history)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := d.Model.Chat(ctx, &model.ChatRequest{
		Messages:    []model.Message{{Role: "system", Content: sysPrompt}, {Role: "user", Content: "下一轮:"}},
		Temperature: 0.3, MaxTokens: 300, AgentID: "negotiation-agent",
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "LLM: "+err.Error())
		return
	}

	var parsed struct {
		Intent  string         `json:"intent"`
		Payload map[string]any `json:"payload"`
	}
	if json.Unmarshal([]byte(resp.Content), &parsed) != nil {
		writeJSON(w, 200, map[string]any{"type": "llm_raw", "content": resp.Content})
		return
	}

	state, err := sess.Apply(negotiation.Turn{
		SessionID: id, FromAgentID: "llm-agent", ToAgentID: "counterpart",
		Intent: negotiation.Intent(parsed.Intent), Payload: parsed.Payload,
		DeadlineUnix: time.Now().Add(30 * time.Minute).Unix(),
	})
	if err != nil {
		_ = d.Store.Negotiation.SaveSession(r.Context(), sess)
		writeJSON(w, 422, map[string]any{"error": err.Error(), "state": string(state)})
		return
	}
	_ = d.Store.Negotiation.SaveSession(r.Context(), sess)

	result := map[string]any{"type": "llm_turn_applied", "llm": resp.Content, "session": sessionJSON(sess)}
	d.Stream.Broadcast(id, result)
	writeJSON(w, 200, result)
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
