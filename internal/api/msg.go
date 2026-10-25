// msg-svc HTTP 端点：消息序号生成（64 分片锁 + FNV），消息正文存储，运维监控。
package api

import (
	"net/http"
	"strconv"
	"time"

	"human-agent-im/internal/id"
	"human-agent-im/internal/msgseq"
	"human-agent-im/internal/store"
)

// SeqRequest 单条序号请求。
type SeqRequest struct {
	UserID uint64 `json:"userId"`
}

// SeqBatchRequest 批量序号请求。
type SeqBatchRequest struct {
	UserID uint64 `json:"userId"`
	N      int    `json:"n"`
}

// SendMessageRequest 发送消息（v0.5.2 新增）。
type SendMessageRequest struct {
	SenderID     uint64         `json:"senderId"`
	ReceiverID   uint64         `json:"receiverId"`
	Content      string         `json:"content"`
	BizType      string         `json:"bizType"`      // 空=text；approval_card/agent_summary/paid...
	AgentContext map[string]any `json:"agentContext"` // Agent 业务上下文
}

// NewMsgMux 挂载 msg-svc 路由（内含进程内分片生成器）。
func NewMsgMux(serviceName string) http.Handler {
	// v2.3.27：进程级单例（重启不归零靠启动播种，跨进程靠唯一索引兜底）
	gen := msgseq.Default()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/msg/seq", func(w http.ResponseWriter, r *http.Request) {
		var req SeqRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		seq, err := gen.Next(req.UserID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"userId": req.UserID, "seq": seq})
	})

	mux.HandleFunc("POST /v1/msg/seq/batch", func(w http.ResponseWriter, r *http.Request) {
		var req SeqBatchRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		seqs, err := gen.NextBatch(req.UserID, req.N)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"userId": req.UserID, "seqs": seqs, "count": len(seqs)})
	})

	mux.HandleFunc("GET /v1/msg/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"hotShards": gen.HotShardIDs(),
			"summary":   gen.Summary(),
		})
	})

	return mux
}

// NewMsgMuxWithStore 挂载带消息存储的 msg-svc 路由（v0.5.2 新增）。
func NewMsgMuxWithStore(serviceName string, msgStore store.MessageStore) http.Handler {
	// v2.3.27：进程级单例（重启不归零靠启动播种，跨进程靠唯一索引兜底）
	gen := msgseq.Default()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	// 复用基础路由（v2.3.27 修 /v1/msg/seq 生产 404）：
	//   旧写法 mux.Handle("/v1/seq", base) 是错的——http.ServeMux 挂 handler **不剥前缀**，
	//   而 base 注册的是绝对 pattern（POST /v1/msg/seq 等），于是 /v1/msg/seq 到不了 base；
	//   /v1/seq/v1/msg/seq 这种拼出来的路径也永远没人请求。结果是三个端点在真实网关上 404，
	//   而 -selftest 单独 new 了一个 mux 直接挂 base，所以一直是绿的（掩盖了故障）。
	//   挂到 "/" 后：更具体的 pattern（send/get/list/healthz）优先，其余落到 base，base 用
	//   原始完整路径匹配自己的绝对 pattern → 三条端点恢复可达。
	//   注意 base 与这里都取 msgseq.Default()，全程只有一个分配器实例。
	base := NewMsgMux(serviceName)
	mux.Handle("/", base)

	// 发送消息（分配序号 + 保存正文）
	mux.HandleFunc("POST /v1/msg/send", func(w http.ResponseWriter, r *http.Request) {
		var req SendMessageRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.SenderID == 0 || req.ReceiverID == 0 || req.Content == "" {
			writeErr(w, http.StatusBadRequest, "senderId/receiverId/content required")
			return
		}
		seq, err := gen.Next(req.SenderID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		msg := &store.Message{
			MessageID:    id.UUIDv4(),
			SenderID:     req.SenderID,
			ReceiverID:   req.ReceiverID,
			BizType:      req.BizType,
			Content:      req.Content,
			AgentContext: req.AgentContext,
			Seq:          int64(seq),
			CreatedAt:    time.Now().Unix(),
		}
		if err := msgStore.SaveMessage(r.Context(), msg); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, msg)
	})

	// 加载消息
	mux.HandleFunc("GET /v1/msg/get", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "id required")
			return
		}
		msg, err := msgStore.LoadMessage(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if msg == nil {
			writeErr(w, http.StatusNotFound, "message not found")
			return
		}
		writeJSON(w, http.StatusOK, msg)
	})

	// 列出用户消息
	mux.HandleFunc("GET /v1/msg/list", func(w http.ResponseWriter, r *http.Request) {
		uidStr := r.URL.Query().Get("userId")
		uid, _ := strconv.ParseUint(uidStr, 10, 64)
		if uid == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		limit := 20
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		msgs, err := msgStore.ListMessages(r.Context(), uid, limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "count": len(msgs)})
	})

	return mux
}
