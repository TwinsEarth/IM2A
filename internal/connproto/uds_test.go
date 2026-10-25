package connproto

// uds_test.go：B2-b 跨进程契约的编解码测试（内核侧与网关侧共用同一实现，必须锁死）。

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestUDSEnvelopeRoundTrip(t *testing.T) {
	body := []byte(`{"hello":1}`)
	frame, err := EncodeUDS(nil, KindDeliverAck, 0, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frame) != UDSHeaderLen+len(body) {
		t.Fatalf("frame len=%d, want %d", len(frame), UDSHeaderLen+len(body))
	}
	n, kind, flags, err := DecodeUDSHeader(frame[:UDSHeaderLen])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if n != 2+len(body) || kind != KindDeliverAck || flags != 0 {
		t.Fatalf("header mismatch n=%d kind=%v flags=%d", n, kind, flags)
	}
	if !bytes.Equal(frame[UDSHeaderLen:], body) {
		t.Fatal("body mismatch")
	}
}

func TestUDSEnvelopeLimits(t *testing.T) {
	if _, err := EncodeUDS(nil, KindDeliverReq, 0, make([]byte, UDSMaxBody+1)); err != ErrUDSTooLarge {
		t.Fatalf("oversize: err=%v", err)
	}
	// len<2 非法
	var h [UDSHeaderLen]byte
	binary.BigEndian.PutUint32(h[0:4], 1)
	if _, _, _, err := DecodeUDSHeader(h[:]); err != ErrUDSBadLen {
		t.Fatalf("len<2: err=%v", err)
	}
	// 超 1MiB 非法
	binary.BigEndian.PutUint32(h[0:4], UDSMaxBody+3)
	if _, _, _, err := DecodeUDSHeader(h[:]); err != ErrUDSTooLarge {
		t.Fatalf("too large: err=%v", err)
	}
}

