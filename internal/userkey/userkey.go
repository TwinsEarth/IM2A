// Package userkey 实现"微信 ID 当公钥、服务器托管私钥"的身份解锁。
// 模式 A：服务器持有主密钥，为每个 wxid 生成 Ed25519 密钥对，
// 私钥经 AES-256-GCM 加密落库；只有 wxid 绑定且服务器能解密私钥，
// 才能签名/解锁该用户私有数据。
package userkey

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"human-agent-im/internal/envelope"
)

var ErrNotFound = errors.New("userkey: wxid not bound")
var ErrAlreadyBound = errors.New("userkey: wxid already bound to another user")

type Service struct {
	db     *sql.DB
	master []byte
}

func New(db *sql.DB) *Service {
	mk := os.Getenv("IM2A_MASTER_KEY")
	if mk == "" {
		mk = "0123456789abcdef0123456789abcdef" // dev 默认，GA 前必须注入强随机
	}
	return &Service{db: db, master: []byte(mk)}
}

// Bind 为 wxid 生成密钥对并绑定到 userID。已存在则返回已有公钥（不重建）。
func (s *Service) Bind(ctx context.Context, wxid string, userID int64) (pubB64 string, isNew bool, err error) {
	// 已绑定？
	var existUser int64
	err = s.db.QueryRowContext(ctx,
		`SELECT user_id FROM user_keys WHERE wxid=$1`, wxid).Scan(&existUser)
	if err == nil {
		if existUser != userID {
			return "", false, ErrAlreadyBound
		}
		var pub string
		_ = s.db.QueryRowContext(ctx, `SELECT public_key FROM user_keys WHERE wxid=$1`, wxid).Scan(&pub)
		return pub, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	// 生成 Ed25519 密钥对
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", false, err
	}
	nonce, cipher, err := envelope.Encrypt(s.master, priv)
	if err != nil {
		return "", false, err
	}
	pubB64 = base64.StdEncoding.EncodeToString(pub)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_keys(wxid,user_id,public_key,private_enc,private_nonce) VALUES($1,$2,$3,$4,$5)`,
		wxid, userID, pubB64, cipher, nonce)
	if err != nil {
		return "", false, err
	}
	return pubB64, true, nil
}

// Unlock 解密返回该 wxid 的私钥（服务器托管模式）。
func (s *Service) Unlock(ctx context.Context, wxid string) (ed25519.PrivateKey, error) {
	var cipher, nonce []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT private_enc, private_nonce FROM user_keys WHERE wxid=$1`, wxid).Scan(&cipher, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	priv, err := envelope.Decrypt(s.master, nonce, cipher)
	if err != nil {
		return nil, fmt.Errorf("userkey: decrypt private: %w", err)
	}
	return ed25519.PrivateKey(priv), nil
}

// PublicKey 返回 base64 公钥。
func (s *Service) PublicKey(ctx context.Context, wxid string) (string, error) {
	var pub string
	err := s.db.QueryRowContext(ctx, `SELECT public_key FROM user_keys WHERE wxid=$1`, wxid).Scan(&pub)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return pub, err
}

// Sign 用该 wxid 的私钥对 msg 签名。
func (s *Service) Sign(ctx context.Context, wxid string, msg []byte) ([]byte, error) {
	priv, err := s.Unlock(ctx, wxid)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, msg), nil
}

// Verify 用该 wxid 绑定的公钥验签。
func (s *Service) Verify(ctx context.Context, wxid string, msg, sig []byte) error {
	pubB64, err := s.PublicKey(ctx, wxid)
	if err != nil {
		return err
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return errors.New("userkey: signature invalid")
	}
	return nil
}
