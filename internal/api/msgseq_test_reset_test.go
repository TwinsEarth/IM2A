package api

// msgseq_test_reset_test.go：测试基建（不属于产品代码）。
//
// 背景（task-14 收口修复）：
//   `msgseq.Default()` 是**进程级单例**（生产上这是必需的：同一进程只能有一个发号器）。
//   但 `go test -count=N` 会在**同一个测试进程**里把测试跑 N 遍，单例会跨轮累积，
//   于是"断言首个序号 == 1"的老测试在第二、三轮变红。
//
// 这不是产品 bug，而是测试没管好全局状态。修法（lead 推荐 A）：
//   每个测试开始前把单例换成一个全新的 Generator，并用 t.Cleanup 还原——
//   保证 (a) 单跑/多跑结果一致；(b) 测试之间互不污染；(c) 生产单例语义一行不改。

import (
	"testing"

	"human-agent-im/internal/msgseq"
)

// resetMsgSeq 给当前测试一个干净的序号分配器；测试结束还原（避免影响同包其他测试）。
func resetMsgSeq(t *testing.T) {
	t.Helper()
	prev := msgseq.SetDefault(msgseq.New(1024))
	t.Cleanup(func() { msgseq.SetDefault(prev) })
}
