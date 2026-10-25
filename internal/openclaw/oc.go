// Package openclaw 实现 v2.1.2 微信扫码绑定会话：
// 发起二维码 → OpenClaw 回调拿到 wxid → 自动 Identify 落库 → 找/建用户组 → BindDevice。
// 真实二维码由外部 OpenClaw Gateway 生成（openclaw-weixin 插件），本端只维护会话状态机。
package openclaw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"human-agent-im/internal/wechatbot"
	"human-agent-im/internal/wechatid"
)

type Session struct {
	QRID       string     `json:"qrId"`
	OwnerUser  string     `json:"ownerUser"`
	DeviceType string     `json:"deviceType"`
	Status     string     `json:"status"`
	Wxid       string     `json:"wxid,omitempty"`
	UserID     int64      `json:"userId,omitempty"`
	GroupID    string     `json:"groupId,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ScannedAt  *time.Time `json:"scannedAt,omitempty"`
}

type Service struct {
	db   *sql.DB
	ids  *wechatid.Service
	bots *wechatbot.Service
}

func New(db *sql.DB, ids *wechatid.Service, bots *wechatbot.Service) *Service {
	return &Service{db: db, ids: ids, bots: bots}
}

var (
	ErrSessionNotFound = errors.New("openclaw: session not found")
	ErrBadSession     = errors.New("openclaw: session not pending")
)

// NewSession 发起一次扫码绑定。qrID 由调用方生成（建议 oc_<nano>），
// ownerUser 是发起绑定的账号（字符串用户标识）。
func (s *Service) NewSession(ctx context.Context, qrID, ownerUser, deviceType string) (*Session, error) {
	if qrID == "" || ownerUser == "" {
		return nil, errors.New("openclaw: qrId/ownerUser required")
	}
	if deviceType == "" {
		deviceType = "unknown"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO openclaw_qr_sessions(qr_id,owner_user,device_type) VALUES($1,$2,$3)`,
		qrID, ownerUser, deviceType)
	if err != nil {
		return nil, err
	}
	return s.Status(ctx, qrID)
}

// Callback 是 OpenClaw 扫码成功后的回调：拿到 wxid 后自动完成 Identify + Bind。
func (s *Service) Callback(ctx context.Context, qrID, wxid string) (*Session, error) {
	if qrID == "" || wxid == "" {
		return nil, errors.New("openclaw: qrId/wxid required")
	}
	var (
		status     string
		ownerUser  string
		deviceType string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT status, owner_user::text, device_type FROM openclaw_qr_sessions WHERE qr_id=$1`,
		qrID).Scan(&status, &ownerUser, &deviceType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return s.fail(ctx, qrID, fmt.Errorf("%w: status=%s", ErrBadSession, status))
	}

	// 1) 识别 wxid → userId
	uid, isNew, err := s.ids.Identify(ctx, wxid)
	if err != nil {
		return s.fail(ctx, qrID, err)
	}
	_ = isNew

	// 2) 找用户的现有组；没有就建一个
	groupID := ""
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM wechat_bot_groups WHERE owner_user=$1 AND status='active' ORDER BY created_at LIMIT 1`,
		ownerUser).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		g, gerr := s.bots.CreateGroup(ctx, "ocg_"+qrID, ownerUser, ownerUser+" 的Bot组")
		if gerr != nil {
			return s.fail(ctx, qrID, gerr)
		}
		groupID = g.ID
	} else if err != nil {
		return s.fail(ctx, qrID, err)
	}

	// 3) 绑设备到组
	bindingID := "ocb_" + qrID
	if _, berr := s.bots.BindDevice(ctx, bindingID, groupID, wxid, deviceType); berr != nil {
		return s.fail(ctx, qrID, berr)
	}

	// 4) 置 bound
	_, err = s.db.ExecContext(ctx,
		`UPDATE openclaw_qr_sessions SET status='bound', wxid=$2, user_id=$3, group_id=$4, scanned_at=now() WHERE qr_id=$1`,
		qrID, wxid, uid, groupID)
	if err != nil {
		return nil, err
	}
	return s.Status(ctx, qrID)
}

func (s *Service) Status(ctx context.Context, qrID string) (*Session, error) {
	var (
		x Session
		sa sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT qr_id, owner_user::text, device_type, status,
		        COALESCE(wxid,''), COALESCE(user_id,0), COALESCE(group_id,''),
		        COALESCE(error,''), created_at, scanned_at
		 FROM openclaw_qr_sessions WHERE qr_id=$1`, qrID).
		Scan(&x.QRID, &x.OwnerUser, &x.DeviceType, &x.Status,
			&x.Wxid, &x.UserID, &x.GroupID, &x.Error, &x.CreatedAt, &sa)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if sa.Valid {
		t := sa.Time
		x.ScannedAt = &t
	}
	return &x, nil
}

func (s *Service) fail(ctx context.Context, qrID string, cause error) (*Session, error) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE openclaw_qr_sessions SET status='failed', error=$2 WHERE qr_id=$1`,
		qrID, cause.Error())
	st, _ := s.Status(ctx, qrID)
	if st == nil {
		return nil, cause
	}
	return st, cause
}
