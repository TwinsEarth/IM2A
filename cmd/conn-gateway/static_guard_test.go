package main

// static_guard_test.go：把设计 §4.4 的三条"防翻车"规则变成可执行的静态检查。
//
// 为什么用源码扫描而不是行为测试：gorilla 的自动 pong/close 回复与我们的裸写之间的
// 竞态是**概率性**的，行为测试很难稳定复现；而"有没有调用 gorilla 的写 API"是确定的。

import (
	"os"
	"strings"
	"testing"
)

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestWritePathHasNoGorillaWrites wssink.go（写路径）不得出现任何 gorilla 写调用。
func TestWritePathHasNoGorillaWrites(t *testing.T) {
	src := mustRead(t, "wssink.go")
	for _, banned := range []string{".WriteMessage(", ".WriteJSON(", ".NextWriter(", ".WriteControl("} {
		if strings.Contains(src, banned) {
			t.Fatalf("wssink.go 出现 gorilla 写调用 %q：写路径必须走裸 net.Conn + net.Buffers(writev)，否则与读循环并发写同一 socket", banned)
		}
	}
}

// TestGorillaAutoRepliesDisabled 两条自动回复必须显式置空（设计 §4.4 规则 1/2）。
func TestGorillaAutoRepliesDisabled(t *testing.T) {
	src := mustRead(t, "wssink.go")
	for _, must := range []string{
		"SetPingHandler(func(string) error { return nil })",
		"SetCloseHandler(func(int, string) error { return nil })",
		"net.Buffers",
	} {
		if !strings.Contains(src, must) {
			t.Fatalf("wssink.go 缺少必须项 %q（自动 pong/close 未禁用，或 writev 批量被改掉）", must)
		}
	}
}

// TestMainGorillaWriteOnlyForHandshakeFailure main.go 里唯一允许的 gorilla 写是
// 握手失败时回一个 err 帧（此刻 WSSink 还没创建，不存在并发写）。
func TestMainGorillaWriteOnlyForHandshakeFailure(t *testing.T) {
	src := mustRead(t, "main.go")
	if n := strings.Count(src, ".WriteMessage("); n > 1 {
		t.Fatalf("main.go 出现 %d 处 gorilla 写调用（只允许 closeWithReason 里 1 处）", n)
	}
	if !strings.Contains(src, "NewWSSink(") {
		t.Fatal("main.go 未使用 WSSink：写路径必须集中在 wssink.go")
	}
}
