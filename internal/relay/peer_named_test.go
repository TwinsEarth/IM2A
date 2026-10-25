package relay

import "testing"

func TestParsePeerNameLead(t *testing.T) {
	cases := []struct {
		in   string
		rest string
		ok   bool
	}{
		{"告诉小红今晚7点晚饭", "小红今晚7点晚饭", true},
		{"帮我跟小明说下午两点碰头", "小明说下午两点碰头", true},
		{"通知阿明明早开会", "阿明明早开会", true},
		{"发给小红：明天见", "小红：明天见", true},
		{"转告小红，方案改好了", "小红，方案改好了", true},
		{"你好，我叫小房", "", false}, // 自我介绍不是转发
		{"今晚7点晚饭", "", false},  // 无前缀
	}
	for _, c := range cases {
		rest, ok := parsePeerNameLead(c.in)
		if ok != c.ok || (ok && rest != c.rest) {
			t.Errorf("parsePeerNameLead(%q)=(%q,%v) want (%q,%v)", c.in, rest, ok, c.rest, c.ok)
		}
	}
}

func TestExtractPeerNameBody(t *testing.T) {
	names := []string{"阿明", "小红", "小明", "欧阳建国"}
	cases := []struct {
		rest     string
		wantName string
		wantBody string
		ok       bool
	}{
		{"小红今晚7点晚饭", "小红", "今晚7点晚饭", true},
		{"小明说下午两点碰头", "小明", "下午两点碰头", true}, // 姓名后"说"被剥离
		{"小红：明天见", "小红", "明天见", true},
		{"小红，方案改好了", "小红", "方案改好了", true},
		{"阿明方案改好了", "阿明", "方案改好了", true},
		{"欧阳建国明天来公司", "欧阳建国", "明天来公司", true}, // 长名优先
		{"小红", "", "", false},                // 只有称呼无正文
		{"今晚一起吃饭", "", "", false},            // 不以任何已知姓名开头
	}
	for _, c := range cases {
		name, body, ok := extractPeerNameBody(c.rest, names)
		if ok != c.ok || (ok && (name != c.wantName || body != c.wantBody)) {
			t.Errorf("extractPeerNameBody(%q)=(%q,%q,%v) want (%q,%q,%v)",
				c.rest, name, body, ok, c.wantName, c.wantBody, c.ok)
		}
	}
}
