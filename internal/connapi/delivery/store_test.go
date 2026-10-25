// SPDX-License-Identifier: MIT
package delivery

import (
	"context"
	"testing"
	"time"
)

// fakeClock 可推进的测试时钟。
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }
func (c *fakeClock) Back(d time.Duration)    { c.t = c.t.Add(-d) }

func newMem(t *testing.T, retention time.Duration) (*MemStore, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	return NewMemStore(Options{Retention: retention, Now: clk.Now}), clk
}

func appendFrames(t *testing.T, s *MemStore, userID int64, payloads ...[]byte) []Entry {
	t.Helper()
	frames := make([]AppendFrame, 0, len(payloads))
	for _, p := range payloads {
		frames = append(frames, AppendFrame{MsgType: MsgTypeEVT, Flags: 0, Payload: p})
	}
	entries, err := s.Append(context.Background(), userID, frames)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return entries
}

func TestMemAppendSequences(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	e1 := appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"))
	if len(e1) != 3 || e1[0].Seq != 1 || e1[2].Seq != 3 {
		t.Fatalf("user1 seqs = %+v, want 1..3", e1)
	}
	e2 := appendFrames(t, s, 2, []byte("x"))
	if e2[0].Seq != 1 {
		t.Fatalf("user2 first seq = %d, want 1 (per-user allocator)", e2[0].Seq)
	}
	e3 := appendFrames(t, s, 1, []byte("d"))
	if e3[0].Seq != 4 {
		t.Fatalf("user1 next seq = %d, want 4", e3[0].Seq)
	}
	last, _ := s.LastSeq(context.Background(), 1)
	if last != 4 {
		t.Fatalf("LastSeq = %d, want 4", last)
	}
}

func TestMemAckGreatestIdempotent(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	// 乱序 + 重复 ack：位点必须单调，等于最大 ack。
	for _, seq := range []uint64{5, 3, 7, 7, 1, 6} {
		cur, err := s.Ack(ctx, 1, "ios-1", seq)
		if err != nil {
			t.Fatalf("Ack(%d): %v", seq, err)
		}
		if cur.AckedSeq < seq {
			t.Fatalf("AckedSeq(%d) = %d, must not go backwards", seq, cur.AckedSeq)
		}
	}
	cur, ok, err := s.Cursor(ctx, "ios-1")
	if err != nil || !ok {
		t.Fatalf("Cursor: ok=%v err=%v", ok, err)
	}
	if cur.AckedSeq != 7 {
		t.Fatalf("acked_seq = %d, want 7 (GREATEST)", cur.AckedSeq)
	}
	if cur.ConnEpoch != 0 {
		t.Fatalf("conn_epoch = %d, want 0", cur.ConnEpoch)
	}
}

func TestMemEpochBumpAndResumeReject(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	if _, err := s.EnsureDevice(ctx, 1, "ios-1"); err != nil {
		t.Fatalf("EnsureDevice: %v", err)
	}
	epoch, err := s.BumpEpoch(ctx, 1, "ios-1")
	if err != nil || epoch != 1 {
		t.Fatalf("BumpEpoch = (%d,%v), want (1,nil)", epoch, err)
	}
	appendFrames(t, s, 1, []byte("a"))
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, DeviceID: "ios-1", ConnEpoch: 0})
	if err == nil {
		t.Fatal("PlanReplay with stale epoch should fail")
	}
	if plan.OK || plan.Reason != "epoch_mismatch" {
		t.Fatalf("plan = %+v, want OK=false reason=epoch_mismatch", plan)
	}
	plan, err = s.PlanReplay(ctx, ReplayRequest{UserID: 1, DeviceID: "ios-1", ConnEpoch: 1})
	if err != nil || !plan.OK {
		t.Fatalf("PlanReplay epoch=1: plan=%+v err=%v", plan, err)
	}
	if plan.Epoch != 1 {
		t.Fatalf("plan.Epoch = %d, want 1", plan.Epoch)
	}
}

func TestMemPlanReplayNoTruncation(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"))
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, Limits: DefaultReplayLimits()})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if !plan.OK || plan.Truncated {
		t.Fatalf("plan = %+v, want OK && !Truncated", plan)
	}
	if plan.From != 1 || plan.To != 3 || plan.Replayed != 3 {
		t.Fatalf("plan = %+v, want from=1 to=3 replayed=3", plan)
	}
	if plan.OldestAvailable != 1 || plan.DroppedBefore != 0 {
		t.Fatalf("plan = %+v, want oldest=1 droppedBefore=0", plan)
	}
}

func TestMemPlanReplayFrameCap(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e"))
	limits := DefaultReplayLimits()
	limits.MaxFrames = 2
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, Limits: limits})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if !plan.Truncated || plan.Replayed != 2 || plan.To != 2 {
		t.Fatalf("plan = %+v, want truncated replayed=2 to=2", plan)
	}
	if plan.DroppedBefore != 0 {
		t.Fatalf("DroppedBefore = %d, want 0 (no permanent gap, only truncated tail)", plan.DroppedBefore)
	}
}

func TestMemPlanReplayByteCap(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	payload := make([]byte, 100)
	appendFrames(t, s, 1, payload, payload, payload, payload, payload)
	limits := DefaultReplayLimits()
	limits.MaxBytes = 250
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, Limits: limits})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if !plan.Truncated || plan.Replayed != 2 || plan.To != 2 {
		t.Fatalf("plan = %+v, want truncated replayed=2 to=2 (2×100B ≤ 250B)", plan)
	}
}

