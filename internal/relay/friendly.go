package relay

import (
	"context"
	"database/sql"
	"strings"
)

// fallbackName 无自定义昵称时的内置称呼（占位：小明/小红）。
func fallbackName(wxid string) string {
	switch {
	case strings.Contains(wxid, "o9cq80y8gA2CW4Y9RPgJxjpYAolE"):
		return "小明"
	case strings.Contains(wxid, "o9cq800WGOBtIY_aA54I"):
		return "小红"
	default:
		return "对方"
	}
}

type nameCacheEntry struct {
	user  string
	agent string
}

func (s *Service) nameCacheInit() {
	s.nameOnce.Do(func() {
		s.nameMu.Lock()
		s.nameMap = map[string]nameCacheEntry{}
		s.nameMu.Unlock()
	})
}

// loadNames 从 wechat_identity 读取昵称（带本地缓存）。
func (s *Service) loadNames(ctx context.Context, wxid string) nameCacheEntry {
	if s.db == nil {
		return nameCacheEntry{}
	}
	s.nameCacheInit()
	s.nameMu.RLock()
	if v, ok := s.nameMap[wxid]; ok {
		s.nameMu.RUnlock()
		return v
	}
	s.nameMu.RUnlock()

	var e nameCacheEntry
	var u, a sql.NullString
	_ = s.db.QueryRowContext(ctx, `SELECT user_name, agent_name FROM wechat_identity WHERE wxid=$1`, wxid).Scan(&u, &a)
	if u.Valid {
		e.user = strings.TrimSpace(u.String)
	}
	if a.Valid {
		e.agent = strings.TrimSpace(a.String)
	}
	s.nameMu.Lock()
	s.nameMap[wxid] = e
	s.nameMu.Unlock()
	return e
}

// userName 返回用户对外显示名（自定义昵称优先，否则内置名）。
func (s *Service) userName(ctx context.Context, wxid string) string {
	if n := s.loadNames(ctx, wxid).user; n != "" {
		return n
	}
	return fallbackName(wxid)
}

// agentName 返回用户给自己 Agent 起的名字；空串表示未起名。
func (s *Service) agentName(ctx context.Context, wxid string) string {
	return s.loadNames(ctx, wxid).agent
}

// setName 落库昵称并失效缓存。kind 为 user_name / agent_name。
func (s *Service) setName(ctx context.Context, wxid, kind, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wechat_identity SET `+kind+`=$1 WHERE wxid=$2`, name, wxid)
	if err == nil {
		s.nameCacheInit()
		s.nameMu.Lock()
		delete(s.nameMap, wxid)
		s.nameMu.Unlock()
	}
	return err
}

// peerOutText 构造转给对方手机的自然消息：发送者昵称：正文；不带 id/单号。
func (s *Service) peerOutText(ctx context.Context, fromWxid, body string) string {
	return s.userName(ctx, fromWxid) + "：" + strings.TrimSpace(body)
}
