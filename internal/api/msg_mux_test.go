package api

// v2.3.27（task-12）：NewMsgMuxWithStore 的 HTTP 级回归测试。
//
// 修的是真实生产故障：NewMsgMuxWithStore 曾把 base mux 挂到 "/v1/seq" 前缀下，
// 而 base 注册的是绝对 pattern（POST /v1/msg/seq 等），http.ServeMux **不剥前缀**，
// 于是三个端点在真实网关（只挂 WithStore）上恒为 404；而 -selftest 单独 new 了一个
// mux 直接挂 base，所以一直是绿的。本测试用**与生产同一条装配路径**
// （NewMsgMuxWithStore）起 httptest，断言端点可达，防止回退。
//
// 复用 api_test.go 的 doJSON 辅助函数（返回 (status, 解析后的 JSON map)）。

import (
	"net/http"
	"testing"

	"human-agent-im/internal/store"
)

// mustNum 把 JSON 数字（float64）取成 uint64，顺便断言存在。
func mustNum(t *testing.T, body map[string]any, key string) uint64 {
	t.Helper()
	v, ok := body[key]
	if !ok {
		t.Fatalf("响应缺字段 %q: %v", key, body)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("字段 %q 不是数字: %#v", key, v)
	}
	return uint64(f)
}

func TestMsgMuxWithStore_SeqEndpointsReachable(t *testing.T) {
	// 与生产一致：只经 NewMsgMuxWithStore 装配（不再自己拼 mux）。
	h := NewMsgMuxWithStore("msg-svc", store.NewMemoryStore(0).Message)

	const uid = 907654321 // 独立 sender，避免与其它测试共享单例状态互相干扰

	// 1) POST /v1/msg/seq
	code, body := doJSON(t, h, "POST", "/v1/msg/seq", map[string]any{"userId": uid})
	if code == http.StatusNotFound {
		t.Fatalf("/v1/msg/seq 仍是 404（base 挂载方式又退回去了？）body=%v", body)
	}
	if code != http.StatusOK {
		t.Fatalf("/v1/msg/seq code=%d body=%v", code, body)
	}
	seq := mustNum(t, body, "seq")
	if seq == 0 {
		t.Fatalf("seq 应 >0, body=%v", body)
	}

	// 2) POST /v1/msg/seq/batch
	code, body = doJSON(t, h, "POST", "/v1/msg/seq/batch", map[string]any{"userId": uid, "n": 3})
	if code == http.StatusNotFound {
		t.Fatalf("/v1/msg/seq/batch 仍是 404 body=%v", body)
	}
	if code != http.StatusOK {
		t.Fatalf("/v1/msg/seq/batch code=%d body=%v", code, body)
	}
	rawSeqs, ok := body["seqs"].([]any)
	if !ok || len(rawSeqs) != 3 {
		t.Fatalf("批次应 3 个 seq, got %#v", body["seqs"])
	}
	first := uint64(rawSeqs[0].(float64))
	last := uint64(rawSeqs[len(rawSeqs)-1].(float64))
	// 单例一致性：批次首个 seq 必须紧接上一步的单条 seq。
	if first != seq+1 {
		t.Fatalf("单条 seq=%d 与批次首个 seq=%d 不连续 → 不是同一个分配器（单例化失效）", seq, first)
	}

	// 3) GET /v1/msg/stats
	code, body = doJSON(t, h, "GET", "/v1/msg/stats", nil)
	if code == http.StatusNotFound {
		t.Fatalf("/v1/msg/stats 仍是 404 body=%v", body)
	}
	if code != http.StatusOK {
		t.Fatalf("/v1/msg/stats code=%d body=%v", code, body)
	}

	// 4) 修复 A 不能弄坏 B：/v1/msg/send 仍可用，且与上面共用同一分配器。
	code, body = doJSON(t, h, "POST", "/v1/msg/send", map[string]any{
		"senderId": uid, "receiverId": 3, "content": "hello",
	})
	if code != http.StatusCreated {
		t.Fatalf("/v1/msg/send code=%d body=%v（应 201）", code, body)
	}
	if sentSeq := mustNum(t, body, "seq"); sentSeq != last+1 {
		t.Fatalf("send 分配 seq=%d，但批次末位=%d → send 与 seq 端点不是同一个分配器",
			sentSeq, last)
	}

	// 5) 既有路由不受影响。
	if code, body = doJSON(t, h, "GET", "/healthz", nil); code != http.StatusOK {
		t.Fatalf("/healthz code=%d body=%v", code, body)
	}
	// 未知路径仍应 404（挂 "/" 不能把 404 吞成 200）。
	if code, _ = doJSON(t, h, "GET", "/v1/msg/no-such-route", nil); code == http.StatusOK {
		t.Fatal("未知路径返回 200：mux.Handle(\"/\", base) 覆盖过宽")
	}
}
