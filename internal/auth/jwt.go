package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Claims 是 IM2A 访问令牌的声明集。sub 为用户 id（字符串形式以符合 JWT 规范）。
type Claims struct {
	Sub int64    `json:"sub"`
	Iat int64    `json:"iat"`
	Exp int64    `json:"exp"`
	Iss string   `json:"iss"`
	Jti string   `json:"jti"`
}

// JWT 校验/签发错误。
var (
	ErrMalformed = errors.New("jwt: malformed token")
	ErrBadAlg    = errors.New("jwt: alg not HS256")
	ErrBadSig    = errors.New("jwt: bad signature")
	ErrExpired   = errors.New("jwt: token expired")
)

const jwtAlg = "HS256"

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// Issue 用 HMAC-SHA256 签发一个访问令牌。ttl<=0 时默认 72h。
func Issue(secret string, userID int64, ttl time.Duration, iss string) (string, error) {
	if secret == "" {
		return "", errors.New("jwt: empty secret")
	}
	if ttl == 0 {
		ttl = 72 * time.Hour
	}
	now := time.Now()
	c := Claims{
		Sub: userID,
		Iat: now.Unix(),
		Exp: now.Add(ttl).Unix(),
		Iss: iss,
		Jti: strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.FormatInt(userID, 10),
	}
	hb, _ := json.Marshal(map[string]string{"alg": jwtAlg, "typ": "JWT"})
	pb, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := b64url(hb) + "." + b64url(pb)
	sig := hmacSig(secret, []byte(signing))
	return signing + "." + b64url(sig), nil
}

func hmacSig(secret string, msg []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(msg)
	return mac.Sum(nil)
}

// Parse 校验签名与过期，返回 Claims。任何不合法都返回非 nil error。
func Parse(secret, token string) (*Claims, error) {
	if secret == "" {
		return nil, errors.New("jwt: empty secret")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	hbp, pbp, sigp := parts[0], parts[1], parts[2]
	hb, err := unb64url(hbp)
	if err != nil {
		return nil, ErrMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, ErrMalformed
	}
	if hdr.Alg != jwtAlg {
		return nil, ErrBadAlg
	}
	// 恒定时间比较签名。
	want, err := unb64url(sigp)
	if err != nil {
		return nil, ErrMalformed
	}
	got := hmacSig(secret, []byte(hbp+"."+pbp))
	if !hmac.Equal(want, got) {
		return nil, ErrBadSig
	}
	pb, err := unb64url(pbp)
	if err != nil {
		return nil, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, ErrMalformed
	}
	if time.Now().Unix() >= c.Exp {
		return nil, ErrExpired
	}
	return &c, nil
}
