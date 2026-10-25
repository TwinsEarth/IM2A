// Package wechatbot 管理微信 Bot 组绑定：
// 一个微信号(wxid)只能绑定一个 Bot 组；组内最多 3 个端(iOS/Android/Windows)。
package wechatbot

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrWxidBound = errors.New("wechatbot: wxid already bound to another group")
var ErrDeviceExists = errors.New("wechatbot: device slot already occupied in group")
var ErrGroupFull = errors.New("wechatbot: group already has 3 devices")
var ErrBadDevice = errors.New("wechatbot: device_type must be ios|android|windows")

var validDevices = map[string]bool{"ios": true, "android": true, "windows": true}

type Group struct {
	ID        string    `json:"id"`
	OwnerUser string    `json:"ownerUser"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type Binding struct {
	ID           string    `json:"id"`
	GroupID      string    `json:"groupId"`
	Wxid         string    `json:"wxid"`
	DeviceType   string    `json:"deviceType"`
	Status       string    `json:"status"`
	BoundAt      time.Time `json:"boundAt"`
	LastOnlineAt time.Time `json:"lastOnlineAt"`
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) CreateGroup(ctx context.Context, id, owner, name string) (*Group, error) {
	if id == "" || owner == "" || name == "" {
		return nil, errors.New("wechatbot: id/owner/name required")
	}
	g := &Group{ID: id, OwnerUser: owner, Name: name, Status: "active", CreatedAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wechat_bot_groups(id,owner_user,name,status) VALUES($1,$2,$3,$4)`,
		g.ID, g.OwnerUser, g.Name, g.Status)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// BindDevice 把微信号绑定到组内某一端。
// 真实约束：wxid 全局唯一(DB UNIQUE 兜底)；组内端数 <=3；组内 device_type 唯一。
func (s *Service) BindDevice(ctx context.Context, id, groupID, wxid, deviceType string) (*Binding, error) {
	if id == "" || groupID == "" || wxid == "" {
		return nil, errors.New("wechatbot: id/groupId/wxid required")
	}
	if !validDevices[deviceType] {
		return nil, ErrBadDevice
	}
	var cnt int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM wechat_bindings WHERE group_id=$1`, groupID).Scan(&cnt); err != nil {
		return nil, err
	}
	if cnt >= 3 {
		return nil, ErrGroupFull
	}
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM wechat_bindings WHERE group_id=$1 AND device_type=$2`, groupID, deviceType).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, ErrDeviceExists
	}
	b := &Binding{ID: id, GroupID: groupID, Wxid: wxid, DeviceType: deviceType, Status: "online", BoundAt: time.Now().UTC(), LastOnlineAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wechat_bindings(id,group_id,wxid,device_type,status) VALUES($1,$2,$3,$4,$5)`,
		b.ID, b.GroupID, b.Wxid, b.DeviceType, b.Status)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrWxidBound
		}
		return nil, err
	}
	return b, nil
}

func (s *Service) Unbind(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM wechat_bindings WHERE id=$1`, id)
	return err
}

func (s *Service) Group(ctx context.Context, groupID string) (*Group, []*Binding, error) {
	g := &Group{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id,owner_user,name,status,created_at FROM wechat_bot_groups WHERE id=$1`, groupID).
		Scan(&g.ID, &g.OwnerUser, &g.Name, &g.Status, &g.CreatedAt)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,group_id,wxid,device_type,status,bound_at,last_online_at
		 FROM wechat_bindings WHERE group_id=$1 ORDER BY bound_at`, groupID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	bs := make([]*Binding, 0)
	for rows.Next() {
		b := &Binding{}
		if err := rows.Scan(&b.ID, &b.GroupID, &b.Wxid, &b.DeviceType, &b.Status, &b.BoundAt, &b.LastOnlineAt); err != nil {
			return nil, nil, err
		}
		bs = append(bs, b)
	}
	return g, bs, rows.Err()
}

func isUniqueViolation(err error) bool {
	msg := err.Error()
	return contains(msg, "uq_wechat_bindings_wxid") || contains(msg, "unique constraint")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
