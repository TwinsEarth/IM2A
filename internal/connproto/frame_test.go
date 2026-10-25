package connproto

// frame_test.go：帧编解码的契约测试。
// 必须覆盖 6 类畸形输入（截断头/截断 payload/坏 magic/未知 version/非法 flags/topic 超长）
// 以及空 payload、超长 payload、多帧粘包、并发编解码。

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []Frame{
		{Type: TypeMsg, Flags: FlagCritical, Seq: 1, Ack: 0, Topic: "", Payload: []byte("hello")},
		{Type: TypeEvt, Flags: FlagFinal, Seq: 1 << 40, Ack: 7, Topic: "agent:42", Payload: []byte(`{"k":"v"}`)},
		{Type: TypeKick, Flags: FlagCritical, Seq: 9, Topic: "", Payload: []byte("kicked")},
		{Type: TypePing, Seq: 3},   // 空 payload
		{Type: TypeMsg, Seq: 4, Payload: make([]byte, MaxPayload)}, // 满 payload
	}
	for i, want := range cases {
		buf, err := Encode(nil, &want)
		if err != nil {
			t.Fatalf("case %d: encode: %v", i, err)
		}
		got, n, err := Decode(buf)
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		if n != len(buf) {
			t.Fatalf("case %d: consumed %d, want %d", i, n, len(buf))
		}
		if got.Type != want.Type || got.Seq != want.Seq || got.Ack != want.Ack || got.Topic != want.Topic {
			t.Fatalf("case %d: header mismatch got=%+v want=%+v", i, got.Type, want.Type)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("case %d: payload mismatch", i)
		}
		// has_topic 由编码器自动置位
		if want.Topic != "" && got.Flags&FlagHasTopic == 0 {
			t.Fatalf("case %d: has_topic not set", i)
		}
	}
}

func TestFrameMalformed(t *testing.T) {
	good, _ := Encode(nil, &Frame{Type: TypeMsg, Seq: 5, Payload: []byte("abcd")})

	tests := []struct {
		name string
		mut  func(b []byte) []byte
		want error
	}{
		{"截断头", func(b []byte) []byte { return b[:HeaderLen-1] }, ErrTruncated},
		{"截断 payload", func(b []byte) []byte { return b[:len(b)-1] }, ErrTruncated},
		{"坏 magic", func(b []byte) []byte { c := cp(b); c[0] = 'X'; return c }, ErrBadMagic},
		{"未知 version", func(b []byte) []byte { c := cp(b); c[2] = 0x09; return c }, ErrBadVersion},
		{"非法 flags", func(b []byte) []byte { c := cp(b); c[3] = 0x80; return c }, ErrBadFlags},
		{"topic 超长", func(b []byte) []byte { c := cp(b); c[5] = MaxTopicLen + 1; return c }, ErrTopicTooLong},
	}
	for _, tc := range tests {
		_, _, err := Decode(tc.mut(good))
		if err != tc.want {
			t.Errorf("%s: err=%v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestFrameEncodeRejectsOversize(t *testing.T) {
	if _, err := Encode(nil, &Frame{Type: TypeMsg, Payload: make([]byte, MaxPayload+1)}); err != ErrPayloadTooLarge {
		t.Fatalf("payload 超限: err=%v, want ErrPayloadTooLarge", err)
	}
	long := make([]byte, MaxTopicLen+1)
	if _, err := Encode(nil, &Frame{Type: TypeMsg, Topic: string(long)}); err != ErrTopicTooLong {
		t.Fatalf("topic 超限: err=%v", err)
	}
	if _, err := Encode(nil, &Frame{Type: TypeMsg, Flags: 0x80}); err != ErrBadFlags {
		t.Fatalf("非法 flags: err=%v", err)
	}
	if _, err := Encode(nil, nil); err != ErrNilFrame {
		t.Fatalf("nil frame: err=%v", err)
	}
}

// TestFrameStream 验证粘包/半包：一次传入多帧、逐帧消费。
func TestFrameStream(t *testing.T) {
	var buf []byte
	for i := 1; i <= 5; i++ {
		var err error
		buf, err = Encode(buf, &Frame{Type: TypeEvt, Seq: uint64(i), Payload: []byte{byte(i)}})
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
	}
	var got []uint64
	for len(buf) > 0 {
		f, n, err := Decode(buf)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		got = append(got, f.Seq)
		buf = buf[n:]
	}
	if len(got) != 5 {
		t.Fatalf("解码出 %d 帧, want 5", len(got))
	}
}

// TestFrameConcurrent 并发编解码（-race 下验证无共享状态）。
func TestFrameConcurrent(t *testing.T) {
	const workers = 100
	const perWorker = 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := []byte("payload")
			for i := 0; i < perWorker; i++ {
				f := Frame{Type: TypeMsg, Flags: FlagCritical, Seq: uint64(w*perWorker + i + 1), Payload: payload}
				b, err := Encode(nil, &f)
				if err != nil {
					t.Errorf("encode: %v", err)
					return
				}
				got, n, err := Decode(b)
				if err != nil {
					t.Errorf("decode: %v", err)
					return
				}
				if n != len(b) || got.Seq != f.Seq {
					t.Errorf("roundtrip mismatch seq=%d got=%d", f.Seq, got.Seq)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestValidType(t *testing.T) {
	for _, ok := range []Type{TypeMsg, TypeEvt, TypePresence, TypeKick, TypePing, TypePong, TypeErr} {
		if !ValidType(ok) {
			t.Errorf("%v 应合法", ok)
		}
	}
	for _, bad := range []Type{0, 8, 200} {
		if ValidType(bad) {
			t.Errorf("%v 应非法", bad)
		}
	}
}

func TestPayloadLenField(t *testing.T) {
	// 显式验证 offset 6..8 是 payload_len（网络序）——这是实现期修订点，必须锁死。
	b, err := Encode(nil, &Frame{Type: TypeMsg, Payload: make([]byte, 300)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := binary.BigEndian.Uint16(b[6:8]); got != 300 {
		t.Fatalf("offset6..8 payload_len = %d, want 300", got)
	}
	if b[0] != Magic0 || b[1] != Magic1 || b[2] != VersionV1 {
		t.Fatalf("magic/version 不对: %x %x %x", b[0], b[1], b[2])
	}
}

// ---- 控制帧 ----

func TestControlRoundTrip(t *testing.T) {
	c := &ControlFrame{T: CtlHello, V: 1, ProtoMin: "1.0", ProtoMax: "1.2",
		Features: []string{"resume", "topic"}, DeviceID: "dev-1", LastAckSeq: 42}
	b, err := EncodeControl(c)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeControl(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.T != CtlHello || got.LastAckSeq != 42 || got.DeviceID != "dev-1" || len(got.Features) != 2 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestControlMalformed(t *testing.T) {
	if _, err := DecodeControl([]byte(`{not json`)); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := DecodeControl([]byte(`{"v":1}`)); err != ErrEmptyControl {
		t.Fatalf("缺 t: err=%v, want ErrEmptyControl", err)
	}
	big := append([]byte(`{"t":"hello","pad":"`), bytes.Repeat([]byte("x"), MaxControlLen)...)
	big = append(big, []byte(`"}`)...)
	if _, err := DecodeControl(big); err != ErrControlTooLarge {
		t.Fatalf("超长控制帧: err=%v, want ErrControlTooLarge", err)
	}
	// 未知 t 允许（前向兼容）
	if c, err := DecodeControl([]byte(`{"t":"future_frame"}`)); err != nil || c.T != "future_frame" {
		t.Fatalf("未知 t 应允许: c=%+v err=%v", c, err)
	}
}

func cp(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
