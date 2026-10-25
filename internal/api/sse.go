// v1.6.0: SSE 审批实时推送（审批态客户端实时收到新卡片）。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSERepo 内存订阅者。
type SSERepo struct {
	mu   sync.Mutex
	subs map[string]map[chan string]bool // userId → subscribers
}

func NewSSERepo() *SSERepo {
	return &SSERepo{subs: make(map[string]map[chan string]bool)}
}

var sseHub = NewSSERepo()

// Publish 向某用户推送一条审批事件。
func (r *SSERepo) Publish(userID, event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subs[userID] {
		select {
		case ch <- event:
		default: // 慢消费者不阻塞
		}
	}
}

// SSEStream GET /v1/sse/?userId=alice
func (d *Deps) SSEStream(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		writeErr(w, 400, "userId required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 8)
	sseHub.mu.Lock()
	if sseHub.subs[userID] == nil {
		sseHub.subs[userID] = make(map[chan string]bool)
	}
	sseHub.subs[userID][ch] = true
	sseHub.mu.Unlock()

	defer func() {
		sseHub.mu.Lock()
		delete(sseHub.subs[userID], ch)
		sseHub.mu.Unlock()
		close(ch)
	}()

	fmt.Fprintf(w, "event: connected\ndata: {\"userId\":%q}\n\n", userID)
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			fmt.Fprintf(w, "event: approval\ndata: %s\n\n", ev)
			flusher.Flush()
		}
	}
}

// SSEPublish POST /v1/sse/publish（测试用：推送一条审批）
func (d *Deps) SSEPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"userId"`
		Event  string `json:"event"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.UserID == "" {
		writeErr(w, 400, "userId required")
		return
	}
	sseHub.Publish(req.UserID, req.Event)
	writeJSON(w, 200, map[string]any{"published": true, "userId": req.UserID})
}
