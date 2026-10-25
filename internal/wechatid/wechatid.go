// Package wechatid 实现"扫码一次即认人"：wxid 长期映射 user_id。
package wechatid

import (
	"context"
	"database/sql"
	"errors"
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// Identify 返回 wxid 对应的 userID。首次扫码分配新 userID(isNew=true)；
// 之后同一 wxid 永远返回同一 userID(isNew=false)，不再开新号。
func (s *Service) Identify(ctx context.Context, wxid string) (userID int64, isNew bool, err error) {
	if wxid == "" {
		return 0, false, errors.New("wechatid: wxid required")
	}
	// 已存在则更新 last_seen 并返回
	err = s.db.QueryRowContext(ctx,
		`UPDATE wechat_identity SET last_seen_at=now() WHERE wxid=$1::text
		 RETURNING user_id`, wxid).Scan(&userID)
	if err == nil {
		return userID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	// 不存在则建
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO wechat_identity(wxid,user_id) VALUES($1::text, nextval('seq_user_id'))
		 RETURNING user_id`, wxid).Scan(&userID)
	if err != nil {
		// 并发下另一请求先建了：回退查询
		err2 := s.db.QueryRowContext(ctx,
			`UPDATE wechat_identity SET last_seen_at=now() WHERE wxid=$1::text RETURNING user_id`, wxid).Scan(&userID)
		if err2 != nil {
			return 0, false, err
		}
		return userID, false, nil
	}
	return userID, true, nil
}
