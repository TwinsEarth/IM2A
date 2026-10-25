package botroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrUnbound = errors.New("botroute: wxid unbound")
	ErrSelf    = errors.New("botroute: cannot route to self")
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) bound(ctx context.Context, wxid string) (bool, error) {
	var c int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM wechat_identity WHERE wxid=$1`, wxid).Scan(&c)
	return c > 0, err
}

// Route 把 fromWxid 的消息路由到 toWxid 的收件箱，返回新消息 id。
func (s *Service) Route(ctx context.Context, fromWxid, toWxid, typ string, payload any) (int64, error) {
	if fromWxid == toWxid {
		return 0, ErrSelf
	}
	fromOK, err := s.bound(ctx, fromWxid)
	if err != nil {
		return 0, err
	}
	toOK, err := s.bound(ctx, toWxid)
	if err != nil {
		return 0, err
	}
	if !fromOK || !toOK {
		return 0, ErrUnbound
	}
	pj, _ := json.Marshal(payload)
	var id int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO bot_route_messages(from_wxid, to_wxid, msg_type, payload) VALUES ($1,$2,$3,$4) RETURNING id`,
		fromWxid, toWxid, typ, pj).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// Inbox 拉发给 wxid 的未投递消息。
func (s *Service) Inbox(ctx context.Context, wxid string, limit int) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, from_wxid, msg_type, payload FROM bot_route_messages
		 WHERE to_wxid=$1 AND delivered=false ORDER BY id LIMIT $2`, wxid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var from, typ string
		var pj []byte
		_ = rows.Scan(&id, &from, &typ, &pj)
		out = append(out, map[string]any{"id": id, "from": from, "type": typ, "payload": string(pj)})
	}
	return out, nil
}

// Deliver 标记已投递。
func (s *Service) Deliver(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE bot_route_messages SET delivered=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("botroute: msg %d not found", id)
	}
	return nil
}
