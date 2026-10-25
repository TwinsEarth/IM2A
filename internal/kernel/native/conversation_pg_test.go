// SPDX-License-Identifier: MIT
//
// PG-gated C2 会话模型判定性测试（真 PG13.23）：
//   - 迁移 043 表存在；
//   - 幂等：同 owner + client_conv_id 重复创建 → 同一会话、不产生第二行；
//   - 上限：GROUP 第 13 个成员被拒（409 MEMBER_LIMIT_REACHED）且**事务回滚**（成员数仍 12）；
//   - 非成员读会话 → CONVERSATION_NOT_FOUND（fail-closed，不泄露存在性）；
//   - 归档：active→archived 单向；归档后加成员 → CONVERSATION_ARCHIVED。
package native

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/store"
)

func openConvPG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; PG-gated C2 conversation test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// uniqueUsers 生成一批互不冲突的 userID（纳秒取模 + 偏移，测试库专用）。
func uniqueUsers(n int) []int64 {
	base := time.Now().UnixNano()%100_000_000*10 + 1_000_000
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = base + int64(i)
	}
	return out
}

func convCleanup(t *testing.T, db *sql.DB, convIDs []string, userIDs []int64) {
	t.Helper()
	t.Cleanup(func() {
		for _, id := range convIDs {
			_, _ = db.Exec(`DELETE FROM n_conversation_members WHERE conversation_id=$1`, id)
			_, _ = db.Exec(`DELETE FROM n_conversations WHERE id=$1`, id)
		}
		for _, uid := range userIDs {
			_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='human' AND user_id=$1`, uid)
		}
	})
}

func TestC2MigrateTables(t *testing.T) {
	db := openConvPG(t)
	ctx := context.Background()
	for _, table := range []string{"n_conversations", "n_conversation_members"} {
		var reg sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(%s): %v", table, err)
		}
		if !reg.Valid || reg.String == "" {
			t.Fatalf("迁移 043 缺表 %s", table)
		}
	}
	// 迁移可重复执行（每次启动都会重跑）。
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
}

func TestC2CreateIdempotent(t *testing.T) {
	db := openConvPG(t)
	s := NewConversationStore(db)
	ctx := context.Background()
	users := uniqueUsers(2)
	owner, peer := users[0], users[1]

	first, created, err := s.Create(ctx, CreateRequest{
		OwnerUserID: owner, Kind: ConvDirect, Title: "idem",
		ClientConvID: "client-conv-1", MemberUserIDs: []int64{peer},
	})
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	convCleanup(t, db, []string{first.ID}, users)

	second, created2, err := s.Create(ctx, CreateRequest{
		OwnerUserID: owner, Kind: ConvDirect, Title: "idem-again",
		ClientConvID: "client-conv-1", MemberUserIDs: []int64{peer},
	})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created2 {
		t.Fatalf("重复 client_conv_id 不应新建会话（created=true）")
	}
	if second.ID != first.ID {
		t.Fatalf("幂等返回不同会话：%q vs %q", first.ID, second.ID)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_conversations WHERE id=$1`, first.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("会话行数=%d, want 1", rows)
	}
	// 幂等键 UNIQUE(owner_principal, client_conv_id)：另一个 owner 可用同名 client id。
	other, createdOther, err := s.Create(ctx, CreateRequest{
		OwnerUserID: 999000000 + owner%1000, Kind: ConvDirect, ClientConvID: "client-conv-1", MemberUserIDs: []int64{owner},
	})
	if err != nil || !createdOther {
		t.Fatalf("其他 owner 复用 client id 应成功: created=%v err=%v", createdOther, err)
	}
	convCleanup(t, db, []string{other.ID}, []int64{999000000 + owner%1000})
	t.Logf("C2_IDEMPOTENT_OK: same id=%s, second created=false, rows=1（per-owner 唯一）", first.ID)
}

func TestC2GroupMemberLimitRollback(t *testing.T) {
	db := openConvPG(t)
	s := NewConversationStore(db)
	ctx := context.Background()
	users := uniqueUsers(13)                // owner + 12 候选
	owner, initial := users[0], users[1:12] // 1 owner + 11 initial = 12（上限）
	conv, created, err := s.Create(ctx, CreateRequest{
		OwnerUserID: owner, Kind: ConvGroup, Title: "limit", MemberUserIDs: initial,
	})
	if err != nil || !created {
		t.Fatalf("create group: created=%v err=%v", created, err)
	}
	convCleanup(t, db, []string{conv.ID}, users)
	if got := len(conv.Members); got != 12 {
		t.Fatalf("初始成员=%d, want 12", got)
	}
	// 第 13 个成员：必须具名拒绝且事务回滚（成员数仍 12）。
	_, err = s.AddMember(ctx, conv.ID, owner, users[12], RoleMember)
	if CodeOf(err) != CodeMemberLimit {
		t.Fatalf("第 13 个成员错误码=%q, want %q (err=%v)", CodeOf(err), CodeMemberLimit, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_conversation_members WHERE conversation_id=$1 AND left_at IS NULL`, conv.ID).Scan(&count); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if count != 12 {
		t.Fatalf("超限后成员数=%d, want 12（事务必须回滚）", count)
	}
	// 重新读会话确认成员数一致。
	view, err := s.Get(ctx, conv.ID, owner)
	if err != nil || len(view.Members) != 12 {
		t.Fatalf("Get after limit: members=%d err=%v", len(view.Members), err)
	}
	t.Logf("C2_LIMIT_OK: GROUP 第13成员被拒 code=%s，回滚后成员数=12", CodeMemberLimit)
}

func TestC2NonMember404(t *testing.T) {
	db := openConvPG(t)
	s := NewConversationStore(db)
	ctx := context.Background()
	users := uniqueUsers(3)
	owner, peer, stranger := users[0], users[1], users[2]
	conv, _, err := s.Create(ctx, CreateRequest{OwnerUserID: owner, Kind: ConvDirect, MemberUserIDs: []int64{peer}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	convCleanup(t, db, []string{conv.ID}, users)
	// 非成员（stranger 已有 principal？没有 principal 的行也必须是 404，不泄露存在性）。
	_, err = s.Get(ctx, conv.ID, stranger)
	if CodeOf(err) != CodeNotFound {
		t.Fatalf("非成员读会话 code=%q, want %q (err=%v)", CodeOf(err), CodeNotFound, err)
	}
	// 成员可读。
	if _, err := s.Get(ctx, conv.ID, peer); err != nil {
		t.Fatalf("成员读会话失败: %v", err)
	}
	t.Logf("C2_VISIBILITY_OK: 非成员 → 404 %s（fail-closed）", CodeNotFound)
}

func TestC2ArchiveStateMachine(t *testing.T) {
	db := openConvPG(t)
	s := NewConversationStore(db)
	ctx := context.Background()
	users := uniqueUsers(3)
	owner, peer, other := users[0], users[1], users[2]
	conv, _, err := s.Create(ctx, CreateRequest{OwnerUserID: owner, Kind: ConvDirect, MemberUserIDs: []int64{peer}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	convCleanup(t, db, []string{conv.ID}, users)
	// 非 owner 归档 → 403 ROLE_FORBIDDEN。
	if _, err := s.Archive(ctx, conv.ID, peer); CodeOf(err) != CodeRoleForbidden {
		t.Fatalf("非 owner 归档 code=%q (err=%v)", CodeOf(err), err)
	}
	arch, err := s.Archive(ctx, conv.ID, owner)
	if err != nil {
		t.Fatalf("owner archive: %v", err)
	}
	if arch.Status != string(ConvStatusArchived) || arch.ArchivedAt == nil {
		t.Fatalf("archive view = %+v", arch)
	}
	// 已归档 → 成员写操作必须 410 CONVERSATION_ARCHIVED。
	if _, err := s.AddMember(ctx, conv.ID, owner, other, RoleMember); CodeOf(err) != CodeArchived {
		t.Fatalf("归档后加成员 code=%q, want %q (err=%v)", CodeOf(err), CodeArchived, err)
	}
	if err := s.Leave(ctx, conv.ID, peer); CodeOf(err) != CodeArchived {
		t.Fatalf("归档后退出 code=%q (err=%v)", CodeOf(err), err)
	}
	// 重复归档幂等成功；归档会话仍可读。
	arch2, err := s.Archive(ctx, conv.ID, owner)
	if err != nil {
		t.Fatalf("archive twice: %v", err)
	}
	if arch2.Status != string(ConvStatusArchived) {
		t.Fatalf("archive twice status=%s", arch2.Status)
	}
	if _, err := s.Get(ctx, conv.ID, peer); err != nil {
		t.Fatalf("归档会话应仍可读: %v", err)
	}
	t.Logf("C2_ARCHIVE_OK: 非owner=403 → owner archive → 写操作=410，重复归档幂等")
}

func TestC2TransferAndLeaveRules(t *testing.T) {
	db := openConvPG(t)
	s := NewConversationStore(db)
	ctx := context.Background()
	users := uniqueUsers(3)
	owner, peer, third := users[0], users[1], users[2]
	conv, _, err := s.Create(ctx, CreateRequest{OwnerUserID: owner, Kind: ConvGroup, MemberUserIDs: []int64{peer, third}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	convCleanup(t, db, []string{conv.ID}, users)
	// owner 直接 leave → 409 OWNER_MUST_TRANSFER。
	if err := s.Leave(ctx, conv.ID, owner); CodeOf(err) != CodeOwnerMustTransfer {
		t.Fatalf("owner leave code=%q (err=%v)", CodeOf(err), err)
	}
	// transfer 给 peer → owner_principal 变化、角色互换。
	if err := s.TransferOwner(ctx, conv.ID, owner, peer); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	view, err := s.Get(ctx, conv.ID, peer)
	if err != nil {
		t.Fatalf("get after transfer: %v", err)
	}
	if view.OwnerUserID != peer {
		t.Fatalf("owner_user_id=%d, want %d", view.OwnerUserID, peer)
	}
	// 旧 owner 现在可 leave。
	if err := s.Leave(ctx, conv.ID, owner); err != nil {
		t.Fatalf("old owner leave after transfer: %v", err)
	}
	removed := false
	for _, m := range view.Members {
		if m.UserID == owner && m.Role == string(RoleAdmin) {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("旧 owner 未降为 admin: %+v", view.Members)
	}
	t.Logf("C2_TRANSFER_OK: owner leave=409 → transfer → owner_user_id=%d，旧owner=admin 可 leave", peer)
}

func TestC2KindLimitsTable(t *testing.T) {
	cases := []struct {
		kind ConvKind
		max  int
	}{
		{ConvDirect, 2}, {ConvGroup, 12}, {ConvTask, 8}, {ConvAgentNegotiation, 4},
	}
	for _, c := range cases {
		if got := c.kind.MaxMembers(); got != c.max {
			t.Fatalf("%s.MaxMembers()=%d, want %d", c.kind, got, c.max)
		}
	}
	if ConvKind("BOGUS").valid() {
		t.Fatal("非法 kind 不应 valid")
	}
	t.Logf("C2_LIMIT_TABLE: DIRECT=2 GROUP=12 TASK=8 AGENT_NEGOTIATION=4；错误码表: %s=409, %s=404, %s=410",
		CodeMemberLimit, CodeNotFound, CodeArchived)
}

// 防未使用告警（fmt 供失败信息扩展）。
var _ = fmt.Sprintf
