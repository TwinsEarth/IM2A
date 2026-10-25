package api

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// v1.7.2：已读回执回调真实落 im_read_receipts；群成员加入/退出回调真实同步 im_group_members。
func TestV172_ReadReceiptAndGroupMembership(t *testing.T) {
	d := v12Deps(t)
	h := d.IMMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanIMBySuf(t, d.DB, suf) })

	post := func(cmd, from, to, groupID, msgKey string) *httptest.ResponseRecorder {
		body := []byte(fmt.Sprintf(`{"CallbackCommand":%q,"From_Account":%q,"To_Account":%q,"GroupId":%q,"MsgKey":%q,"MsgBody":[]}`,
			cmd, from, to, groupID, msgKey))
		req := httptest.NewRequest("POST", "/v1/im/webhook?key=v12-wh-secret", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1) 已读回执：alice 已读 bob 的某消息
	alice := "v172_alice_" + suf
	bob := "v172_bob_" + suf
	if rec := post("C2C.MsgReaded", alice, bob, "", "v172read_"+suf); rec.Code != 200 {
		t.Fatalf("read receipt webhook code=%d %s", rec.Code, rec.Body.String())
	}
	var cnt int
	d.DB.QueryRow(`SELECT count(*) FROM im_read_receipts WHERE owner_id=$1 AND peer_id=$2 AND msg_key=$3`,
		alice, bob, "v172read_"+suf).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("read receipt must persist exactly 1, got %d", cnt)
	}

	// 2) 群成员加入：newbie 进群
	gid := "v172g_" + suf
	admin := "v172_admin_" + suf
	newbie := "v172_newbie_" + suf
	if rec := post("Group.AddMember", admin, newbie, gid, "v172join_"+suf); rec.Code != 200 {
		t.Fatalf("add member webhook code=%d", rec.Code)
	}
	d.DB.QueryRow(`SELECT count(*) FROM im_group_members WHERE group_id=$1 AND user_id=$2`, gid, newbie).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("after AddMember newbie must be in group, count=%d", cnt)
	}

	// 3) 群成员退出：newbie 离群
	if rec := post("Group.DeleteMember", admin, newbie, gid, "v172leave_"+suf); rec.Code != 200 {
		t.Fatalf("delete member webhook code=%d", rec.Code)
	}
	d.DB.QueryRow(`SELECT count(*) FROM im_group_members WHERE group_id=$1 AND user_id=$2`, gid, newbie).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("after DeleteMember newbie must be gone, count=%d", cnt)
	}
}
