package relay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"human-agent-im/internal/botroute"
	"human-agent-im/internal/model"
	"human-agent-im/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// intentStubModel 区分"意图分类器"调用与"普通对话"调用，分别返回可控 JSON / 回答。
type intentStubModel struct {
	classifyJSON string
	answer       string
	err          error
}

func (m *intentStubModel) Name() string { return "intent-stub" }

func (m *intentStubModel) Chat(_ context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "意图分类器") {
			return &model.ChatResponse{Content: m.classifyJSON, RoutedTo: "primary"}, nil
		}
	}
	return &model.ChatResponse{Content: m.answer, RoutedTo: "primary"}, nil
}

// 纯逻辑：JSON 提取容错（代码块 / 前后缀 / 裸 JSON）。
func TestExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		`{"intent":"chat"}`:                         `{"intent":"chat"}`,
		"```json\n{\"intent\":\"nego_reply\"}\n```": `{"intent":"nego_reply"}`,
		`好的，结果是 {"intent":"relay","target":"小红"} 哦`: `{"intent":"relay","target":"小红"}`,
	}
	for in, want := range cases {
		if got := extractJSONObject(in); got != want {
			t.Fatalf("extract(%q)=%q want %q", in, got, want)
		}
	}
}

// 纯逻辑：分类器解析结果，含安全降级。
func TestClassifyIntent(t *testing.T) {
	ctx := context.Background()
	from := "u1@im.wechat"

	// 1) 正常聊天（带代码块包裹）→ chat
	s := &Service{mdl: &intentStubModel{classifyJSON: "```json\n" + `{"intent":"chat","confidence":0.95}` + "\n```"}}
	if got := s.classifyIntent(ctx, from, "你能干什么？有什么功能？", nil, nil); intentKind(got.Intent) != intentChat {
		t.Fatalf("want chat, got %q", got.Intent)
	}

	// 2) 明确协商答复 → nego_reply
	s = &Service{mdl: &intentStubModel{classifyJSON: `{"intent":"nego_reply","confidence":0.9}`}}
	if got := s.classifyIntent(ctx, from, "两点可以，我带3个人", nil, &pendingNego{other: "p@im.wechat"}); intentKind(got.Intent) != intentNegoReply {
		t.Fatalf("want nego_reply, got %q", got.Intent)
	}

	// 3) 垃圾输出 → 安全降级 chat
	s = &Service{mdl: &intentStubModel{classifyJSON: "抱歉，我无法理解"}}
	if got := s.classifyIntent(ctx, from, "随便说点", nil, nil); intentKind(got.Intent) != intentChat {
		t.Fatalf("garbage should fall back to chat, got %q", got.Intent)
	}

	// 4) 模型报错 → 安全降级 chat（绝不误转发）
	s = &Service{mdl: &intentStubModel{err: errors.New("model unavailable")}}
	if got := s.classifyIntent(ctx, from, "告诉小红今晚7点", nil, nil); intentKind(got.Intent) != intentChat {
		t.Fatalf("model error should fall back to chat, got %q", got.Intent)
	}

	// 5) 非法 intent 值 → 归一为 chat
	s = &Service{mdl: &intentStubModel{classifyJSON: `{"intent":"hacked"}`}}
	if got := s.classifyIntent(ctx, from, "x", nil, nil); intentKind(got.Intent) != intentChat {
		t.Fatalf("invalid intent must normalize to chat, got %q", got.Intent)
	}
}

