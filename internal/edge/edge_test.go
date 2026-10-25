package edge

import (
	"bytes"
	"strings"
	"testing"
)

// v1.4.1: 端侧意图分类——自有唤醒词、命令优先于问句、可插拔
func TestV141Intent(t *testing.T) {
	c := NewRuleClassifier()
	if got := c.Classify("你好管家帮我看看日程").Category; got != "wakeup" {
		t.Errorf("自有唤醒词应 wakeup, got %s", got)
	}
	// 关键：命令句带问号不应被误判为闲聊（旧实现 ? 先命中 chat）
	if got := c.Classify("帮我打开空调？").Category; got != "command" {
		t.Errorf("命令句应 command, got %s", got)
	}
	if got := c.Classify("你好吗").Category; got != "chat" {
		t.Errorf("问句应 chat, got %s", got)
	}
	if got := c.Classify("一个完全没有关键词也很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长").Category; got != "unknown" {
		t.Errorf("长无关键词应 unknown, got %s", got)
	}

	// 可插拔：接口替换为自定义实现
	var _ Classifier = (*RuleClassifier)(nil)
	custom := NewCustomClassifier([]string{"芝麻开门"}, nil)
	if got := custom.Classify("芝麻开门开灯").Category; got != "wakeup" {
		t.Errorf("自定义唤醒词应 wakeup, got %s", got)
	}
}

// v1.4.2: 敏感正则双通道——纯号码（不含"手机号"字样）也必须拦截
func TestV142Sensitive(t *testing.T) {
	f := NewSensitiveFilter()

	// 纯手机号：没有"手机号"这个词，只有号码本身
	blocked, redacted, hits := f.Filter("联系我 13812345678 即可")
	if !blocked {
		t.Error("含手机号号码应拦截")
	}
	if bytes.Contains([]byte(redacted), []byte("13812345678")) {
		t.Errorf("手机号号码不应出现在脱敏结果: %s", redacted)
	}
	if len(hits) == 0 {
		t.Error("应记录命中规则")
	}

	// 身份证
	if b, _, _ := f.Filter("我的身份证 110101199003071234 是"); !b {
		t.Error("含身份证应拦截")
	}
	// 邮箱
	if b, r, _ := f.Filter("发邮件 to someone@example.com please"); !b || bytes.Contains([]byte(r), []byte("someone@example.com")) {
		t.Errorf("邮箱应拦截且脱敏: blocked=%v redacted=%s", b, r)
	}
	// 正常文本不拦截
	if b, _, _ := f.Filter("明天下午三点开会讨论方案"); b {
		t.Error("正常文本不应拦截")
	}
}

// v1.4.4: EgressGuard 真实字节扫描（不是信任调用方传 bool）
func TestV144EgressGuard(t *testing.T) {
	g := NewEgressGuard([]string{"api.im2a.chat"})

	// 文本 JSON → allow
	if ok, reason := g.Allow("https://api.im2a.chat/v1/intent", []byte(`{"category":"command","text":"打开空调"}`)); !ok {
		t.Errorf("合法文本应允许, reason=%s", reason)
	}
	// 非白名单 host → reject
	if ok, _ := g.Allow("https://evil.com/steal", []byte(`{"text":"x"}`)); ok {
		t.Error("非白名单 host 应拒绝")
	}
	// WAV magic（RIFF....WAVE）→ reject，哪怕调用方没说有音频
	wav := append([]byte("RIFF"), []byte("\x24\x08\x00\x00WAVEfmt ")...)
	if ok, reason := g.Allow("https://api.im2a.chat/v1/intent", wav); ok {
		t.Errorf("WAV 字节应拒绝, reason=%s", reason)
	}
	// MP3 magic ID3 → reject
	if ok, _ := g.Allow("https://api.im2a.chat/v1/intent", []byte("ID3\x03\x00\x00\x00\x00")); ok {
		t.Error("MP3 字节应拒绝")
	}
	// JSON 含 audio 字段 → reject
	if ok, reason := g.Allow("https://api.im2a.chat/v1/intent", []byte(`{"audio":"base64=="}`)); ok {
		t.Errorf("含 audio 字段应拒绝, reason=%s", reason)
	}
	// 超大 body → reject
	if ok, _ := g.Allow("https://api.im2a.chat/v1/intent", make([]byte, 128*1024)); ok {
		t.Error("超大 body 应拒绝")
	}

	total, blocked, violations := g.Report()
	if total != 6 {
		t.Errorf("总决策应 6, got %d", total)
	}
	if blocked != 5 {
		t.Errorf("拦截应 5（非白名单+wav+mp3+audio字段+超大）, got %d", blocked)
		t.Logf("violations: %+v", violations)
	}
}

