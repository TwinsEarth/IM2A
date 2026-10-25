package agentregistry

import (
	"context"
	"testing"
)

// v2.3.24：首消息自动开通 + 读真实信任档（不被重复开通覆盖）。
func TestRegistry_ProvisionTrustReal(t *testing.T) {
	db := openReal(t)
	defer db.Close()
	s := New(db)
	ctx := context.Background()
	wx := "probe_wx_provision"

	_, _ = db.ExecContext(ctx, `DELETE FROM agent_registry WHERE user_wxid=$1`, wx)
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM agent_registry WHERE user_wxid=$1`, wx) }()

	// 1) 首次开通：返回默认信任，且 6 个标准 Agent 全部注册。
	trust, err := s.TrustLevel(ctx, wx)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if trust != DefaultTrust {
		t.Fatalf("new user trust=%d want %d", trust, DefaultTrust)
	}
	list, err := s.ListByUser(ctx, wx)
	if err != nil || len(list) != 6 {
		t.Fatalf("want 6 agents, got %d err=%v", len(list), err)
	}

	// 2) 运营/管理员把 master 信任档调到 5、把一个子 Agent 暂停；
	//    再次开通（用户又发消息）不得覆盖这些人工设置。
	if _, err := db.ExecContext(ctx,
		`UPDATE agent_registry SET trust_level=5 WHERE user_wxid=$1 AND agent_id='master'`, wx); err != nil {
		t.Fatal(err)
	}
	trust2, err := s.TrustLevel(ctx, wx)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if trust2 != 5 {
		t.Fatalf("admin trust must be preserved, got %d want 5", trust2)
	}

	// 3) master 被暂停后，TrustLevel 返回 ErrPaused。
	if err := s.SetStatus(ctx, wx, "master", "paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrustLevel(ctx, wx); err != ErrPaused {
		t.Fatalf("paused master should yield ErrPaused, got %v", err)
	}

	// 4) 空 wxid 拒绝。
	if _, err := s.TrustLevel(ctx, ""); err == nil {
		t.Fatal("empty wxid must error")
	}
}