func TestMemPlanReplayAgeTruncation(t *testing.T) {
	s, clk := newMem(t, time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("old1"), []byte("old2"), []byte("old3"))
	clk.Advance(DefaultMaxReplayAge + time.Hour) // 超过 7 天可补发时效
	appendFrames(t, s, 1, []byte("new4"), []byte("new5"))
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, Limits: DefaultReplayLimits()})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if !plan.Truncated {
		t.Fatalf("plan = %+v, want truncated (age window)", plan)
	}
	if plan.OldestAvailable != 4 || plan.DroppedBefore != 3 {
		t.Fatalf("plan = %+v, want oldest=4 droppedBefore=3", plan)
	}
	if plan.From != 4 || plan.To != 5 || plan.Replayed != 2 {
		t.Fatalf("plan = %+v, want from=4 to=5 replayed=2", plan)
	}
}

func TestMemPlanReplayNoLogsButAllocated(t *testing.T) {
	s, clk := newMem(t, time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"))
	clk.Advance(DefaultMaxReplayAge + time.Hour) // 全部过期
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, Limits: DefaultReplayLimits()})
	if err != nil {
		t.Fatalf("PlanReplay: %v", err)
	}
	if !plan.Truncated || plan.Replayed != 0 || plan.DroppedBefore != 3 {
		t.Fatalf("plan = %+v, want truncated replayed=0 droppedBefore=3", plan)
	}
}

func TestMemPlanReplayNewDeviceUsesClientLastAck(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"), []byte("d"))
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: 1, DeviceID: "android-new", FromSeq: 2})
	if err != nil || !plan.OK {
		t.Fatalf("PlanReplay: plan=%+v err=%v", plan, err)
	}
	if plan.From != 3 || plan.To != 4 || plan.Replayed != 2 {
		t.Fatalf("plan = %+v, want from=3 to=4 replayed=2", plan)
	}
	cur, ok, _ := s.Cursor(ctx, "android-new")
	if !ok || cur.AckedSeq != 2 {
		t.Fatalf("new device cursor = %+v ok=%v, want acked=2", cur, ok)
	}
}

func TestMemPage(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e"))
	page, err := s.Page(ctx, 1, 0, 4, 10, 1024)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page) != 4 || page[0].Seq != 1 || page[3].Seq != 4 {
		t.Fatalf("page = %+v, want seq 1..4", page)
	}
	page, _ = s.Page(ctx, 1, 2, 0, 2, 1024)
	if len(page) != 2 || page[0].Seq != 3 || page[1].Seq != 4 {
		t.Fatalf("page = %+v, want seq 3..4", page)
	}
}

func TestMemTrimOnlyToMinCursor(t *testing.T) {
	s, clk := newMem(t, time.Hour)
	ctx := context.Background()
	entries := appendFrames(t, s, 1, []byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e"))
	if len(entries) != 5 {
		t.Fatalf("append = %d entries", len(entries))
	}
	if _, err := s.Ack(ctx, 1, "ios-a", 5); err != nil {
		t.Fatalf("Ack ios-a: %v", err)
	}
	if _, err := s.Ack(ctx, 1, "android-b", 2); err != nil {
		t.Fatalf("Ack android-b: %v", err)
	}
	clk.Advance(2 * time.Hour) // 超过 1h 保留期
	deleted, err := s.Trim(ctx, 1, 0)
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("Trim deleted = %d, want 2 (MIN(5,2)=2)", deleted)
	}
	page, _ := s.Page(ctx, 1, 0, 0, 10, 1<<20)
	if len(page) != 3 || page[0].Seq != 3 {
		t.Fatalf("remaining = %+v, want seq 3..5", page)
	}
	// 再来一台新设备、位点 0 → MIN 变 0，不得继续裁剪。
	if _, err := s.Ack(ctx, 1, "new-device", 0); err != nil {
		t.Fatalf("Ack new-device: %v", err)
	}
	if deleted, _ = s.Trim(ctx, 1, 0); deleted != 0 {
		t.Fatalf("Trim with MIN=0 deleted %d, want 0", deleted)
	}
}

func TestMemTrimNoCursorNoDelete(t *testing.T) {
	s, clk := newMem(t, time.Hour)
	appendFrames(t, s, 1, []byte("a"), []byte("b"))
	clk.Advance(2 * time.Hour)
	deleted, err := s.Trim(context.Background(), 1, 0)
	if err != nil || deleted != 0 {
		t.Fatalf("Trim without cursor = (%d,%v), want (0,nil)", deleted, err)
	}
}

func TestMemTrimRespectsRetention(t *testing.T) {
	s, _ := newMem(t, 24*time.Hour)
	ctx := context.Background()
	appendFrames(t, s, 1, []byte("a"))
	if _, err := s.Ack(ctx, 1, "ios-a", 1); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	// 刚写入的日志未到保留期 → 不裁。
	if deleted, err := s.Trim(ctx, 1, 0); err != nil || deleted != 0 {
		t.Fatalf("Trim before retention = (%d,%v), want (0,nil)", deleted, err)
	}
}

func TestMemInvalidArgs(t *testing.T) {
	s, _ := newMem(t, time.Hour)
	ctx := context.Background()
	if _, err := s.Append(ctx, 0, []AppendFrame{{Payload: []byte("x")}}); err == nil {
		t.Fatal("Append userID=0 should fail")
	}
	if _, err := s.Ack(ctx, 1, "", 1); err == nil {
		t.Fatal("Ack deviceID='' should fail")
	}
	if _, err := s.PlanReplay(ctx, ReplayRequest{UserID: 0}); err == nil {
		t.Fatal("PlanReplay userID=0 should fail")
	}
}
