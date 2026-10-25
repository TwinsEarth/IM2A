// SPDX-License-Identifier: MIT
package connapi

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// captureSink 记录 Deliver 调用（测试 Bridge.Publish 与投递出口的配合）。
type captureSink struct {
	userID int64
	frames []connproto.Frame
	res    connhub.DeliverResult
}

func (c *captureSink) Deliver(userID int64, frames []connproto.Frame) connhub.DeliverResult {
	c.userID = userID
	c.frames = append(c.frames, frames...)
	if c.res == (connhub.DeliverResult{}) {
		c.res.Enqueued = len(frames)
	}
	return c.res
}

func (c *captureSink) Stats() connhub.Stats { return connhub.Stats{} }

func newBridge(t *testing.T) (*Bridge, *delivery.MemStore, *captureSink) {
	t.Helper()
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	b := NewBridge(store, log.New(io.Discard, "", 0))
	sink := &captureSink{}
	b.SetSink(sink)
	return b, store, sink
}

func TestBridgePublishAppendsThenDelivers(t *testing.T) {
	b, store, sink := newBridge(t)
	ctx := context.Background()
	res, err := b.Publish(ctx, 1, []PublishFrame{
		{Payload: []byte("a"), Critical: true},
		{Payload: []byte("b"), Critical: false},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Enqueued != 2 {
		t.Fatalf("deliver enqueued = %d, want 2", res.Enqueued)
	}
	if sink.userID != 1 || len(sink.frames) != 2 {
		t.Fatalf("sink got user=%d frames=%d", sink.userID, len(sink.frames))
	}
	if sink.frames[0].Seq != 1 || sink.frames[1].Seq != 2 {
		t.Fatalf("frame seqs = %d,%d want 1,2", sink.frames[0].Seq, sink.frames[1].Seq)
	}
	last, _ := store.LastSeq(ctx, 1)
	if last != 2 {
		t.Fatalf("LastSeq = %d, want 2", last)
	}
	// 错误参数不写日志。
	if _, err := b.Publish(ctx, 0, []PublishFrame{{Payload: []byte("x")}}); err == nil {
		t.Fatal("Publish userID=0 should fail")
	}
}

func TestBridgeConnOpenAckAndResume(t *testing.T) {
	b, store, sink := newBridge(t)
	ctx := context.Background()
	ack := b.OnConnOpen(connproto.ConnOpen{ConnID: 1, UserID: 7, DeviceID: "ios-e2e"})
	if !ack.OK || ack.Epoch != 0 || ack.ResumeToken == "" {
		t.Fatalf("ConnOpenAck = %+v", ack)
	}
	// 发 3 条，客户端 ack 到 2。
	if _, err := b.Publish(ctx, 7, []PublishFrame{
		{Payload: []byte("m1"), Critical: true},
		{Payload: []byte("m2"), Critical: true},
		{Payload: []byte("m3"), Critical: true},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !b.OnAckUp(connproto.AckUp{UserID: 7, DeviceID: "ios-e2e", ConnEpoch: 0, AckSeq: 2}) {
		t.Fatal("AckUp should be accepted")
	}
	// 断线重连 resume：epoch 匹配 → 只补 seq 3。
	resp, frames := b.OnResume(ctx, connproto.ResumeReq{UserID: 7, DeviceID: "ios-e2e", ConnEpoch: 0, LastAckSeq: 2})
	if !resp.OK || resp.Truncated {
		t.Fatalf("ResumeResp = %+v, want ok && !truncated", resp)
	}
	if resp.From != 3 || resp.To != 3 || resp.Replayed != 1 || len(frames) != 1 {
		t.Fatalf("resume = %+v frames=%d, want from=3 to=3 replayed=1", resp, len(frames))
	}
	if frames[0].Seq != 3 {
		t.Fatalf("replay frame seq = %d, want 3", frames[0].Seq)
	}
	// epoch 不匹配 → 拒绝 resume（ok=false reason=epoch_mismatch）。
	if _, err := b.BumpEpoch(ctx, 7, "ios-e2e"); err != nil {
		t.Fatalf("BumpEpoch: %v", err)
	}
	stale, staleFrames := b.OnResume(ctx, connproto.ResumeReq{UserID: 7, DeviceID: "ios-e2e", ConnEpoch: 0, LastAckSeq: 2})
	if stale.OK || stale.Reason != "epoch_mismatch" || len(staleFrames) != 0 {
		t.Fatalf("stale resume = %+v frames=%d, want rejected epoch_mismatch", stale, len(staleFrames))
	}
	// 旧 epoch 的 AckUp 必须被丢弃（防旧连接污染新位点）。
	if b.OnAckUp(connproto.AckUp{UserID: 7, DeviceID: "ios-e2e", ConnEpoch: 0, AckSeq: 3}) {
		t.Fatal("AckUp with stale epoch must be dropped")
	}
	// 新 epoch 的 ack 正常。
	cur, ok, _ := store.Cursor(ctx, "ios-e2e")
	if !ok || cur.ConnEpoch != 1 {
		t.Fatalf("cursor = %+v, want epoch 1", cur)
	}
	if !b.OnAckUp(connproto.AckUp{UserID: 7, DeviceID: "ios-e2e", ConnEpoch: 1, AckSeq: 3}) {
		t.Fatal("AckUp with new epoch should be accepted")
	}
	_ = sink
}

func TestBridgeResumeTruncation(t *testing.T) {
	b, _, _ := newBridge(t)
	ctx := context.Background()
	b.OnConnOpen(connproto.ConnOpen{ConnID: 1, UserID: 9, DeviceID: "ios-trunc"})
	if _, err := b.Publish(ctx, 9, []PublishFrame{
		{Payload: []byte("m1")}, {Payload: []byte("m2")}, {Payload: []byte("m3")},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// 调整补发上限为 1 帧 → 必须 truncation=true 且 replayed=1。
	b.limits.MaxFrames = 1
	resp, frames := b.OnResume(ctx, connproto.ResumeReq{UserID: 9, DeviceID: "ios-trunc", ConnEpoch: 0, LastAckSeq: 0})
	if !resp.OK || !resp.Truncated || resp.Replayed != 1 || len(frames) != 1 {
		t.Fatalf("resume = %+v frames=%d, want truncated replayed=1", resp, len(frames))
	}
}

func TestBridgePublishWithoutSinkStillPersists(t *testing.T) {
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	b := NewBridge(store, log.New(io.Discard, "", 0))
	res, err := b.Publish(context.Background(), 3, []PublishFrame{{Payload: []byte("x"), Critical: true}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Enqueued != 0 {
		t.Fatalf("no sink should not enqueue, got %+v", res)
	}
	last, _ := store.LastSeq(context.Background(), 3)
	if last != 1 {
		t.Fatalf("LastSeq = %d, want 1 (日志必须已持久化)", last)
	}
}
