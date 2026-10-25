package orchestration

import (
	"context"
	"os"
	"strings"
	"testing"

	"human-agent-im/internal/memory"
)

// v2.3.23：L4 钩子接入持久三层记忆。
// 验证：
//  1. 同一 wxid 首次任务后，按子 Agent 落情景记忆（episodes），可被记忆命名空间读到；
//  2. 下一次规划时，buildSummary 能取回"最近办过的事"（跨重启记忆，模拟重启用新 Hook）；
//  3. 不同 wxid 记忆隔离（A 的轨迹不进 B 的摘要）；
//  4. 闲聊透传不写记忆。
func hookWithMemory(t *testing.T, p Planner) (*L4Hook, memory.Store, memory.SubjectResolver) {
	store := memory.NewMemoryStore()
	subj := memory.NewMemSubjectResolver()
	r := NewRunner(New(p), DefaultGate(), NewExitGuard(), NewDistillStats())
	h := NewL4Hook(r).WithMemory(store, subj)
	if h == nil {
		t.Fatal("L4_ENABLED should be on")
	}
	return h, store, subj
}

func TestL4Hook_MemoryPersistsTaskAndRecalled(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	ctx := context.Background()

	// 第一次任务（模拟首次对话）
	h, store, subj := hookWithMemory(t, hookTaskPlanner{})
	reply, handled := h.MaybeHandle(ctx, "wx_alice", "提醒我开会")
	if !handled || !strings.Contains(reply, "提醒") {
		t.Fatalf("task handled=%v reply=%q", handled, reply)
	}
	uid, err := subj.SubjectID(ctx, "wx_alice")
	if err != nil {
		t.Fatal(err)
	}
	// schedule 子 Agent 应落一条情景记忆
	eps, err := store.ListEpisodes(ctx, memory.Scope{UserID: uid, AgentID: string(AgentSchedule)}, memory.EpisodeQuery{Limit: 5})
	if err != nil || len(eps) == 0 {
		t.Fatalf("schedule episode not persisted: n=%d err=%v", len(eps), err)
	}
	if !strings.Contains(eps[0].Summary, "提醒我开会") {
		t.Fatalf("episode summary=%q", eps[0].Summary)
	}

	// 模拟"进程重启"：新建 Hook/Runner，但复用同一个 store（生产里是 PGStore）。
	h2, _, _ := hookWithMemory(t, hookTaskPlanner{})
	h2.Mem = store
	h2.Subj = subj
	sum := h2.buildSummary(ctx, uid)
	if !strings.Contains(sum, "提醒我开会") {
		t.Fatalf("summary after restart must recall task, got:\n%s", sum)
	}

	// 不同用户隔离：Bob 的摘要不应包含 Alice 的任务。
	uidB, _ := subj.SubjectID(ctx, "wx_bob")
	sumB := h2.buildSummary(ctx, uidB)
	if strings.Contains(sumB, "提醒我开会") {
		t.Fatalf("cross-user leak: bob saw alice episode:\n%s", sumB)
	}
}

func TestL4Hook_ChatPassthroughWritesNoMemory(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	ctx := context.Background()
	h, store, subj := hookWithMemory(t, hookChatPlanner{})
	if _, handled := h.MaybeHandle(ctx, "wx_chat", "你好"); handled {
		t.Fatal("chat must passthrough")
	}
	uid, _ := subj.SubjectID(ctx, "wx_chat")
	eps, _ := store.ListEpisodes(ctx, memory.Scope{UserID: uid, AgentID: CommanderAgentID}, memory.EpisodeQuery{Limit: 5})
	if len(eps) != 0 {
		t.Fatalf("passthrough chat must not persist episodes, got %d", len(eps))
	}
}
