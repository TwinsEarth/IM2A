package api

import (
	"database/sql"
	"testing"
)

// 本文件集中提供 real_test 的零残留清理 helper。
//
// 铁律：每个真实落库测试结束必须删除自己写入的「业务数据」行；清理失败必须 t.Errorf。
//
// 例外（合规设计，不是残留）：audit_events 由 trg_audit_no_update/trg_audit_no_delete
// 强制为 append-only（WORM 不可变审计），任何 DELETE/UPDATE 都会被数据库拒绝。
// 因此审计事件一律不删，测试必须：
//   1) 用唯一 agent_id / detail.ticketId 写入，使测试事件可被精确识别与隔离；
//   2) 断言基于唯一标识或相对增量（>= 本次新增），不得依赖全表固定计数。
// 这与生产中审计回放/压测/运维产生的事件处理方式一致：永久保留，靠归档/分区管理。

// cleanIMBySuf 按本次测试唯一 suf（匹配 %suf%）清理人类 IM 通道全部业务表。
func cleanIMBySuf(t *testing.T, db *sql.DB, suf string) {
	t.Helper()
	p := "%" + suf + "%"
	stmts := []string{
		`DELETE FROM im_group_messages WHERE group_id LIKE $1 OR from_user LIKE $1`,
		`DELETE FROM im_group_members WHERE group_id LIKE $1 OR user_id LIKE $1`,
		`DELETE FROM im_groups WHERE group_id LIKE $1 OR owner_id LIKE $1`,
		`DELETE FROM im_read_receipts WHERE owner_id LIKE $1 OR peer_id LIKE $1 OR msg_key LIKE $1`,
		`DELETE FROM im_relations WHERE owner_id LIKE $1 OR peer_id LIKE $1`,
		`DELETE FROM im_messages WHERE from_user LIKE $1 OR to_user LIKE $1 OR msg_key LIKE $1`,
		`DELETE FROM im_webhook_events WHERE from_user LIKE $1 OR to_user LIKE $1 OR group_id LIKE $1 OR msg_key LIKE $1`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q, p); err != nil {
			t.Errorf("cleanup IM %s: %v", suf, err)
		}
	}
}

// cleanPushByUser 清理推送凭据与任务（user_id LIKE）。
func cleanPushByUser(t *testing.T, db *sql.DB, userLike string) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM im_push_tasks WHERE user_id LIKE $1`,
		`DELETE FROM im_push_credentials WHERE user_id LIKE $1`,
	} {
		if _, err := db.Exec(q, userLike); err != nil {
			t.Errorf("cleanup push %s: %v", userLike, err)
		}
	}
}

// cleanTicket 清理审批单的业务状态/事件/离线缓存（按 ticket_id）。
// 注意：不触碰 append-only 的 audit_events（WORM），审批审计行保留并靠 ticketId 隔离。
func cleanTicket(t *testing.T, db *sql.DB, ticketIDs ...string) {
	t.Helper()
	for _, id := range ticketIDs {
		for _, q := range []string{
			`DELETE FROM approval_events WHERE ticket_id=$1`,
			`DELETE FROM ticket_state WHERE ticket_id=$1`,
			`DELETE FROM offline_approvals WHERE ticket_id=$1`,
			`DELETE FROM approval_tickets WHERE id=$1`,
		} {
			if _, err := db.Exec(q, id); err != nil {
				t.Errorf("cleanup ticket %s [%s]: %v", id, q[:32], err)
			}
		}
	}
}

// cleanDevice 删除一台测试设备。
func cleanDevice(t *testing.T, db *sql.DB, deviceID string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM devices WHERE id=$1`, deviceID); err != nil {
		t.Errorf("cleanup device %s: %v", deviceID, err)
	}
}

// cleanMessagesByUsers 清理人类消息测试（数字 uid）。
func cleanMessagesByUsers(t *testing.T, db *sql.DB, uids ...uint64) {
	t.Helper()
	for _, uid := range uids {
		if _, err := db.Exec(`DELETE FROM messages WHERE sender_id=$1 OR recipient_id=$1`, uid); err != nil {
			t.Errorf("cleanup messages uid=%d: %v", uid, err)
		}
	}
}

// cleanNegotiation 清理协商会话及其轮次。
func cleanNegotiation(t *testing.T, db *sql.DB, sessionID string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM negotiation_turns WHERE session_id=$1`, sessionID); err != nil {
		t.Errorf("cleanup negotiation_turns: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM negotiation_sessions WHERE id=$1`, sessionID); err != nil {
		t.Errorf("cleanup negotiation_sessions: %v", err)
	}
}