// v1.4.0: 端侧 pipeline 串联 + 出端裁决
func TestV140PipelineEgress(t *testing.T) {
	p := NewPipeline([]string{"api.im2a.chat"}, "https://api.im2a.chat/v1/intent")

	// 敏感文本 → ShouldUpload=false，Egress 直接拒
	r := p.Process("你好管家我的手机号13812345678")
	if !r.Blocked {
		t.Error("手机号应触发拦截")
	}
	if r.ShouldUpload {
		t.Error("敏感文本不应允许出端")
	}
	if ok, _, _ := p.Egress(r); ok {
		t.Error("敏感文本的出端裁决必须拒绝")
	}

	// 正常命令 → 允许出端，且出端 body 是纯文本 JSON（guard 扫描通过）
	r2 := p.Process("帮我打开空调")
	if !r2.ShouldUpload {
		t.Error("正常命令应允许出端")
	}
	ok, reason, body := p.Egress(r2)
	if !ok {
		t.Errorf("正常命令出端应允许, reason=%s", reason)
	}
	if bytes.Contains(body, []byte("RIFF")) || bytes.Contains(body, []byte("ID3")) {
		t.Error("出端 body 不应含音频 magic")
	}
}

// v1.4.3: BLE 二进制帧编解码 + CRC
func TestV143BLEFrame(t *testing.T) {
	// 意图文本帧往返
	f := Frame{Cmd: CmdIntent, Payload: []byte("打开空调")}
	raw, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != SOF {
		t.Error("SOF 错误")
	}
	dec, err := Decode(raw)
	if err != nil {
		t.Fatalf("往返解码失败: %v", err)
	}
	if dec.Cmd != CmdIntent || string(dec.Payload) != "打开空调" {
		t.Errorf("往返内容不一致: %+v", dec)
	}

	// 篡改一个字节 → CRC 失败
	bad := append([]byte{}, raw...)
	bad[4] ^= 0xFF
	if _, err := Decode(bad); err == nil {
		t.Error("篡改字节应 CRC 报错")
	}
	// 坏 SOF
	if _, err := Decode(append([]byte{0x00}, raw[1:]...)); err == nil {
		t.Error("坏 SOF 应报错")
	}
	// 超长 payload
	if _, err := (Frame{Cmd: CmdIntent, Payload: make([]byte, MaxPayload+1)}).Encode(); err == nil {
		t.Error("超长 payload 应报错")
	}

	// 设备电量帧
	g := NewBLEGateway()
	dev, _ := g.Connect("eb-1", DeviceEarbud)
	_ = g.OnFrame("eb-1", Frame{Cmd: CmdBattery, Payload: []byte{42}})
	if dev.Battery != 42 {
		t.Errorf("电量应更新为 42, got %d", dev.Battery)
	}
}

// 出端白名单为空必须 fail-closed（合规默认值不得放行任何 host）。
// 旧实现 len==0 视为"不限制"，等于"忘了配置=全部放行"，本测试锁死该回归。
func TestEgressGuardEmptyAllowlistFailsClosed(t *testing.T) {
	g := NewEgressGuard(nil)
	ok, reason := g.Allow("https://anything.example/v1/intent", []byte(`{"text":"hi"}`))
	if ok {
		t.Fatalf("空白名单必须拒绝（fail-closed），reason=%q", reason)
	}
	if !strings.Contains(reason, "fail-closed") {
		t.Fatalf("空白名单的拒绝原因应包含 fail-closed，got %q", reason)
	}

	// 非空白名单仍按 host 判定：白名单内放行、白名单外拒绝。
	g2 := NewEgressGuard([]string{"api.example.com"})
	if ok, reason := g2.Allow("https://api.example.com/v1/intent", []byte(`{"text":"hi"}`)); !ok {
		t.Fatalf("白名单内 host 应放行，reason=%q", reason)
	}
	if ok, _ := g2.Allow("https://evil.example/v1/intent", []byte(`{"text":"hi"}`)); ok {
		t.Fatal("白名单外 host 必须拒绝")
	}
}
