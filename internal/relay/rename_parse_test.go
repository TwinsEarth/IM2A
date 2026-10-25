package relay

import "testing"

func TestParseRenameGreetingsAndAgent(t *testing.T) {
	cases := []struct {
		in       string
		wantKind string
		wantName string
		wantOK   bool
	}{
		{"你叫阿福", "agent", "阿福", true},
		{"你好，我叫小房", "user", "小房", true},
		{"您好，我叫小房。", "user", "小房", true},
		{"我的 Agent 叫小佑佑", "agent", "小佑佑", true},
		{"我的agent叫小佑佑", "agent", "小佑佑", true},
		{"我的助理叫小佑佑", "agent", "小佑佑", true},
		{"以后叫我阿明", "user", "阿明", true},
		{"你叫什么名字", "", "", false},     // 问句
		{"我是学生", "", "", false},       // 正常话
		{"叫我吃饭", "", "", false},       // 正常话
		{"我叫什么", "", "", false},       // 干扰词
		{"两点可以，我带3个人", "", "", false}, // 协商答复
	}
	for _, c := range cases {
		k, n, ok := parseRename(c.in)
		if ok != c.wantOK || k != c.wantKind || n != c.wantName {
			t.Errorf("parseRename(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, k, n, ok, c.wantKind, c.wantName, c.wantOK)
		}
	}
}
