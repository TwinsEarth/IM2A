package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"human-agent-im/internal/model"
)

// ChatStreamHandler B1：SSE 流式聊天，首 token 即出。
// POST /v1/chat/stream  body {"messages":[{"role":"user","content":"..."}]}
// 返回 text/event-stream，每行 data: {"delta":"..."}，末尾 data: [DONE]。
func ChatStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Messages []model.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "messages required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	cloud := model.NewOpenAICompatible(
		os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL"),
	)
	write := func(event string) {
		fmt.Fprintf(w, "data: %s\n\n", event)
		flusher.Flush()
	}
	resp, err := cloud.ChatStream(r.Context(), &model.ChatRequest{Messages: req.Messages}, func(delta string) {
		b, _ := json.Marshal(map[string]string{"delta": delta})
		write(string(b))
	})
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		write(string(b))
		return
	}
	done, _ := json.Marshal(map[string]any{"done": true, "model": resp.Model, "ms": resp.Duration.Milliseconds()})
	write(string(done))
}
