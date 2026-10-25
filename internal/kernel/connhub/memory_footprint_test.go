package connhub

// memory_footprint_test.go：task-24 连接内存模型的结构体尺寸基线。
//
// 目的不是断言运行时 RSS（那受 goroutine 栈/gorilla buffer/GC 影响），而是把
// **每连接静态结构**的尺寸锁死，防止以后有人往 Conn/ring/Frame 里塞大字段而没人发现。
// 2026-10-25 task-24：ring 改成惰性分配后，空闲连接的静态占用应接近于 0。

import (
	"testing"
	"unsafe"

	"human-agent-im/internal/connproto"
)

func TestMemoryFootprint(t *testing.T) {
	frame := unsafe.Sizeof(connproto.Frame{})
	conn := unsafe.Sizeof(Conn{})
	rg := unsafe.Sizeof(ring{})
	// 输出到 -v 日志，供 docs/notes-conn-memory.md 引用
	t.Logf("sizeof: Frame=%d B, Conn=%d B, ring=%d B (惰性队列，空时 0 B 槽位)", frame, conn, rg)
	// 防止回归：单帧头结构 ≤ 80B（8B 对齐），Conn+ring 静态结构 ≤ 1KB
	if frame > 80 {
		t.Fatalf("connproto.Frame 尺寸膨胀: %d B", frame)
	}
	if conn+rg > 1024 {
		t.Fatalf("Conn+ring 静态结构膨胀: %d B", conn+rg)
	}
	// 惰性 ring：新建时不得有任何槽位分配
	r := newRing(256, 16384, 32)
	if cap(r.q) != 0 || len(r.q) != 0 {
		t.Fatalf("newRing 不应预分配槽位: len=%d cap=%d", len(r.q), cap(r.q))
	}
}