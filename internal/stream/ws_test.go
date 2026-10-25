package stream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func startHub(t *testing.T, h *Hub) (hub *Hub, wsURL string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWS(r.URL.Query().Get("topic"), w, r)
	}))
	t.Cleanup(srv.Close)
	return h, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dial(t *testing.T, wsURL, topic string) *websocket.Conn {
	c, _, err := websocket.DefaultDialer.Dial(wsURL+"/?topic="+topic, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", topic, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// readOne 在超时内读一条文本消息。
func readOne(t *testing.T, c *websocket.Conn, wait time.Duration) []byte {
	_ = c.SetReadDeadline(time.Now().Add(wait))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

// v1.1.0：升级 101 + 首帧 connected + Stats 计数真实。
func TestV110HandshakeAndStats(t *testing.T) {
	h, url := startHub(t, NewHub())
	c := dial(t, url, "t110")
	got := string(readOne(t, c, 2*time.Second))
	if !strings.Contains(got, `"type":"connected"`) || !strings.Contains(got, "t110") {
		t.Fatalf("首帧不是 connected: %s", got)
	}
	st := h.Stats()
	if st["t110"] != 1 || st["_total"] != 1 {
		t.Fatalf("stats 不真实: %v", st)
	}
}

// v1.1.1/v1.1.3：同 topic 两个连接，handler 结果必须广播给所有人（含发送者）。
func TestV111HandlerBroadcast(t *testing.T) {
	h, url := startHub(t, NewHub())
	h.RegisterHandler("room", func(nid string, in map[string]any) (any, error) {
		return map[string]any{"type": "turn", "echo": in["x"]}, nil
	})
	c1 := dial(t, url, "room")
	c2 := dial(t, url, "room")
	readOne(t, c1, time.Second) // connected
	readOne(t, c2, time.Second) // connected

	if err := c1.WriteJSON(map[string]any{"action": "turn", "x": "hello"}); err != nil {
		t.Fatal(err)
	}
	// 发送者与旁观者都应收到广播
	r1 := string(readOne(t, c1, 2*time.Second))
	r2 := string(readOne(t, c2, 2*time.Second))
	if !strings.Contains(r1, `"echo":"hello"`) {
		t.Fatalf("发送者未收到广播: %s", r1)
	}
	if !strings.Contains(r2, `"echo":"hello"`) {
		t.Fatalf("旁观者未收到广播: %s", r2)
	}
}

// v1.1.3：无 handler 时回显（通用端点）。
func TestV113Echo(t *testing.T) {
	_, url := startHub(t, NewHub())
	c := dial(t, url, "echo-room")
	readOne(t, c, time.Second) // connected
	if err := c.WriteJSON(map[string]any{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	got := string(readOne(t, c, 2*time.Second))
	if !strings.Contains(got, `"hello":"world"`) {
		t.Fatalf("未回显: %s", got)
	}
}

// v1.1.4：连接不回 Pong（不读控制帧），必须在 pongWait 后被读超时踢出并注销，Stats 归零。
// 旧版 cleanup 无视 ping 错误且无独立 ping ticker，死连接永不清理——此测试即针对该缺陷。
func TestV114DeadConnCleanup(t *testing.T) {
	h, url := startHub(t, NewHubWithIntervals(300*time.Millisecond, 150*time.Millisecond, 100*time.Millisecond))
	c := dial(t, url, "dead")
	readOne(t, c, time.Second) // 读完 connected 后停止 ReadMessage → 不处理 Ping、不回 Pong

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.Stats()["_total"] == 0 {
			return // 死连接已被真实清理
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("死连接未被清理，stats=%v", h.Stats())
}

// v1.1.4：大量 goroutine 并发广播 + 服务端 ping，必须无数据竞争/无 panic（配合 go test -race）。
func TestV11xConcurrentBroadcastNoRace(t *testing.T) {
	h, url := startHub(t, NewHubWithIntervals(200*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond))
	c := dial(t, url, "race")
	readOne(t, c, time.Second) // connected
	// 持续读取，避免接收缓冲满
	go func() {
		for {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Broadcast("race", map[string]any{"type": "x", "n": j})
			}
		}()
	}
	wg.Wait()
	// 走到这里且 -race 未报告即说明并发写安全
	if h.Stats()["race"] != 1 {
		t.Fatalf("连接应仍存活，stats=%v", h.Stats())
	}
}

// v2.3.28 / B2-c：默认（未设置 mirror）时 Broadcast 行为逐字节不变——
// 订阅者照常收到、不调用任何镜像；这是 embedded 生产路径的守卫测试。
func TestMirrorDefaultNilKeepsBroadcastBehavior(t *testing.T) {
	h, url := startHub(t, NewHubWithIntervals(time.Minute, 30*time.Second, time.Minute))
	if h.HasMirror() {
		t.Fatal("新 Hub 默认不应有 mirror")
	}
	c := dial(t, url, "mirror-topic")
	readOne(t, c, time.Second) // connected

	msg := map[string]any{"type": "trust_granted", "level": 3}
	h.Broadcast("mirror-topic", msg)
	got := string(readOne(t, c, 2*time.Second))
	if !strings.Contains(got, `"trust_granted"`) {
		t.Fatalf("默认路径广播未送达: %s", got)
	}
	// nil 状态下再广播多次不得 panic、不得改变统计。
	for i := 0; i < 10; i++ {
		h.Broadcast("no-such-topic", msg)
	}
	if h.Stats()["mirror-topic"] != 1 {
		t.Fatalf("订阅者数量被 mirror 逻辑影响: %v", h.Stats())
	}
}

// B2-c：设置 mirror 后，本地订阅者照常收到，镜像回调收到 (topic, msg)；
// SetMirror(nil) 后镜像立刻停止（关机撤钩子路径）。
func TestMirrorOptInDelivery(t *testing.T) {
	h, url := startHub(t, NewHubWithIntervals(time.Minute, 30*time.Second, time.Minute))
	c := dial(t, url, "trust:424242")
	readOne(t, c, time.Second) // connected

	type mirrorCall struct {
		topic string
		msg   any
	}
	calls := make(chan mirrorCall, 4)
	h.SetMirror(func(topic string, msg any) {
		calls <- mirrorCall{topic: topic, msg: msg}
	})
	if !h.HasMirror() {
		t.Fatal("SetMirror 后 HasMirror=false")
	}
	want := map[string]any{"type": "trust_granted", "level": 3}
	h.Broadcast("trust:424242", want)

	// 本地订阅者仍收到（旧行为不受影响）。
	got := string(readOne(t, c, 2*time.Second))
	if !strings.Contains(got, `"trust_granted"`) {
		t.Fatalf("本地订阅者未收到: %s", got)
	}
	select {
	case call := <-calls:
		if call.topic != "trust:424242" {
			t.Fatalf("mirror topic = %q", call.topic)
		}
		m, ok := call.msg.(map[string]any)
		if !ok || m["level"] != 3 {
			t.Fatalf("mirror msg = %#v", call.msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mirror 回调未被调用")
	}

	h.SetMirror(nil)
	if h.HasMirror() {
		t.Fatal("SetMirror(nil) 后 HasMirror=true")
	}
	h.Broadcast("trust:424242", want)
	select {
	case <-calls:
		t.Fatal("SetMirror(nil) 后 mirror 仍被调用")
	case <-time.After(50 * time.Millisecond):
	}
}
