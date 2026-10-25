package wechatbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestWechatBotGroup(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { d.Close() })
	suf := fmt.Sprintf("v200_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_bindings WHERE wxid LIKE $1 OR group_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_bot_groups WHERE id LIKE $1`, suf+"%")
	})
	ctx := context.Background()
	s := New(d)

	gid := suf + "_g1"
	if _, err := s.CreateGroup(ctx, gid, "owner1", "我的Bot组"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 绑三端
	for _, dev := range []string{"ios", "android", "windows"} {
		b, err := s.BindDevice(ctx, suf+"_b_"+dev, gid, suf+"_wx_"+dev, dev)
		if err != nil { t.Fatalf("bind %s: %v", dev, err) }
		_ = b
	}
	// 第四端 → ErrGroupFull
	if _, err := s.BindDevice(ctx, suf+"_b4", gid, suf+"_wx4", "ios"); !errors.Is(err, ErrGroupFull) {
		t.Fatalf("group full: %v", err)
	}
	// 同 device 重复（新组）→ ErrDeviceExists
	_, _ = s.BindDevice(ctx, suf+"_b_ios", gid, suf+"_wx_dup", "ios") // 已被占用 → 但组已满，先看顺序
	// 注：组已满会先返回 ErrGroupFull；用新组验证 ErrDeviceExists
	g2 := suf + "_g2"
	if _, err := s.CreateGroup(ctx, g2, "owner2", "第二组"); err != nil { t.Fatal(err) }
	if _, err := s.BindDevice(ctx, suf+"_b2_ios", g2, suf+"_wx_ios2", "ios"); err != nil { t.Fatal(err) }
	if _, err := s.BindDevice(ctx, suf+"_b2_ios2", g2, suf+"_wx_ios3", "ios"); !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("device exists: %v", err)
	}
	// 同 wxid 绑另一个组 → ErrWxidBound
	if _, err := s.BindDevice(ctx, suf+"_b2_android", g2, suf+"_wx_ios2", "android"); !errors.Is(err, ErrWxidBound) {
		t.Fatalf("wxid bound: %v", err)
	}
	// 坏 device
	if _, err := s.BindDevice(ctx, suf+"_bb", g2, suf+"_wx_bad", "linux"); !errors.Is(err, ErrBadDevice) {
		t.Fatalf("bad device: %v", err)
	}
	// 查组
	g, bs, err := s.Group(ctx, gid)
	if err != nil || g.Name != "我的Bot组" || len(bs) != 3 {
		t.Fatalf("group: name=%s bs=%d err=%v", g.Name, len(bs), err)
	}
	// Unbind 一个后剩 2
	if err := s.Unbind(ctx, suf+"_b_ios"); err != nil { t.Fatalf("unbind: %v", err) }
	_, bs2, _ := s.Group(ctx, gid)
	if len(bs2) != 2 {
		t.Fatalf("after unbind=%d", len(bs2))
	}
}
