package memory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// v2.3.23：真实落库验证 wxid → 记忆命名空间 user_id 的稳定映射。
// 证明：(1) 同一 wxid 多次解析幂等返回同一 id；(2) 不同 wxid 得到不同 id；
// (3) 返回值带高位偏移、不与真实 users.id 小整数撞命名空间；零残留。
func TestPGSubjectResolver(t *testing.T) {
	db := v10OpenPG(t)
	res := NewPGSubjectResolver(db)
	ctx := context.Background()

	wxidA := fmt.Sprintf("probe_subj_a_%d", time.Now().UnixNano())
	wxidB := fmt.Sprintf("probe_subj_b_%d", time.Now().UnixNano())

	// 零残留：删除本测试写入的映射行。
	t.Cleanup(func() {
		for _, w := range []string{wxidA, wxidB} {
			if _, err := db.Exec(`DELETE FROM l4_subjects WHERE wxid=$1`, w); err != nil {
				t.Errorf("cleanup l4_subjects %s: %v", w, err)
			}
		}
	})

	idA1, err := res.SubjectID(ctx, wxidA)
	if err != nil {
		t.Fatalf("first resolve A: %v", err)
	}
	if idA1 < L4SubjectOffset {
		t.Fatalf("subject id %d must be >= offset %d", idA1, L4SubjectOffset)
	}
	idA2, err := res.SubjectID(ctx, wxidA)
	if err != nil {
		t.Fatalf("second resolve A: %v", err)
	}
	if idA1 != idA2 {
		t.Fatalf("resolver not idempotent: %d != %d", idA1, idA2)
	}

	idB, err := res.SubjectID(ctx, wxidB)
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}
	if idB == idA1 {
		t.Fatalf("different wxids must map to different ids, both=%d", idB)
	}

	// 空 wxid 必须报错，不允许写脏数据。
	if _, err := res.SubjectID(ctx, ""); err == nil {
		t.Fatal("empty wxid must error")
	}

	// 记忆落库后可按映射 id 读回（证明高位偏移命名空间可用）。
	st := NewPGStore(db)
	sc := Scope{UserID: idA1, AgentID: "v2323_probe"}
	t.Cleanup(func() {
		for _, tb := range []string{"working_memory", "episodes", "facts"} {
			_, _ = db.Exec(fmt.Sprintf("DELETE FROM %s WHERE user_id=$1 AND agent_id=$2", tb), idA1, sc.AgentID)
		}
	})
	if err := st.PutWorking(ctx, sc, "pref", "美式"); err != nil {
		t.Fatalf("put working: %v", err)
	}
	// 重新解析（模拟进程重启）后同一 wxid 仍能读到。
	idA3, _ := res.SubjectID(ctx, wxidA)
	got, err := st.GetWorking(ctx, Scope{UserID: idA3, AgentID: "v2323_probe"}, "pref")
	if err != nil {
		t.Fatalf("get after restart re-resolve: %v", err)
	}
	if got != "美式" {
		t.Fatalf("remembered value=%v want 美式", got)
	}
}
