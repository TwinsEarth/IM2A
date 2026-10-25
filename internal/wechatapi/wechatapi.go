// Package wechatapi 实现微信 Bot 上行协议 v2.0.4：
// 信封解析 → Ed25519 验签 → 时间戳漂移检查 → 幂等 → 类型分发。
package wechatapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"human-agent-im/internal/a2a"
	"human-agent-im/internal/agentdir"
	"human-agent-im/internal/agentquota"
	"human-agent-im/internal/userkey"
	"human-agent-im/internal/wechatid"
)

var (
	ErrSigInvalid  = errors.New("wechatapi: signature invalid")
	ErrTSSkew      = errors.New("wechatapi: timestamp skew > 300s")
	ErrWxidUnbound = errors.New("wechatapi: wxid unbound or signature invalid")
	ErrBadEnvelope = errors.New("wechatapi: bad envelope")
)

type Envelope struct {
	V       int             `json:"v"`
	Wxid    string          `json:"wxid"`
	Ts      int64           `json:"ts"`
	Idem    string          `json:"idem"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Sig     string          `json:"sig"`
}

type Service struct {
	db  *sql.DB
	uk  *userkey.Service
	now func() time.Time
	quota *agentquota.Service
	dir   *agentdir.Service
	a2a   *a2a.Service
	wid   *wechatid.Service
}

func New(db *sql.DB, uk *userkey.Service) *Service {
	return &Service{db: db, uk: uk, now: time.Now}
}

func (e *Envelope) canonical() []byte {
	c := struct {
		V       int             `json:"v"`
		Wxid    string          `json:"wxid"`
		Ts      int64           `json:"ts"`
		Idem    string          `json:"idem"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}{e.V, e.Wxid, e.Ts, e.Idem, e.Type, e.Payload}
	b, _ := json.Marshal(c)
	return b
}

func (s *Service) Handle(ctx context.Context, raw []byte) ([]byte, error) {
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, ErrBadEnvelope
	}
	if e.Wxid == "" || e.Idem == "" || e.Type == "" {
		return nil, ErrBadEnvelope
	}
	if d := s.now().Unix() - e.Ts; d > 300 || d < -300 {
		return nil, ErrTSSkew
	}
	sig, err := base64.StdEncoding.DecodeString(e.Sig)
	if err != nil {
		return nil, ErrSigInvalid
	}
	if err := s.uk.Verify(ctx, e.Wxid, e.canonical(), sig); err != nil {
		return nil, ErrWxidUnbound
	}
	var prev json.RawMessage
	err = s.db.QueryRowContext(ctx, `SELECT response FROM wechat_idem WHERE idem=$1`, e.Idem).Scan(&prev)
	if err == nil {
		return prev, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	resp, err := s.dispatch(ctx, &e)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO wechat_idem(idem,wxid,response) VALUES($1,$2,$3) ON CONFLICT(idem) DO NOTHING`,
		e.Idem, e.Wxid, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Service) dispatch(ctx context.Context, e *Envelope) ([]byte, error) {
	switch e.Type {
	case "bind.confirm":
		var p struct {
			UserID int64 `json:"user_id"`
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &p)
		}
		pub, isNew, err := s.uk.Bind(ctx, e.Wxid, p.UserID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"type": "bind.result", "wxid": e.Wxid,
			"public_key": pub, "is_new": isNew,
		})
	case "agent.create":
		o, err := s.handleAgentCreate(ctx, e.Wxid, e.Payload)
		if err != nil {
			return nil, err
		}
		return json.Marshal(o)
	default:
		return json.Marshal(map[string]any{
			"type": "ack", "echo_type": e.Type, "status": "accepted",
		})
	}
}