func TestDeliverReqBinaryRoundTrip(t *testing.T) {
	req := &DeliverReq{UserID: 42, BatchID: 7, Frames: []Frame{
		{Type: TypeMsg, Flags: FlagCritical, Seq: 1, Payload: []byte("a")},
		{Type: TypeEvt, Seq: 2, Topic: "agent:42", Payload: bytes.Repeat([]byte("x"), 300)},
	}}
	body, err := EncodeDeliverReq(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeDeliverReq(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != 42 || got.BatchID != 7 || len(got.Frames) != 2 {
		t.Fatalf("header mismatch: %+v", got)
	}
	if got.Frames[0].Seq != 1 || !bytes.Equal(got.Frames[0].Payload, []byte("a")) {
		t.Fatalf("frame0 mismatch: %+v", got.Frames[0])
	}
	if got.Frames[1].Topic != "agent:42" || len(got.Frames[1].Payload) != 300 {
		t.Fatalf("frame1 mismatch: %+v", got.Frames[1])
	}
}

func TestDeliverReqMalformed(t *testing.T) {
	good, _ := EncodeDeliverReq(&DeliverReq{UserID: 1, BatchID: 1, Frames: []Frame{{Type: TypeMsg, Seq: 1, Payload: []byte("x")}}})
	cases := []struct {
		name string
		mut  func([]byte) []byte
	}{
		{"截断批头", func(b []byte) []byte { return b[:10] }},
		{"帧长度越界", func(b []byte) []byte { c := cp(b); binary.BigEndian.PutUint32(c[18:22], 0xffff); return c }},
		{"空 body", func(b []byte) []byte { return nil }},
	}
	for _, tc := range cases {
		if _, err := DecodeDeliverReq(tc.mut(good)); err == nil {
			t.Errorf("%s: 期望报错", tc.name)
		}
	}
}

func TestSplitDeliverReq(t *testing.T) {
	// 每帧 ~1KB，构造 3000 帧：4096 帧上限不触发，但 1MiB body 会触发切批。
	frames := make([]Frame, 0, 3000)
	payload := bytes.Repeat([]byte("y"), 1000)
	for i := 0; i < 3000; i++ {
		frames = append(frames, Frame{Type: TypeMsg, Seq: uint64(i + 1), Payload: payload})
	}
	req := &DeliverReq{UserID: 9, BatchID: 100, Frames: frames}
	chunks, err := SplitDeliverReq(req)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("3000×1KB 应被切成多批, got %d", len(chunks))
	}
	total := 0
	seenBatch := map[uint64]bool{}
	for i, c := range chunks {
		if c.UserID != 9 {
			t.Fatalf("chunk %d userID=%d", i, c.UserID)
		}
		if seenBatch[c.BatchID] {
			t.Fatalf("chunk %d 的 BatchID=%d 重复（必须每片独立）", i, c.BatchID)
		}
		seenBatch[c.BatchID] = true
		body, err := EncodeDeliverReq(c)
		if err != nil {
			t.Fatalf("chunk %d encode: %v", i, err)
		}
		if len(body) > UDSMaxBody {
			t.Fatalf("chunk %d body=%d > UDSMaxBody", i, len(body))
		}
		total += len(c.Frames)
	}
	if total != len(frames) {
		t.Fatalf("切批后总帧数=%d, want %d", total, len(frames))
	}
	// 单帧 64KiB（协议上限）远小于 1MiB body 预算：应原样成为一批，不得被静默拆分/截断。
	big := &DeliverReq{UserID: 1, BatchID: 1, Frames: []Frame{{Type: TypeMsg, Payload: make([]byte, MaxPayload)}}}
	bigChunks, err := SplitDeliverReq(big)
	if err != nil {
		t.Fatalf("单帧 64KiB 不应报错: %v", err)
	}
	if len(bigChunks) != 1 || len(bigChunks[0].Frames) != 1 {
		t.Fatalf("单帧应保持一批: chunks=%d frames=%d", len(bigChunks), len(bigChunks[0].Frames))
	}
}

func TestNegotiateUDS(t *testing.T) {
	ok := NegotiateUDS(UDSHello{ProtoVersion: UDSProtoVersion, Instance: "k1", MaxFrameBytes: 1024, MaxBatchFrames: 100})
	if !ok.Accepted || ok.ProtoVersion != UDSProtoVersion {
		t.Fatalf("同版本应 accepted: %+v", ok)
	}
	// 取更保守的部署口径
	if ok.MaxFrameBytes != 1024 || ok.MaxBatchFrames != 100 {
		t.Fatalf("未取更保守值: %+v", ok)
	}
	bad := NegotiateUDS(UDSHello{ProtoVersion: UDSProtoVersion + 1})
	if bad.Accepted || bad.Reason == "" {
		t.Fatalf("版本不匹配必须拒绝并给 reason: %+v", bad)
	}
}

func TestJSONBodies(t *testing.T) {
	ack := &DeliverAck{BatchID: 1, UserID: 2, TargetConns: 2, Accepted: 5, DroppedSlow: 1, QueuedBytes: 99}
	b, err := EncodeJSONBody(ack)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got DeliverAck
	if err := DecodeJSONBody(b, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BatchID != 1 || got.Accepted != 5 || got.DroppedSlow != 1 {
		t.Fatalf("mismatch: %+v", got)
	}
	// 未知字段必须忽略（前向兼容）
	if err := DecodeJSONBody([]byte(`{"batch_id":7,"future_field":"x"}`), &got); err != nil {
		t.Fatalf("未知字段不应报错: %v", err)
	}
	if got.BatchID != 7 {
		t.Fatalf("batch_id=%d", got.BatchID)
	}
	// Drain 字段
	d := Drain{DeadlineMS: 25000}
	db, _ := EncodeJSONBody(d)
	var dg Drain
	if err := DecodeJSONBody(db, &dg); err != nil || dg.DeadlineMS != 25000 {
		t.Fatalf("drain roundtrip: %+v err=%v", dg, err)
	}
}



// TestC1IdentityFields 锁住 C1 的身份增量：ConnOpen.Token 透传 + ConnOpenAck.UserID 权威身份；
// 并保证老报文（只有 user_id、没有 token/user_id 回显）仍然能解出来（向后兼容）。
func TestC1IdentityFields(t *testing.T) {
	// 新路径：网关只透传 token，user_id 为 0
	op := ConnOpen{ConnID: 7, DeviceID: "ios", Token: "eyJhbGciOi.payload.sig"}
	b, err := EncodeJSONBody(op)
	if err != nil {
		t.Fatalf("encode ConnOpen: %v", err)
	}
	var got ConnOpen
	if err := DecodeJSONBody(b, &got); err != nil {
		t.Fatalf("decode ConnOpen: %v", err)
	}
	if got.Token != op.Token || got.ConnID != 7 || got.DeviceID != "ios" || got.UserID != 0 {
		t.Fatalf("ConnOpen roundtrip mismatch: %+v", got)
	}

	ack := ConnOpenAck{ConnID: 7, OK: true, UserID: 424242, ResumeToken: "rt-1", Epoch: 3}
	ab, err := EncodeJSONBody(ack)
	if err != nil {
		t.Fatalf("encode ConnOpenAck: %v", err)
	}
	var gotAck ConnOpenAck
	if err := DecodeJSONBody(ab, &gotAck); err != nil {
		t.Fatalf("decode ConnOpenAck: %v", err)
	}
	if !gotAck.OK || gotAck.UserID != 424242 || gotAck.Epoch != 3 || gotAck.ConnID != 7 {
		t.Fatalf("ConnOpenAck roundtrip mismatch: %+v", gotAck)
	}

	// 老报文（B2 客户端）：无 token / 无 user_id 回显 —— 必须正常解出且字段为零值。
	var oldOp ConnOpen
	if err := DecodeJSONBody([]byte(`{"conn_id":9,"user_id":5,"device_id":"android"}`), &oldOp); err != nil {
		t.Fatalf("老 ConnOpen 应可解: %v", err)
	}
	if oldOp.UserID != 5 || oldOp.Token != "" {
		t.Fatalf("老 ConnOpen 解码错误: %+v", oldOp)
	}
	var oldAck ConnOpenAck
	if err := DecodeJSONBody([]byte(`{"ok":true,"resume_token":"rt-0","epoch":1}`), &oldAck); err != nil {
		t.Fatalf("老 ConnOpenAck 应可解: %v", err)
	}
	if !oldAck.OK || oldAck.UserID != 0 || oldAck.Epoch != 1 {
		t.Fatalf("老 ConnOpenAck 解码错误: %+v", oldAck)
	}
}

// TestC1PresenceBodies presence 两个新 kind 的往返 + 未知 kind 向后兼容性说明。
func TestC1PresenceBodies(t *testing.T) {
	if KindPresenceLease != 26 || KindPresenceState != 27 {
		t.Fatalf("presence kind 编号被改动: %d/%d（C1 约定 26/27，不能改）", KindPresenceLease, KindPresenceState)
	}
	if KindPresenceLease.String() != "PRESENCE_LEASE" || KindPresenceState.String() != "PRESENCE_STATE" {
		t.Fatalf("presence kind 名称: %s/%s", KindPresenceLease, KindPresenceState)
	}

	lease := PresenceLease{ConnID: 1, UserID: 42, DeviceID: "ios", TTLS: 30, AtMS: 1700000000000, State: "online"}
	b, err := EncodeJSONBody(lease)
	if err != nil {
		t.Fatalf("encode lease: %v", err)
	}
	var gotLease PresenceLease
	if err := DecodeJSONBody(b, &gotLease); err != nil {
		t.Fatalf("decode lease: %v", err)
	}
	if gotLease != lease {
		t.Fatalf("lease roundtrip mismatch: %+v", gotLease)
	}

	st := PresenceState{UserID: 42, State: "away", UpdatedAt: 1700000000000, Devices: 2}
	sb, err := EncodeJSONBody(st)
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}
	var gotState PresenceState
	if err := DecodeJSONBody(sb, &gotState); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if gotState != st {
		t.Fatalf("state roundtrip mismatch: %+v", gotState)
	}

	// 旧端遇到未知 kind 应在分派层忽略（不是解码错误）：这里锁住"kind 只增不改"的事实。
	if UDSKind(26) != KindPresenceLease || UDSKind(27) != KindPresenceState {
		t.Fatal("kind 编号不稳定")
	}
}