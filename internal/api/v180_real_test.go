package api

import (
	"strconv"
	"testing"
	"time"
)

// v1.7.10 内容审核：自动规则扫描（allowed/review/blocked）+ 人工队列流转 + 规则启停 + 落库/审计断言。
func TestV180_ContentModeration(t *testing.T) {
	d := v12Deps(t)
	admin := d.AdminMux()
	biz := d.ModerationMux()

	ns := time.Now().UnixNano()
	suf := strconv.FormatInt(ns, 10)
	revCode, revWord := "v180_review_"+suf, "v180pend"+strconv.FormatInt(ns, 36)
	blkCode, blkWord := "v180_block_"+suf, "v180badw"+strconv.FormatInt(ns, 36)
	refClean, refReview, refBlock, refPhone := "v180ref-clean-"+suf, "v180ref-review-"+suf, "v180ref-block-"+suf, "v180ref-phone-"+suf

	// audit_events append-only：审计行保留不删，按唯一 ref(message_id) 精确断言。
	t.Cleanup(func() {
		d.DB.Exec(`DELETE FROM content_moderation_queue WHERE ref LIKE 'v180ref-%'`)
		d.DB.Exec(`DELETE FROM content_rules WHERE code LIKE 'v180\_%'`)
		// audit_events append-only：审计行保留不删。
	})

	// 1. 新建一条 review 规则 + 一条 block 规则
	_, rv := v178Req(admin, "POST", "/v1/admin/moderation/rules",
		`{"code":"`+revCode+`","rule_type":1,"pattern":"`+revWord+`","action":2}`)
	revRule := rv["rule"].(map[string]any)
	_, bv := v178Req(admin, "POST", "/v1/admin/moderation/rules",
		`{"code":"`+blkCode+`","rule_type":1,"pattern":"`+blkWord+`","action":1}`)
	blkRule := bv["rule"].(map[string]any)
	blkID := int(blkRule["id"].(float64))
	_ = revRule

	// 2. 扫描：干净
	code, clean := v178Req(biz, "POST", "/v1/moderation/scan", `{"ref":"`+refClean+`","content":"正常商务拜访确认，周三下午"}`)
	if code != 200 || clean["decision"] != "allowed" || int(clean["queue_id"].(float64)) != 0 {
		t.Fatalf("clean scan: code=%d %v", code, clean)
	}
	// 转人工
	code, rev := v178Req(biz, "POST", "/v1/moderation/scan", `{"ref":"`+refReview+`","content":"请处理 `+revWord+` 事宜"}`)
	if code != 200 || rev["decision"] != "review" || int(rev["queue_id"].(float64)) == 0 {
		t.Fatalf("review scan: code=%d %v", code, rev)
	}
	reviewQID := int(rev["queue_id"].(float64))
	// 拦截（关键词）
	code, blk := v178Req(biz, "POST", "/v1/moderation/scan", `{"ref":"`+refBlock+`","content":"含 `+blkWord+` 必须拦截"}`)
	if code != 200 || blk["decision"] != "blocked" {
		t.Fatalf("block scan: code=%d %v", code, blk)
	}
	blockQID := int(blk["queue_id"].(float64))
	// 拦截（预置手机号规则）
	code, ph := v178Req(biz, "POST", "/v1/moderation/scan", `{"ref":"`+refPhone+`","content":"电话 13912345678 联系"}`)
	if code != 200 || ph["decision"] != "blocked" {
		t.Fatalf("phone scan: code=%d %v", code, ph)
	}
	phoneHits := ph["hits"].([]any)
	if len(phoneHits) == 0 || phoneHits[0].(string) != "phone" {
		t.Fatalf("phone hits wrong: %v", phoneHits)
	}

	// 3. 待审队列：clean 不入队，其余 3 条
	code, q := v178Req(admin, "GET", "/v1/admin/moderation/queue?status=0&limit=200", "")
	if code != 200 {
		t.Fatalf("queue code %d", code)
	}
	refs := map[string]bool{}
	for _, it := range q["items"].([]any) {
		refs[it.(map[string]any)["ref"].(string)] = true
	}
	for _, r := range []string{refReview, refBlock, refPhone} {
		if !refs[r] {
			t.Fatalf("queue missing %s", r)
		}
	}
	if refs[refClean] {
		t.Fatalf("clean content must not enter queue")
	}

	// 4. 人工：review 条放行；block 条误报
	code, ar := v178Req(admin, "POST", "/v1/admin/moderation/"+strconv.Itoa(reviewQID)+"/review",
		`{"decision":"allow","reviewer":"v180tester","note":"没问题"}`)
	if code != 200 || ar["item"].(map[string]any)["manual_status"].(float64) != 1 {
		t.Fatalf("manual allow: code=%d %v", code, ar)
	}
	code, fp := v178Req(admin, "POST", "/v1/admin/moderation/"+strconv.Itoa(blockQID)+"/review",
		`{"decision":"false_positive","reviewer":"v180tester"}`)
	if code != 200 || fp["item"].(map[string]any)["manual_status"].(float64) != 3 {
		t.Fatalf("false positive: code=%d %v", code, fp)
	}
	// 落库断言 reviewer
	var reviewer string
	if err := d.DB.QueryRow(`SELECT reviewer FROM content_moderation_queue WHERE id=$1`, reviewQID).Scan(&reviewer); err != nil || reviewer != "v180tester" {
		t.Fatalf("reviewer not persisted: %q err=%v", reviewer, err)
	}

	// 5. 规则停用后不再拦截
	code, _ = v178Req(admin, "POST", "/v1/admin/moderation/rules/"+strconv.Itoa(blkID)+"/toggle", `{"enabled":0}`)
	if code != 200 {
		t.Fatalf("disable rule code %d", code)
	}
	_, off := v178Req(biz, "POST", "/v1/moderation/scan", `{"ref":"v180ref-off-`+suf+`","content":"含 `+blkWord+` 但规则已停用"}`)
	if off["decision"] != "allowed" {
		t.Fatalf("disabled rule should allow, got %v", off["decision"])
	}
	v178Req(admin, "POST", "/v1/admin/moderation/rules/"+strconv.Itoa(blkID)+"/toggle", `{"enabled":1}`)

	// 6. 审计断言：CONTENT_BLOCKED 已落库
	var n int
	if err := d.DB.QueryRow(`SELECT count(*) FROM audit_events WHERE message_id=$1 AND action='CONTENT_BLOCKED'`, refBlock).Scan(&n); err != nil || n != 1 {
		t.Fatalf("CONTENT_BLOCKED audit not persisted: n=%d err=%v", n, err)
	}
	var na int
	if err := d.DB.QueryRow(`SELECT count(*) FROM audit_events WHERE message_id=$1 AND action='CONTENT_ALLOWED'`, refClean).Scan(&na); err != nil || na != 1 {
		t.Fatalf("CONTENT_ALLOWED audit not persisted: n=%d err=%v", na, err)
	}
}