// 真实 PG：存在未完成协商时，普通闲聊不得被吸走（不转发、不推进、不落 nego_reply）；
// 只有 LLM 判定 nego_reply 才推进协商并落库。
func TestRelayIntentGateReal(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	dir := t.TempDir()
	suf := fmt.Sprintf("vig_%d", time.Now().UnixNano())
	accountID := suf + "-im-bot"
	botWxid := suf + "_bot@im.wechat"
	ownerWxid := suf + "_owner@im.wechat"
	peerWxid := suf + "_peer@im.wechat"
	sessID := suf + "_sess"
	if err := os.WriteFile(filepath.Join(dir, accountID+".json"),
		[]byte(`{"userId":"`+botWxid+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM negotiation_sessions WHERE id=$1`, sessID)
		_, _ = d.Exec(`DELETE FROM wechat_relay_messages WHERE from_wxid LIKE $1 OR bot_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})

	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,940001),($2,940002) ON CONFLICT DO NOTHING`,
		ownerWxid, peerWxid); err != nil {
		t.Fatal(err)
	}
	// 造一个"最后一轮由对方(peer)发来、正等 owner 回复"的协商单。
	if _, err := d.Exec(`INSERT INTO negotiation_sessions(id,state,from_agent_id,to_agent_id,round,max_rounds)
		VALUES($1,'INPUT_REQUIRED',$2,$3,1,5)`, sessID, peerWxid, ownerWxid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO negotiation_turns(session_id,seq,from_agent_id,to_agent_id,intent,payload)
		VALUES($1,1,$2,$3,'REQUEST_INFO',jsonb_build_object('text','你那边几点能到？'))`,
		sessID, peerWxid, ownerWxid); err != nil {
		t.Fatal(err)
	}

	negoStore := store.NewPostgresStore(d, 10).Negotiation

	// A) 分类器判定 chat：闲聊不得被旧协商吸走。
	stub := &intentStubModel{
		classifyJSON: `{"intent":"chat","confidence":0.97}`,
		answer:       "我能帮你约时间、查信息、写东西，也能帮你传话给朋友。",
	}
	svc := New(d, NewAccountResolver(dir), stub, botroute.New(d)).
		WithNegotiation(NewWechatBridge(negoStore))
	res, err := svc.Relay(context.Background(), accountID, ownerWxid, "你能干什么？有什么功能？", "ig_chat_"+suf)
	if err != nil {
		t.Fatalf("relay chat: %v", err)
	}
	if res.Route != RouteSelfChat {
		t.Fatalf("闲聊应走 self_chat，实际 route=%q", res.Route)
	}
	var turns int
	if err := d.QueryRow(`SELECT count(*) FROM negotiation_turns WHERE session_id=$1`, sessID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 1 {
		t.Fatalf("闲聊不应推进协商，turns=%d 应为 1", turns)
	}
	var negoRows int
	if err := d.QueryRow(`SELECT count(*) FROM wechat_relay_messages WHERE idem_key=$1 AND route='nego_reply'`,
		"ig_chat_"+suf).Scan(&negoRows); err != nil {
		t.Fatal(err)
	}
	if negoRows != 0 {
		t.Fatalf("闲聊不应产生 nego_reply 审计行，实际 %d 条", negoRows)
	}

	// B) 分类器判定 nego_reply：才推进协商并落库。
	stub.classifyJSON = `{"intent":"nego_reply","confidence":0.92}`
	res2, err := svc.Relay(context.Background(), accountID, ownerWxid, "两点可以，我带3个人", "ig_nego_"+suf)
	if err != nil {
		t.Fatalf("relay nego: %v", err)
	}
	if res2.Route != RouteNegoReply || res2.NegoSessionID != sessID {
		t.Fatalf("应走 nego_reply 并挂到 %s，实际 route=%q sid=%q", sessID, res2.Route, res2.NegoSessionID)
	}
	if err := d.QueryRow(`SELECT count(*) FROM negotiation_turns WHERE session_id=$1`, sessID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("协商答复应新增一轮，turns=%d 应为 2", turns)
	}
	if err := d.QueryRow(`SELECT count(*) FROM wechat_relay_messages WHERE idem_key=$1 AND route='nego_reply'`,
		"ig_nego_"+suf).Scan(&negoRows); err != nil {
		t.Fatal(err)
	}
	if negoRows != 1 {
		t.Fatalf("协商答复应落 1 条 nego_reply 行，实际 %d 条", negoRows)
	}
}
